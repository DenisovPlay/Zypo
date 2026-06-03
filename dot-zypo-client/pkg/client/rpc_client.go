package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dot-zypo/daemon/common/node"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	rpcRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "zypo_rpc_requests_total",
		Help: "The total number of RPC requests",
	}, []string{"endpoint"})
)

type RPCServer struct {
	nodeGetter func() *node.ZypoNode
	token      string
}

func StartClientRPC(port int, token string, dataDir string, getter func() *node.ZypoNode) {
	srv := &RPCServer{nodeGetter: getter, token: token}
	mux := http.NewServeMux()

	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			rpcRequestsTotal.WithLabelValues(r.URL.Path).Inc()
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

			if r.Method == "OPTIONS" {
				return
			}
			if r.Header.Get("Authorization") != token {
				http.Error(w, "Unauthorized", 401)
				return
			}
			next.ServeHTTP(w, r)
		}
	}

	mux.HandleFunc("/rpc", auth(func(w http.ResponseWriter, r *http.Request) { handleRpcProxy(srv.nodeGetter(), w, r) }))
	mux.HandleFunc("/rpc/status", func(w http.ResponseWriter, r *http.Request) { handleRpcStatus(srv.nodeGetter(), w, r) })
	mux.HandleFunc("/rpc/account", auth(func(w http.ResponseWriter, r *http.Request) { handleRpcAccount(srv.nodeGetter(), w, r) }))
	mux.HandleFunc("/rpc/account/transfer", auth(func(w http.ResponseWriter, r *http.Request) { handleRpcAccountTransfer(srv.nodeGetter(), w, r) }))
	mux.HandleFunc("/rpc/network/reconnect", auth(func(w http.ResponseWriter, r *http.Request) {
		n := srv.nodeGetter()
		if n != nil {
			go n.BootstrapNetwork()
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}))
	mux.HandleFunc("/rpc/dns/override", auth(func(w http.ResponseWriter, r *http.Request) {
		handleRpcDnsOverride(srv.nodeGetter(), w, r)
	}))
	mux.HandleFunc("/rpc/vpn/list_nodes", auth(func(w http.ResponseWriter, r *http.Request) {
		handleRpcVPNListNodes(srv.nodeGetter(), w, r)
	}))
	mux.HandleFunc("/rpc/vpn/connect", auth(func(w http.ResponseWriter, r *http.Request) {
		handleRpcVPNConnect(srv.nodeGetter(), w, r)
	}))
	mux.HandleFunc("/rpc/vpn/status", auth(func(w http.ResponseWriter, r *http.Request) {
		handleRpcVPNStatus(srv.nodeGetter(), w, r)
	}))
	mux.HandleFunc("/rpc/vpn/disconnect", auth(func(w http.ResponseWriter, r *http.Request) {
		handleRpcVPNStop(srv.nodeGetter(), w, r)
	}))
	mux.HandleFunc("/rpc/vpn/config", auth(func(w http.ResponseWriter, r *http.Request) {
		handleRpcVPNConfig(srv.nodeGetter(), w, r)
	}))
	mux.HandleFunc("/rpc/vpn/register_node", auth(func(w http.ResponseWriter, r *http.Request) {
		n := srv.nodeGetter()
		if n == nil {
			http.Error(w, "Node initializing", 503)
			return
		}
		var ann node.VPNAnnouncement
		if err := json.NewDecoder(r.Body).Decode(&ann); err != nil {
			http.Error(w, "Invalid body", 400)
			return
		}
		n.RegisterVPNNodeDHT(&ann)
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}))
	mux.HandleFunc("/rpc/vpn/settle_ticket", auth(func(w http.ResponseWriter, r *http.Request) {
		n := srv.nodeGetter()
		if n == nil {
			http.Error(w, "Node initializing", 503)
			return
		}
		ticketBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", 400)
			return
		}
		if err := n.EconomyManager.SettleVPNTicket(ticketBytes); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}))
	mux.HandleFunc("/rpc/bridge/status", auth(func(w http.ResponseWriter, r *http.Request) {
		n := srv.nodeGetter()
		if n == nil || n.BridgeManager == nil {
			http.Error(w, "Bridge Manager not initialized", 503)
			return
		}
		json.NewEncoder(w).Encode(n.BridgeManager.Status())
	}))

	// Enterprise Metrics
	mux.Handle("/metrics", promhttp.Handler())

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL_RPC_ERROR: %v\n", err)
		os.Exit(1)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port
	os.MkdirAll(dataDir, 0755)
	portFile := filepath.Join(dataDir, ".rpc_port")
	os.WriteFile(portFile, []byte(fmt.Sprintf("%d", actualPort)), 0644)

	fmt.Fprintf(os.Stdout, "RPC_SERVER_STARTING http://127.0.0.1:%d\n", actualPort)

	err = http.Serve(listener, mux)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL_RPC_ERROR: %v\n", err)
		os.Exit(1)
	}
}

func handleRpcProxy(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "missing url", 400)
		return
	}
	domain := strings.Split(strings.TrimPrefix(url, "zypo://"), "/")[0]

	pids, err := n.ResolveDomain(domain)
	if err != nil {
		log.Printf("[RPC] Domain resolution failed for %s: %v", domain, err)
		http.Error(w, "Domain not found in DHT", 404)
		return
	}

	// Try multiple peers if needed
	var lastErr error
	for i := 0; i < len(pids) && i < 3; i++ {
		// Pick a peer (cycle through them)
		pid := pids[i]

		err = n.ProxyRequest(pid, strings.TrimPrefix(url, "zypo://"), w, r)
		if err == nil {
			return // Success!
		}

		log.Printf("[RPC] Proxy attempt %d failed for %s (peer %s): %v", i+1, domain, pid, err)
		lastErr = err
	}

	http.Error(w, fmt.Sprintf("Peer error: %v", lastErr), 502)
}

func getCCStream(ctx context.Context, n *node.ZypoNode) (network.Stream, error) {
	// 1. Identify all candidate peers, prioritizing BootstrapIDs (Command Centers)
	seen := make(map[peer.ID]bool)
	var targets []peer.ID

	// Prioritize healthy verified bootstrap IDs
	for _, bid := range n.BootstrapIDs {
		if !seen[bid] && n.IsPeerHealthy(bid) {
			// Check if we are actually connected
			for _, p := range n.Host.Network().Peers() {
				if p == bid {
					seen[bid] = true
					targets = append(targets, bid)
					break
				}
			}
		}
	}

	// Try any other bootstrap node even if not marked healthy yet
	for _, bid := range n.BootstrapIDs {
		if !seen[bid] {
			for _, p := range n.Host.Network().Peers() {
				if p == bid {
					seen[bid] = true
					targets = append(targets, bid)
					break
				}
			}
		}
	}

	// Fallback to any connected peer (less likely to be a CC, but maybe)
	for _, p := range n.Host.Network().Peers() {
		if !seen[p] {
			seen[p] = true
			targets = append(targets, p)
		}
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no connected peers")
	}

	// 2. Try to open a stream to the best candidate
	var lastErr error
	for _, target := range targets {
		s, err := n.Host.NewStream(ctx, target, node.ZypoProtocolID)
		if err == nil {
			return s, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("failed to open economy stream: %w", lastErr)
}

func handleRpcStatus(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if n == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"peer_id":        "initializing...",
			"peers":          0,
			"balance":        0,
			"economy_status": "initializing",
			"cc_connected":   false,
			"topology":       []interface{}{},
		})
		return
	}

	peers := n.Host.Network().Peers()
	ccConnected := n.IsCCConnected()

	status := "offline"
	if ccConnected {
		status = "synced"
	} else if len(peers) > 0 {
		status = "mesh_only"
	}

	balance := n.EconomyManager.GetBalance(n.Host.ID().String())

	// Build topology map for UI
	type PeerInfo struct {
		ID          string   `json:"id"`
		Addrs       []string `json:"addrs"`
		IsRelayed   bool     `json:"is_relayed"`
		IsBootstrap bool     `json:"is_bootstrap"`
		Transports  []string `json:"transports"`
	}
	topology := make([]PeerInfo, 0, len(peers))
	
	transportCounts := map[string]int{"tcp": 0, "quic": 0, "ws": 0, "relay": 0}
	
	for _, p := range peers {
		conns := n.Host.Network().ConnsToPeer(p)
		isRelayed := false
		addrs := make([]string, 0)
		transports := make([]string, 0)
		
		for _, c := range conns {
			addr := c.RemoteMultiaddr().String()
			addrs = append(addrs, addr)
			if strings.Contains(addr, "/p2p-circuit") {
				isRelayed = true
				transportCounts["relay"]++
				transports = append(transports, "relay")
			} else if strings.Contains(addr, "/ws") {
				transportCounts["ws"]++
				transports = append(transports, "ws")
			} else if strings.Contains(addr, "/quic") {
				transportCounts["quic"]++
				transports = append(transports, "quic")
			} else if strings.Contains(addr, "/tcp") {
				transportCounts["tcp"]++
				transports = append(transports, "tcp")
			}
		}

		isBootstrap := n.IsBootstrap(p)

		topology = append(topology, PeerInfo{
			ID:          p.String(),
			Addrs:       addrs,
			IsRelayed:   isRelayed,
			IsBootstrap: isBootstrap,
			Transports:  transports,
		})
	}

	vpnStatus := interface{}(nil)
	if GlobalP2PVPNClient != nil {
		vpnStatus = map[string]interface{}{
			"is_operational": GlobalP2PVPNClient.providerID != "",
		}
	}
	
	bridgeActive := n.BridgeManager != nil && n.BridgeManager.IsEnabled()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"peer_id":        n.Host.ID().String(),
		"peers":          len(peers),
		"balance":        balance,
		"economy_status": status,
		"cc_connected":   ccConnected,
		"vpn":            vpnStatus,
		"topology":       topology,
		"transport_stats": transportCounts,
		"diagnostics": map[string]interface{}{
			"dht_healthy": len(peers) > 0,
			"cc_alive": ccConnected,
			"bridge_active": bridgeActive,
			"vpn_active": GlobalP2PVPNClient != nil && GlobalP2PVPNClient.providerID != "",
			"tun_available": GlobalTUN != nil,
		},
	})
}

func handleRpcAccount(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	acc := n.EconomyManager.GetAccount(n.Host.ID().String())
	if acc != nil {
		json.NewEncoder(w).Encode(acc)
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{"balance": 100, "history": []interface{}{}, "rating": 5.0})
	}
}

func handleRpcAccountTransfer(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	var treq struct {
		To      string  `json:"to"`
		Amount  float64 `json:"amount"`
		Comment string  `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&treq); err != nil {
		http.Error(w, "Invalid body", 400)
		return
	}

	tx, err := n.EconomyManager.CreateAndSendTransaction(treq.To, treq.Amount, treq.Comment)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"status": 200, "success": true, "tx_hash": tx.ID})
}

func handleRpcSubmitDomainRequest(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	r.ParseForm()
	ctx, cancel := context.WithTimeout(n.GetContext(), 5*time.Second)
	s, err := getCCStream(ctx, n) // Domain requests also go to CC
	cancel()
	if err != nil {
		http.Error(w, "Command Center offline", 502)
		return
	}
	defer s.Close()

	json.NewEncoder(s).Encode(node.ZypoRequest{Action: "domain_request"})
	json.NewEncoder(s).Encode(map[string]string{"domain": r.FormValue("domain"), "peer_id": n.Host.ID().String()})
	w.WriteHeader(200)
}

func handleRpcVPNListNodes(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	// Use DHT exclusively for decentralized discovery
	log.Println("[RPC] Discovering VPN nodes via DHT...")
	dhtNodes, err := n.DiscoverVPNNodes()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(dhtNodes)
}

func handleRpcVPNConnect(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	var req struct {
		ProviderID string `json:"provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", 400)
		return
	}

	if GlobalP2PVPNClient == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "VPN Client not initialized"})
		return
	}

	log.Printf("[RPC] Initiating P2P VPN connection to: %s", req.ProviderID)
	err := GlobalP2PVPNClient.ConnectToProvider(req.ProviderID)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleRpcDnsOverride(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	var req struct {
		Domain string `json:"domain"`
		PeerID string `json:"peer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", 400)
		return
	}

	pid, err := peer.Decode(req.PeerID)
	if err != nil {
		http.Error(w, "Invalid peer ID", 400)
		return
	}

	n.AddLocalDNSOverride(req.Domain, pid)
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleRpcVPNStatus(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if GlobalP2PVPNClient == nil {
		http.Error(w, "VPN Client not initialized", 500)
		return
	}
	// P2P doesn't have ICE state, just connected or not
	status := map[string]interface{}{
		"connection_state": "disconnected",
		"is_operational":   false,
		"bytes_sent":       0,
		"bytes_received":   0,
		"tun_available":    GlobalTUN != nil,
	}
	if GlobalP2PVPNClient.providerID != "" {
		status["connection_state"] = "connected"
		status["is_operational"] = true
		status["provider_id"] = GlobalP2PVPNClient.providerID
		if GlobalTUN != nil {
			status["bytes_sent"] = atomic.LoadUint64(&GlobalTUN.BytesSent)
			status["bytes_received"] = atomic.LoadUint64(&GlobalTUN.BytesReceived)
		}
	}
	json.NewEncoder(w).Encode(status)
}

func handleRpcVPNStop(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if GlobalP2PVPNClient == nil {
		http.Error(w, "VPN Client not initialized", 500)
		return
	}
	GlobalP2PVPNClient.Disconnect()
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleRpcVPNConfig(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if n == nil {
		http.Error(w, "Node initializing", 503)
		return
	}
	var req struct {
		Location  string  `json:"location"`
		Flag      string  `json:"flag"`
		Price     float64 `json:"price"`
		Bandwidth int     `json:"bandwidth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", 400)
		return
	}

	n.UpdateVPNConfig(req.Location, req.Flag, req.Price, req.Bandwidth)
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
