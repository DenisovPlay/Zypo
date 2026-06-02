package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dot-zypo/daemon/common/hosting"
	client "github.com/dot-zypo/daemon/client/pkg/client"
	"github.com/dot-zypo/daemon/common/node"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	fmt.Fprintln(os.Stdout, "ZYPO_DAEMON_BOOTING")

	configPath := flag.String("config", "zypo_config.json", "Path to config file")
	rpcPort := flag.Int("rpc", 8902, "Local RPC port")
	rpcToken := flag.String("rpc-token", "", "RPC Auth Token")
	listenPort := flag.Int("port", 0, "P2P listen port (0 for random)")
	bootstrapAddr := flag.String("bootstrap", "", "Extra bootstrap node")
	proxyPort := flag.Int("proxy", 8903, "Forward proxy port")
	sigURLFlag := flag.String("vpn-sig", "", "VPN signaling server URL (defaults to bootstrap IP)")
	flag.Parse()

	cfg, err := node.LoadConfig(*configPath)
	if err != nil {
		cfg = node.DefaultConfig()
		node.SaveConfig(*configPath, cfg)
	}

	// Apply command line overrides
	if *rpcToken != "" {
		cfg.RpcToken = *rpcToken
	}
	if *listenPort != 0 {
		cfg.ListenPort = *listenPort
	} else if cfg.ListenPort == 0 {
		cfg.ListenPort = 0 // random
	}

	// Start RPC server IMMEDIATELLY with nil node
	go client.StartClientRPC(*rpcPort, cfg.RpcToken)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Fprintln(os.Stdout, "INIT_P2P_NODE")
	nReady, err := node.NewNode(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL_INIT_ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "NODE_PEER_ID %s\n", nReady.Host.ID().String())
	client.SetRPCNode(nReady) // Update the global for RPC handlers
	defer nReady.Close()

	// Plug in hosting logic (for local site previews or future usage)
	nReady.ResourceResolver = func(req *node.ZypoRequest, domain, path string, bodyReader io.Reader) (node.ZypoHeader, io.ReadCloser, error) {
		return hosting.GetResourceData(nReady, req, domain, path, bodyReader)
	}

	// Start Forward Proxy
	go client.StartForwardProxy(nReady, *proxyPort)

	var extraPeers []string
	if *bootstrapAddr != "" {
		extraPeers = append(extraPeers, *bootstrapAddr)
	}

	// Determine initial Signaling URL (fallback)
	sigURL := *sigURLFlag
	if sigURL == "" {
		sigURL = "ws://213.171.27.234:8905/ws" // Default fallback to relay
	}

	fmt.Fprintln(os.Stdout, "STARTING_MESH_STACK")
	if err := nReady.Start(extraPeers); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL_START_ERROR: %v\n", err)
		os.Exit(1)
	}

	log.Println("=== ZYPO CLIENT NODE IS RUNNING ===")

	// Start Pure P2P VPN components
	vpn := client.NewP2PVPNClient(nReady)

	// Determine IPs to exclude from TUN (Bootstrap nodes)
	excludedIPs := []string{"213.171.27.234"} // Primary relay fallback
	allBootstrap := append(cfg.BootstrapNodes, extraPeers...)
	for _, b := range allBootstrap {
		ma, err := multiaddr.NewMultiaddr(b)
		if err == nil {
			val, err := ma.ValueForProtocol(multiaddr.P_IP4)
			if err == nil {
				excludedIPs = append(excludedIPs, val)
			}
		}
	}

	// TUN requires root privileges on most systems
	tunName := "zypo-tun0"
	if os.Getenv("OS") == "darwin" || true { // macOS preference
		tunName = "utun10"
	}
	tm, err := client.StartTUN(tunName, vpn, excludedIPs)
	if err != nil {
		log.Printf("[TUN] Failed to start (ignore if not root): %v", err)
	}

	// Start SOCKS5 Server for non-root apps and better reliability
	_, err = client.StartSOCKS5(8910, vpn)
	if err != nil {
		log.Printf("[SOCKS5] Failed to start: %v", err)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	sig := <-ch
	log.Printf("Received signal %v, exiting...", sig)

	if tm != nil {
		tm.Cleanup()
	}
}
