package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// --- Per-peer stream rate limiter ---
// Prevents a single malicious peer from flooding us with streams.
var (
	peerStreamMu    sync.Mutex
	peerStreamCount = make(map[peer.ID]*peerRateEntry)
)

type peerRateEntry struct {
	count     int
	windowEnd time.Time
	bannedUntil time.Time
}

const (
	streamRateLimit  = 50             // max streams per window per peer
	streamRateWindow = 10 * time.Second
	streamBanDur     = 30 * time.Second
)

func checkStreamRateLimit(pid peer.ID) bool {
	peerStreamMu.Lock()
	defer peerStreamMu.Unlock()

	now := time.Now()
	entry, ok := peerStreamCount[pid]
	if !ok {
		entry = &peerRateEntry{}
		peerStreamCount[pid] = entry
	}

	// Still banned?
	if now.Before(entry.bannedUntil) {
		return false
	}

	// New window?
	if now.After(entry.windowEnd) {
		entry.count = 0
		entry.windowEnd = now.Add(streamRateWindow)
	}

	entry.count++
	if entry.count > streamRateLimit {
		entry.bannedUntil = now.Add(streamBanDur)
		log.Printf("[RateLimit] Peer %s exceeded stream limit (%d/10s), banned for %v",
			pid, entry.count, streamBanDur)
		return false
	}
	return true
}

type ZypoRequest struct {
	Action     string              `json:"action"`
	Resource   string              `json:"resource"`
	Method     string              `json:"method,omitempty"`
	Size       int64               `json:"size,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	RemotePeer string              `json:"-"` // Added for internal use
}

type ZypoHeader struct {
	Status  int                 `json:"status"`
	Mime    string              `json:"mime"`
	Size    int64               `json:"size"`
	Headers map[string][]string `json:"headers,omitempty"`
}

func (n *ZypoNode) handleZypoStream(s network.Stream) {
	defer s.Close()

	// Rate limit: reject streams from peers that are sending too many requests.
	remotePID := s.Conn().RemotePeer()
	if !checkStreamRateLimit(remotePID) {
		log.Printf("[P2P] Rate limit hit for peer %s, dropping stream", remotePID)
		return
	}

	reader := bufio.NewReader(s)

	// 1. Read Request Header
	s.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		if err != io.EOF {
			log.Printf("[P2P] Stream read error: %v", err)
		}
		return
	}

	var req ZypoRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		log.Printf("[P2P] Protocol error (invalid request): %v", err)
		return
	}
	req.RemotePeer = s.Conn().RemotePeer().String()

	// 2. Prepare Body Reader
	var bodyReader io.Reader = reader
	if req.Size > 0 {
		bodyReader = io.LimitReader(reader, req.Size)
	} else if req.Method == "GET" || req.Method == "HEAD" {
		bodyReader = bytes.NewReader(nil)
	} else {
		bodyReader = io.LimitReader(reader, 10*1024*1024)
	}

	log.Printf("[P2P] %s: %s %s (Size: %d)", s.Conn().RemotePeer().String()[:8], req.Action, req.Resource, req.Size)

	var header ZypoHeader
	var bodyStream io.ReadCloser

	// 3. Dispatch Action
	if req.Action == "fetch" || req.Action == "websocket" {
		parts := strings.SplitN(req.Resource, "/", 2)
		domain, path := parts[0], "index.html"
		if len(parts) > 1 && parts[1] != "" {
			path = parts[1]
		}
		if n.ResourceResolver != nil {
			header, bodyStream, err = n.ResourceResolver(&req, domain, path, bodyReader)
		} else {
			header = ZypoHeader{Status: 501}
			err = fmt.Errorf("no resolver")
		}
		if err != nil {
			header = ZypoHeader{Status: 404}
		}
	} else if req.Action == "ping" {
		header = ZypoHeader{Status: 200}
	} else if req.Action == "vpn_probe" {
		if n.cfg.EnableVpn {
			ann := &VPNAnnouncement{
				PeerID:    n.Host.ID().String(),
				Location:  n.cfg.VpnLocation,
				Flag:      n.cfg.VpnFlag,
				Price:     n.cfg.VpnPrice,
				Bandwidth: n.cfg.VpnBandwidth,
				Timestamp: time.Now().Unix(),
			}
			if n.cfg.IsCommandCenter {
				if ann.Location == "Decentralized Node" || ann.Location == "" {
					ann.Location = "Zypo Command Center"
				}
				if ann.Flag == "🌐" || ann.Flag == "" {
					ann.Flag = "🏛️"
				}
				if ann.Price == 0.5 {
					ann.Price = 0.1
				}
				if ann.Bandwidth == 100 {
					ann.Bandwidth = 5000
				}
			}
			data, _ := json.Marshal(ann)
			bodyStream = io.NopCloser(bytes.NewReader(data))
			header = ZypoHeader{Status: 200, Mime: "application/json", Size: int64(len(data))}
		} else {
			header = ZypoHeader{Status: 404}
		}
	} else if req.Action == "economy_tx" {
		// Read tx from stream
		txData, _ := io.ReadAll(bodyReader)
		var tx Transaction
		if err := json.Unmarshal(txData, &tx); err == nil {
			isNew, err := n.EconomyManager.ProcessTransaction(&tx)
			if err == nil {
				header = ZypoHeader{Status: 200}
				if isNew {
					go n.EconomyManager.BroadcastTransaction(&tx)
				}
			} else {
				log.Printf("[Economy] Tx rejected: %v", err)
				header = ZypoHeader{Status: 400}
			}
		} else {
			header = ZypoHeader{Status: 400}
		}
	} else if strings.HasPrefix(req.Action, "economy_") {
		if req.Action == "economy_balance" {
			balance := n.EconomyManager.GetBalance(s.Conn().RemotePeer().String())
			bodyStream = io.NopCloser(bytes.NewReader([]byte(fmt.Sprintf(`{"balance": %f}`, balance))))
			header = ZypoHeader{Status: 200, Mime: "application/json"}
		} else if req.Action == "economy_dump" && n.cfg.IsCommandCenter {
			// SECURITY: Only allow the node itself to dump the ledger (for internal sync).
			// External peers must not be able to retrieve the full financial ledger.
			if s.Conn().RemotePeer() != n.Host.ID() {
				log.Printf("[Economy] Rejected economy_dump from external peer %s", s.Conn().RemotePeer())
				header = ZypoHeader{Status: 403}
			} else {
				// Oracle command to dump the entire economy state (internal use only)
				dump := n.EconomyManager.DumpState()
				bodyStream = io.NopCloser(bytes.NewReader(dump))
				header = ZypoHeader{Status: 200, Mime: "application/json", Size: int64(len(dump))}
			}
		} else {
			header = ZypoHeader{Status: 403}
		}
	} else if req.Action == "domain_request" {
		if n.cfg.IsCommandCenter {
			cmd := "submit_domain"
			header, bodyStream, err = n.ResourceResolver(&req, "api.zypo", cmd, bodyReader)
		} else {
			header = ZypoHeader{Status: 403}
		}
	} else if req.Action == "cc_gossip" {
		// Gossip: respond with known CC multiaddrs so peers can discover the CC.
		// All nodes participate — even non-CC nodes share what they know.
		n.bootstrapMu.Lock()
		var ccAddrs []string
		for _, bid := range n.BootstrapIDs {
			addrs := n.Host.Peerstore().Addrs(bid)
			for _, a := range addrs {
				ccAddrs = append(ccAddrs, fmt.Sprintf("%s/p2p/%s", a.String(), bid.String()))
			}
		}
		n.bootstrapMu.Unlock()
		data, _ := json.Marshal(ccAddrs)
		bodyStream = io.NopCloser(bytes.NewReader(data))
		header = ZypoHeader{Status: 200, Mime: "application/json", Size: int64(len(data))}
	} else {
		header = ZypoHeader{Status: 400}
	}

	// Ensure request body is fully drained before sending response to keep stream in sync
	if req.Size != 0 && req.Method != "GET" && req.Method != "HEAD" {
		// Set a read deadline for draining to prevent hanging if the client lied about Size or it's chunked
		s.SetReadDeadline(time.Now().Add(5 * time.Second))
		io.Copy(io.Discard, bodyReader)
	}

	// 4. Send Response Header
	s.SetWriteDeadline(time.Now().Add(10 * time.Second))
	hBytes, _ := json.Marshal(header)
	s.Write(append(hBytes, '\n'))

	// 5. Send Response Body
	if bodyStream != nil {
		if req.Action == "websocket" {
			if rw, ok := bodyStream.(io.ReadWriteCloser); ok {
				go func() { io.Copy(rw, s); rw.Close() }()
				io.Copy(s, rw)
			}
			bodyStream.Close()
			return // Websocket takes over the stream completely
		} else {
			// Clear deadline for potentially large file transfers
			s.SetWriteDeadline(time.Time{})
			if header.Mime == "application/json" {
				data, _ := io.ReadAll(bodyStream)
				s.Write(append(data, '\n'))
			} else {
				io.Copy(s, bodyStream)
			}
			bodyStream.Close()
		}
	}
}

func (n *ZypoNode) ProxyRequest(target peer.ID, resource string, w http.ResponseWriter, r *http.Request) error {
	if target == n.Host.ID() {
		parts := strings.SplitN(resource, "/", 2)
		domain, path := parts[0], "index.html"
		if len(parts) > 1 && parts[1] != "" {
			path = parts[1]
		}
		zreq := &ZypoRequest{Action: "fetch", Resource: resource, Method: r.Method, Headers: r.Header, Size: r.ContentLength}
		header, bodyStream, err := n.ResourceResolver(zreq, domain, path, r.Body)
		if err != nil {
			return err
		}
		if bodyStream != nil {
			defer bodyStream.Close()
		}
		w.Header().Set("Content-Type", header.Mime)
		for k, v := range header.Headers {
			for _, val := range v {
				w.Header().Add(k, val)
			}
		}
		w.WriteHeader(header.Status)
		if bodyStream != nil {
			io.Copy(w, bodyStream)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(n.GetContext(), 30*time.Second)
	defer cancel()

	s, err := n.Host.NewStream(ctx, target, ZypoProtocolID)
	if err != nil {
		log.Printf("[P2P] Direct dial failed to %s: %v. Attempting via Circuit Relay...", target, err)

		// Attempt routing via active public peers (like CC) using /p2p-circuit
		var relayedStream network.Stream
		var relayErr error

		for _, p := range n.Host.Network().Peers() {
			relayAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", p.String(), target.String()))
			if err == nil {
				// Add to peerstore to tell libp2p how to route it
				n.Host.Peerstore().AddAddr(target, relayAddr, time.Minute)

				// Try dialing again using the new circuit route
				relayedStream, relayErr = n.Host.NewStream(ctx, target, ZypoProtocolID)
				if relayErr == nil {
					log.Printf("[P2P] Successfully established relayed connection to %s via %s", target, p)
					s = relayedStream
					err = nil
					break
				}
			}
		}

		if err != nil {
			return fmt.Errorf("failed direct and relayed dial to %s: %w", target, relayErr)
		}
	}
	defer s.Close()

	action := "fetch"
	if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
		action = "websocket"
	}

	req := ZypoRequest{
		Action:   action,
		Resource: resource,
		Method:   r.Method,
		Headers:  r.Header,
		Size:     r.ContentLength,
	}

	s.SetWriteDeadline(time.Now().Add(10 * time.Second))
	reqBytes, _ := json.Marshal(req)
	if _, err := s.Write(append(reqBytes, '\n')); err != nil {
		return err
	}

	if r.Method != "GET" && r.Method != "HEAD" && r.ContentLength != 0 {
		s.SetWriteDeadline(time.Now().Add(1 * time.Minute))
		if _, err := io.Copy(s, r.Body); err != nil {
			return err
		}
	}

	reader := bufio.NewReader(s)
	s.SetReadDeadline(time.Now().Add(30 * time.Second))
	hLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}

	var header ZypoHeader
	if err := json.Unmarshal([]byte(hLine), &header); err != nil {
		return err
	}

	w.Header().Set("Content-Type", header.Mime)
	for k, v := range header.Headers {
		for _, val := range v {
			w.Header().Add(k, val)
		}
	}
	w.WriteHeader(header.Status)

	if action == "websocket" {
		io.Copy(w, reader)
		return nil
	}

	// Clear read deadline for large file transfers
	s.SetReadDeadline(time.Time{})

	if header.Size > 0 {
		lr := io.LimitReader(reader, header.Size)
		io.Copy(w, lr)
	} else if header.Mime == "application/json" {
		line, _ := reader.ReadString('\n')
		w.Write([]byte(line))
	} else {
		io.Copy(w, reader)
	}

	return nil
}
