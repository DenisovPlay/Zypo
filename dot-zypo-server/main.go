package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dot-zypo/daemon/common/hosting"
	"github.com/dot-zypo/daemon/common/node"
)

func main() {
	configPath := flag.String("config", "zypo_config.json", "Path to config file")
	listenPort := flag.Int("port", 8901, "P2P listen port")
	bootstrapAddr := flag.String("bootstrap", "", "Extra bootstrap node")
	flag.Parse()

	cfg, err := node.LoadConfig(*configPath)
	if err != nil {
		cfg = node.DefaultConfig()
		cfg.IsServerNode = true
		node.SaveConfig(*configPath, cfg)
	}

	// Apply overrides
	cfg.IsServerNode = true
	if *listenPort != 0 {
		cfg.ListenPort = *listenPort
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n, err := node.NewNode(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to init node: %v", err)
	}
	defer n.Close()

	// Plug in hosting logic
	n.ResourceResolver = func(req *node.ZypoRequest, domain, path string, bodyReader io.Reader) (node.ZypoHeader, io.ReadCloser, error) {
		return hosting.GetResourceData(n, req, domain, path, bodyReader)
	}

	var extraPeers []string
	if *bootstrapAddr != "" {
		extraPeers = append(extraPeers, *bootstrapAddr)
	}

	if err := n.Start(extraPeers); err != nil {
		log.Fatalf("Failed to start: %v", err)
	}

	// Start VPN Provider (Server as an exit node) — TCP and UDP
	n.StartP2PVPNServer()
	n.StartP2PVPNUDPServer()

	// Enable bridge mode: server nodes with public IPs act as bridges for NAT'd clients
	if n.BridgeManager != nil {
		n.BridgeManager.Enable()
		log.Println("[Server] Bridge mode enabled — this node will relay traffic for NAT'd peers")
	}

	log.Println("=== ZYPO SERVER NODE IS RUNNING ===")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}
