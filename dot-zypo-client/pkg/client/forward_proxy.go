package client

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"

	"github.com/dot-zypo/daemon/common/node"
)

func StartForwardProxy(n *node.ZypoNode, port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleForwardProxyRequest(n, w, r)
	})
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("🚀 Local Forward Proxy (for Chrome/Firefox) started on http://%s", addr)
	http.ListenAndServe(addr, mux)
}

func handleForwardProxyRequest(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	if r.Method == "CONNECT" {
		http.Error(w, "HTTPS (CONNECT) is not supported over Zypo Proxy yet. Please use http://", http.StatusNotImplemented)
		return
	}

	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}

	// Accept any domain that has a TLD (dot present). The Zypo network supports
	// multiple zones: .zypo, .mesh, .rus, etc. Blocking on TLD here is wrong —
	// let ResolveDomain return an error if the domain isn't in the network.
	if !strings.Contains(host, ".") {
		http.Error(w, "Zypo Forward Proxy: invalid domain (no TLD)", http.StatusBadGateway)
		return
	}

	pids, err := n.ResolveDomain(host)
	if err != nil {
		http.Error(w, "Domain not found in Zypo Network: "+err.Error(), http.StatusNotFound)
		return
	}

	// Anycast: pick random host
	peerID := pids[rand.Intn(len(pids))]

	resourcePath := host + r.URL.Path
	if r.URL.RawQuery != "" {
		resourcePath += "?" + r.URL.RawQuery
	}

	err = n.ProxyRequest(peerID, resourcePath, w, r)
	if err != nil {
		log.Printf("Forward Proxy error: %v", err)
	}
}
