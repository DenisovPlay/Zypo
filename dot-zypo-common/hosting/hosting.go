package hosting

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dot-zypo/daemon/common/node"
	"github.com/fsnotify/fsnotify"
)

var (
	proxyCache    map[string]map[string]string // sitesDir -> domain -> targetURL
	proxyMu       sync.RWMutex
	proxyOnce     sync.Once
	proxyWatchers map[string]*fsnotify.Watcher
	faucetLimits  sync.Map
)

func initProxyCache() {
	proxyCache = make(map[string]map[string]string)
	proxyWatchers = make(map[string]*fsnotify.Watcher)
}

func loadProxyConfig(sitesDir string) map[string]string {
	proxyConfigPath := filepath.Join(sitesDir, "reverse_proxy.json")
	proxyData, err := os.ReadFile(proxyConfigPath)
	if err != nil {
		return nil
	}
	var proxyMap map[string]string
	if err := json.Unmarshal(proxyData, &proxyMap); err != nil {
		log.Printf("[Hosting] Error unmarshaling %s: %v", proxyConfigPath, err)
		return nil
	}
	return proxyMap
}

func startProxyWatcher(sitesDir string, watcher *fsnotify.Watcher) {
	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
					if strings.HasSuffix(event.Name, "reverse_proxy.json") {
						log.Printf("[Hosting] Reloading proxy config for %s due to file change", sitesDir)
						newMap := loadProxyConfig(sitesDir)
						proxyMu.Lock()
						proxyCache[sitesDir] = newMap
						proxyMu.Unlock()
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[Hosting] fsnotify error: %v", err)
			}
		}
	}()

	err := watcher.Add(sitesDir)
	if err != nil {
		log.Printf("[Hosting] Failed to watch sitesDir %s: %v", sitesDir, err)
	}
}

func getProxyTarget(sitesDir, domain string) (string, bool) {
	proxyOnce.Do(initProxyCache)

	proxyMu.RLock()
	cache, exists := proxyCache[sitesDir]
	proxyMu.RUnlock()

	if !exists {
		proxyMu.Lock()
		// Double check
		if _, exists = proxyCache[sitesDir]; !exists {
			cache = loadProxyConfig(sitesDir)
			proxyCache[sitesDir] = cache

			// Setup watcher while we have the lock, but don't block
			if _, hasWatcher := proxyWatchers[sitesDir]; !hasWatcher {
				watcher, err := fsnotify.NewWatcher()
				if err == nil {
					proxyWatchers[sitesDir] = watcher
					startProxyWatcher(sitesDir, watcher)
				} else {
					log.Printf("[Hosting] Failed to create fsnotify watcher: %v", err)
				}
			}
		} else {
			cache = proxyCache[sitesDir]
		}
		proxyMu.Unlock()
	}

	if cache == nil {
		return "", false
	}

	target, ok := cache[domain]
	return target, ok
}

type socketWrapper struct {
	r io.Reader
	w io.Writer
	c io.Closer
}

func (s *socketWrapper) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *socketWrapper) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *socketWrapper) Close() error                { return s.c.Close() }

// GetResourceData resolves a request to a local file, WASM app, or reverse proxy.
func GetResourceData(n *node.ZypoNode, req *node.ZypoRequest, domain, path string, bodyReader io.Reader) (node.ZypoHeader, io.ReadCloser, error) {
	cfg := n.GetConfig()

	// Ensure sites dir is used consistently and correctly resolved
	sitesDir := cfg.SitesDir
	if sitesDir == "" {
		sitesDir = "zypo_sites"
	}

	absSitesDir, _ := filepath.Abs(sitesDir)

	// Special case: internal API calls over P2P (for mesh-based finance)
	if domain == "api.zypo" {
		return node.ZypoHeader{Status: 404}, nil, fmt.Errorf("api handled by control logic")
	}

	// Zypo Faucet API (Command Center only)
	if domain == "faucet.zypo" && path == "api/claim" {
		if !cfg.IsCommandCenter {
			return node.ZypoHeader{Status: 403, Mime: "application/json", Size: 22}, io.NopCloser(bytes.NewReader([]byte(`{"error":"Forbidden"}`))), nil
		}
		
		peerID := req.RemotePeer
		if peerID == "" {
			return node.ZypoHeader{Status: 400, Mime: "application/json", Size: 23}, io.NopCloser(bytes.NewReader([]byte(`{"error":"No Peer ID"}`))), nil
		}

		lastTime, ok := faucetLimits.Load(peerID)
		if ok && time.Since(lastTime.(time.Time)) < 24*time.Hour {
			return node.ZypoHeader{Status: 429, Mime: "application/json", Size: 30}, io.NopCloser(bytes.NewReader([]byte(`{"error":"Rate limit exceed"}`))), nil
		}

		if err := n.EconomyManager.ProcessFaucet(peerID); err != nil {
			msg := fmt.Sprintf(`{"error":"Faucet error: %v"}`, err)
			return node.ZypoHeader{Status: 500, Mime: "application/json", Size: int64(len(msg))}, io.NopCloser(bytes.NewReader([]byte(msg))), nil
		}

		faucetLimits.Store(peerID, time.Now())
		resp := `{"success":true,"message":"10 ZPCN sent to your wallet"}`
		return node.ZypoHeader{Status: 200, Mime: "application/json", Size: int64(len(resp))}, io.NopCloser(bytes.NewReader([]byte(resp))), nil
	}

	domainDir := filepath.Join(absSitesDir, domain)

	// Check for Serverless WASM Execution
	wasmPath := filepath.Join(domainDir, "app.wasm")
	if _, err := os.Stat(wasmPath); err == nil {
		log.Printf("⚡ Executing Serverless WASM function for domain: %s", domain)
		urlStr := "http://" + domain + "/" + path
		method := req.Method
		if method == "" {
			method = "GET"
		}
		httpReq, _ := http.NewRequest(method, urlStr, bodyReader)
		if req.Headers != nil {
			for k, v := range req.Headers {
				for _, val := range v {
					httpReq.Header.Add(k, val)
				}
			}
		}
		
		// Securely inject the authenticated Peer ID
		if req.RemotePeer != "" {
			httpReq.Header.Set("X-Zypo-Peer-Id", req.RemotePeer)
		}
		httpReq.Header.Set("X-Zypo-Network", "true")

		respBytes, err := ExecuteWasm(n, wasmPath, httpReq)
		if err != nil {
			return node.ZypoHeader{}, nil, fmt.Errorf("wasm execution error: %v", err)
		}

		respReader := bufio.NewReader(bytes.NewReader(respBytes))
		resp, err := http.ReadResponse(respReader, httpReq)
		if err != nil {
			return node.ZypoHeader{Status: 200, Mime: "text/plain", Size: int64(len(respBytes))}, io.NopCloser(bytes.NewReader(respBytes)), nil
		}

		respHeaders := make(map[string][]string)
		for k, v := range resp.Header {
			respHeaders[k] = v
		}

		mimeType := resp.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "text/plain"
		}

		var bodyBytes []byte
		if resp.Body != nil {
			bodyBytes, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}

		return node.ZypoHeader{Status: resp.StatusCode, Mime: mimeType, Size: int64(len(bodyBytes)), Headers: respHeaders}, io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	// Check for Reverse Proxy mapping
	if targetURL, ok := getProxyTarget(sitesDir, domain); ok {
		fullURL := targetURL
		if !strings.HasSuffix(fullURL, "/") {
			fullURL += "/"
		}
		fullURL += path

		log.Printf("P2P Reverse Proxy: %s -> %s", domain, fullURL)

		method := req.Method
		if method == "" {
			method = "GET"
		}

		if req.Action == "websocket" {
			hostPath := strings.TrimPrefix(fullURL, "http://")
			hostPath = strings.TrimPrefix(hostPath, "https://")
			parts := strings.SplitN(hostPath, "/", 2)
			host := parts[0]
			path := "/"
			if len(parts) > 1 {
				path += parts[1]
			}
			if !strings.Contains(host, ":") {
				host += ":80"
			}

			conn, err := net.DialTimeout("tcp", host, 5*time.Second)
			if err != nil {
				return node.ZypoHeader{}, nil, fmt.Errorf("websocket dial error: %v", err)
			}

			reqLine := fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path)
			conn.Write([]byte(reqLine))
			if req.Headers != nil {
				for k, v := range req.Headers {
					for _, val := range v {
						conn.Write([]byte(fmt.Sprintf("%s: %s\r\n", k, val)))
					}
				}
			}
			conn.Write([]byte("\r\n"))

			reader := bufio.NewReader(conn)
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				conn.Close()
				return node.ZypoHeader{}, nil, fmt.Errorf("websocket handshake error: %v", err)
			}

			respHeaders := make(map[string][]string)
			for k, v := range resp.Header {
				respHeaders[k] = v
			}

			return node.ZypoHeader{Status: resp.StatusCode, Mime: "websocket", Size: 0, Headers: respHeaders}, &socketWrapper{
				r: io.MultiReader(reader, conn),
				w: conn,
				c: conn,
			}, nil
		}

		client := &http.Client{Timeout: 15 * time.Second}
		httpReq, err := http.NewRequest(method, fullURL, bodyReader)
		if err != nil {
			return node.ZypoHeader{}, nil, fmt.Errorf("failed to create request: %v", err)
		}

		if req.Headers != nil {
			for k, v := range req.Headers {
				for _, val := range v {
					httpReq.Header.Add(k, val)
				}
			}
		}

		// Securely inject the authenticated Peer ID
		if req.RemotePeer != "" {
			httpReq.Header.Set("X-Zypo-Peer-Id", req.RemotePeer)
		}
		httpReq.Header.Set("X-Zypo-Network", "true")

		resp, err := client.Do(httpReq)
		if err != nil {
			return node.ZypoHeader{}, nil, fmt.Errorf("backend error: %v", err)
		}

		mimeType := resp.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "text/html"
		}

		respHeaders := make(map[string][]string)
		for k, v := range resp.Header {
			respHeaders[k] = v
		}

		return node.ZypoHeader{Status: resp.StatusCode, Mime: mimeType, Size: resp.ContentLength, Headers: respHeaders}, resp.Body, nil
	}

	// File System Hosting
	cleanPath := filepath.Clean("/" + path)
	fullPath := filepath.Join(domainDir, cleanPath)
	if !strings.HasPrefix(fullPath, domainDir) {
		log.Printf("Hosting Error: Path traversal attempt blocked: %s", path)
		return node.ZypoHeader{Status: 403, Mime: "text/plain", Size: 9}, io.NopCloser(bytes.NewReader([]byte("Forbidden"))), nil
	}
	
	log.Printf("Hosting: Request for domain=%s path=%s -> fullPath=%s", domain, path, fullPath)

	// If path points to a directory, try adding index.html
	info, err := os.Stat(fullPath)
	if err == nil && info.IsDir() {
		fullPath = filepath.Join(fullPath, "index.html")
	}

	file, err := os.Open(fullPath)
	if err != nil {
		// One last try: if path doesn't have an extension, try adding .html
		if !strings.Contains(filepath.Base(fullPath), ".") {
			altPath := fullPath + ".html"
			file, err = os.Open(altPath)
			if err == nil {
				fullPath = altPath
			}
		}
	}

	if err != nil {
		log.Printf("Hosting Error: %v", err)
		return node.ZypoHeader{}, nil, fmt.Errorf("file not found: %s", fullPath)
	}

	stat, _ := file.Stat()
	log.Printf("Hosting Success: Serving %s (%d bytes)", fullPath, stat.Size())
	mimeType := mime.TypeByExtension(filepath.Ext(fullPath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return node.ZypoHeader{Status: 200, Mime: mimeType, Size: stat.Size()}, file, nil
}
