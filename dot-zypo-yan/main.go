package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dot-zypo/daemon/common/hosting"
	"github.com/dot-zypo/daemon/common/node"
)

type ZypoYanConfig struct {
	node.Config
	SitesDir string `json:"sites_dir"`
}

func switchLayout(s string) string {
	ru := []rune("йцукенгшщзхъфывапролджэячсмитьбю.ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЯЧСМИТЬБЮ,")
	en := []rune("qwertyuiop[]asdfghjkl;'zxcvbnm,./QWERTYUIOP{}ASDFGHJKL:\"ZXCVBNM<>?")

	ruMap := make(map[rune]rune)
	enMap := make(map[rune]rune)
	for i, r := range ru {
		if i < len(en) {
			ruMap[r] = en[i]
			enMap[en[i]] = r
		}
	}

	var out []rune
	for _, r := range s {
		if val, ok := ruMap[r]; ok {
			out = append(out, val)
		} else if val, ok := enMap[r]; ok {
			out = append(out, val)
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

func main() {
	// Initialize Enterprise Structured Logging
	slogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(slogger)

	configPath := flag.String("config", "zypo_config.json", "Path to config file")
	listenPort := flag.Int("port", 9000, "P2P listen port")
	enableMdns := flag.Bool("mdns", false, "Enable mDNS discovery")
	bootstrapAddr := flag.String("bootstrap", "", "Extra bootstrap node")
	enableVpn := flag.Bool("vpn", false, "Enable VPN Server (Exit Node)")
	flag.Parse()

	cfg, err := node.LoadConfig(*configPath)
	if err != nil {
		cfg = node.DefaultConfig()
		cfg.ListenPort = 9000
		cfg.KeyFile = "data/yan_key.bin"
		cfg.DataDir = "data"
		cfg.EnableMdns = false
		node.SaveConfig(*configPath, cfg)
	}

	// Apply overrides
	if *listenPort != 9000 {
		cfg.ListenPort = *listenPort
	}
	if *enableMdns {
		cfg.EnableMdns = *enableMdns
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := node.NewNode(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to init Ya node: %v", err)
	}
	defer n.Close()

	// Start VPN if requested via flag OR config
	if *enableVpn || cfg.EnableVpn {
		n.StartP2PVPNServer()
		n.StartP2PVPNUDPServer()
		log.Printf("[P2P VPN] Service started on Yan Node (TCP+UDP)")
		
		if n.BridgeManager != nil {
			n.BridgeManager.Enable()
			log.Println("[Bridge] Enabled on Yan Node for NAT'd peers")
		}
		
		go func() {
			time.Sleep(10 * time.Second)
			n.AnnounceVPNPresence()
		}()
	}

	// Setup Generic Resource Resolver
	n.ResourceResolver = func(req *node.ZypoRequest, domain, path string, bodyReader io.Reader) (node.ZypoHeader, io.ReadCloser, error) {
		// 1. Critical Check: Is this a domain we actually serve?
		// We NEVER serve official .zypo domains from this node
		if strings.HasSuffix(domain, ".zypo") && domain != "ya.zypo" {
			return node.ZypoHeader{Status: 404}, nil, nil
		}

		// 2. Handle API requests (Path-based)
		isAPI := strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "api/")
		if isAPI {
			log.Printf("[YA Node] API call: %s", path)
			if strings.Contains(path, "api/search") {
				var queryStr string
				parts := strings.SplitN(path, "?", 2)
				if len(parts) == 2 {
					params := strings.Split(parts[1], "&")
					for _, p := range params {
						if strings.HasPrefix(p, "q=") {
							rawQ := strings.TrimPrefix(p, "q=")
							if unescaped, err := url.QueryUnescape(rawQ); err == nil {
								queryStr = unescaped
							} else {
								queryStr = rawQ
							}
							queryStr = strings.ToLower(queryStr)
							break
						}
					}
				}

				var results []map[string]string
				if db != nil && queryStr != "" {
					query := "%" + queryStr + "%"
					altQuery := "%" + switchLayout(queryStr) + "%"
					rows, err := db.Query(`
						SELECT url, title, description 
						FROM pages 
						WHERE title LIKE ? OR description LIKE ? OR url LIKE ? 
						   OR title LIKE ? OR description LIKE ? OR url LIKE ? 
						LIMIT 50
					`, query, query, query, altQuery, altQuery, altQuery)
					
					if err == nil {
						defer rows.Close()
						for rows.Next() {
							var u, t, d string
							if err := rows.Scan(&u, &t, &d); err == nil {
								results = append(results, map[string]string{
									"url":   u,
									"title": t,
									"desc":  d,
								})
							}
						}
					} else {
						log.Printf("[YA Node] Search DB error: %v", err)
					}
				}

				if len(results) == 0 {
					results = []map[string]string{}
				}

				respJSON, _ := json.Marshal(map[string]interface{}{
					"status":  "ok",
					"engine":  "ya-mesh-spider",
					"results": results,
				})

				return node.ZypoHeader{Status: 200, Mime: "application/json", Size: int64(len(respJSON))}, io.NopCloser(bytes.NewReader(respJSON)), nil
			}
			return node.ZypoHeader{Status: 404}, nil, fmt.Errorf("unknown api endpoint")
		}

		// 3. Security Check: Only serve domains that exist in our local sites dir
		domainDir := filepath.Join(cfg.SitesDir, domain)
		if _, err := os.Stat(domainDir); os.IsNotExist(err) {
			return node.ZypoHeader{Status: 404}, nil, nil
		}

		// 4. Handle Static Content
		cleanPath := path
		// Strip query parameters for static files
		if idx := strings.Index(cleanPath, "?"); idx != -1 {
			cleanPath = cleanPath[:idx]
		}
		if cleanPath == "" || cleanPath == "/" {
			cleanPath = "index.html"
		}

		log.Printf("[YA Node] Serving content for domain=%s path=%s", domain, cleanPath)
		return hosting.GetResourceData(n, req, domain, cleanPath, bodyReader)
	}

	var extraPeers []string
	if *bootstrapAddr != "" {
		extraPeers = append(extraPeers, *bootstrapAddr)
	}

	if err := n.Start(extraPeers); err != nil {
		log.Fatalf("Failed to start Ya node: %v", err)
	}

	log.Println("=== ZYPO YA SEARCH NODE IS RUNNING ===")

	// Auto-announce hosted domains.
	// Strategy:
	//   1. Register a local DNS override immediately so THIS node resolves its own
	//      domains without any network round-trip (no fallback storm).
	//   2. Send a domain_request to the Command Center over P2P so CC signs the
	//      record and publishes it to DHT — other nodes can then find us via DHT.
	go func() {
		time.Sleep(5 * time.Second)
		entries, _ := os.ReadDir(cfg.SitesDir)
		for _, e := range entries {
			if !e.IsDir() || !strings.Contains(e.Name(), ".") {
				continue
			}
			domain := e.Name()

			// 1. Local override — instant resolution on this node itself.
			n.AddLocalDNSOverride(domain, n.Host.ID())
			log.Printf("[YA Node] Local DNS override set: %s → self", domain)

			// 2. Ask CC to sign & publish to DHT so remote peers can find us.
			n.AnnounceDomain(domain)
		}
	}()

	// Start the P2P Spider Crawler
	StartCrawler(n)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}
