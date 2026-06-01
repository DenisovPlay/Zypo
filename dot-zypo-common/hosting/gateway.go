package hosting

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"

	"github.com/dot-zypo/daemon/common/node"
)

func StartGateway(n *node.ZypoNode, port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleGatewayRequest(n, w, r)
	})
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("🌍 Public Gateway started on http://%s", addr)
	http.ListenAndServe(addr, mux)
}

func handleGatewayRequest(n *node.ZypoNode, w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}

	parts := strings.Split(host, ".")
	// Example: site.rus.zypo.network -> site.rus
	// If the gateway is running on zypo.network, we need at least 3 parts to have a Zypo domain.
	if len(parts) < 3 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<h1>Zypo Network Gateway</h1><p>Access a P2P site by prefixing its domain, for example: http://mydomain.zypo.network or http://ya.rus.zypo.network</p>"))
		return
	}

	// Reconstruct the domain from all parts except the last two (the gateway domain itself, e.g. zypo.network)
	zypoDomain := strings.Join(parts[:len(parts)-2], ".")

	pids, err := n.ResolveDomain(zypoDomain)
	if err != nil {
		http.Error(w, "Domain not found in Zypo Network", http.StatusNotFound)
		return
	}

	// Anycast: pick random host
	peerID := pids[rand.Intn(len(pids))]

	resourcePath := zypoDomain + r.URL.Path
	if r.URL.RawQuery != "" {
		resourcePath += "?" + r.URL.RawQuery
	}

	err = n.ProxyRequest(peerID, resourcePath, w, r)
	if err != nil {
		log.Printf("Gateway Proxy error: %v", err)
	}
}
