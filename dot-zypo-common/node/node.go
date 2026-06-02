package node

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	record "github.com/libp2p/go-libp2p-record"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	libp2ptcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	libp2pws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	libp2pwebtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	"github.com/multiformats/go-multiaddr"
)

const (
	ZypoProtocolID = "/zypo/transport/2.0.0"
	DiscoveryTag   = "dot-zypo-mesh"
	// CID representing the "Command Center Service" for global DHT rendezvous
	CC_RENDEZVOUS_CID = "bafkreigh2ak363shsr5u4u47bc3cc6ot76sqscvxv277dtkcycyu4m3y3e"
)

type ZypoNode struct {
	Host              host.Host
	DHT               *dht.IpfsDHT
	ctx               context.Context
	cfg               Config
	PrivKey           crypto.PrivKey
	validator         *ZypoValidator
	ResourceResolver  func(req *ZypoRequest, domain, path string, bodyReader io.Reader) (ZypoHeader, io.ReadCloser, error)
	EconomyManager    *EconomyManager
	BootstrapIDs      []peer.ID
	localDNSMu        sync.RWMutex
	localDNSOverrides map[string]LocalDNSOverride
	bootstrapMu       sync.Mutex
	bootstrapHealth   map[peer.ID]time.Time // last successful interaction
	dhtRefreshTrigger chan struct{}
	streamPoolMu      sync.Mutex
	streamPool        map[peer.ID][]network.Stream
}

func loadOrGenerateKey(path string) (crypto.PrivKey, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Println("Generating new Ed25519 key...")
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, err
		}
		bytes, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, err
		}
		err = os.WriteFile(path, bytes, 0600)
		return priv, err
	}

	log.Println("Loading existing key from disk...")
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(bytes)
}

func (n *ZypoNode) savePeers(peers []peer.AddrInfo) {
	os.MkdirAll(n.cfg.DataDir, 0755)
	type storedPeer struct {
		ID    string   `json:"id"`
		Addrs []string `json:"addrs"`
	}
	var stored []storedPeer
	for _, p := range peers {
		if len(p.Addrs) == 0 {
			continue
		}
		sp := storedPeer{ID: p.ID.String()}
		for _, a := range p.Addrs {
			sp.Addrs = append(sp.Addrs, a.String())
		}
		stored = append(stored, sp)
	}
	if len(stored) == 0 {
		return
	}
	b, _ := json.MarshalIndent(stored, "", "  ")
	tmpPath := filepath.Join(n.cfg.DataDir, "known_peers.json.tmp")
	os.WriteFile(tmpPath, b, 0644)
	os.Rename(tmpPath, filepath.Join(n.cfg.DataDir, "known_peers.json"))
}

func (n *ZypoNode) loadKnownPeers() []string {
	b, err := os.ReadFile(filepath.Join(n.cfg.DataDir, "known_peers.json"))
	if err != nil {
		return nil
	}
	type storedPeer struct {
		ID    string   `json:"id"`
		Addrs []string `json:"addrs"`
	}
	var stored []storedPeer
	if err := json.Unmarshal(b, &stored); err != nil {
		return nil
	}
	var addrs []string
	for _, sp := range stored {
		for _, a := range sp.Addrs {
			addrs = append(addrs, a+"/p2p/"+sp.ID)
		}
	}
	log.Printf("Loaded %d known peers from disk", len(stored))
	return addrs
}

func NewNode(ctx context.Context, cfg Config) (*ZypoNode, error) {
	priv, err := loadOrGenerateKey(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load/generate key: %w", err)
	}

	listenAddrs := []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.ListenPort),
		"/ip4/0.0.0.0/udp/0/quic-v1",
		fmt.Sprintf("/ip6/::/tcp/%d", cfg.ListenPort),
		"/ip6/::/udp/0/quic-v1",
	}

	var h host.Host
	var kDHT *dht.IpfsDHT

	zypoVal := &ZypoValidator{OraclePubKey: nil} // Populate later if not CC

	validator := record.NamespacedValidator{
		"zypo": zypoVal,
		"pk":   record.PublicKeyValidator{},
	}

	// Parse bootstrap nodes for static relays
	var staticRelays []peer.AddrInfo
	for _, b := range cfg.BootstrapNodes {
		ma, err := multiaddr.NewMultiaddr(b)
		if err == nil {
			pi, err := peer.AddrInfoFromP2pAddr(ma)
			if err == nil {
				staticRelays = append(staticRelays, *pi)
			}
		}
	}

	opts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(libp2pwebtransport.New),
		libp2p.Transport(libp2ptcp.NewTCPTransport),
		libp2p.Transport(libp2pws.New),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
		libp2p.EnableRelayService(),
		libp2p.Security(noise.ID, noise.New),
	}

	if len(staticRelays) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(staticRelays))
	} else {
		opts = append(opts, libp2p.EnableAutoRelayWithPeerSource(
			func(ctx context.Context, numPeers int) <-chan peer.AddrInfo {
				peerChan := make(chan peer.AddrInfo)
				go func() {
					defer close(peerChan)
					if kDHT == nil || h == nil {
						return
					}
					select {
					case <-time.After(15 * time.Second):
					case <-ctx.Done():
						return
					}
					ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					cid, _ := cid.Parse("bafkreigh2ak363shsr5u4u47bc3cc6ot76sqscvxv277dtkcycyu4m3y3e")
					providers, err := kDHT.FindProviders(ctx, cid)
					if err != nil {
						return
					}
					count := 0
					for _, p := range providers {
						if p.ID == h.ID() {
							continue
						}
						select {
						case peerChan <- p:
							count++
							if count >= numPeers {
								return
							}
						case <-ctx.Done():
							return
						}
					}
				}()
				return peerChan
			},
		))
	}

	if cfg.IsCommandCenter {
		opts = append(opts, libp2p.ForceReachabilityPublic())
	}

	h, err = libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create host: %w\nListenAddrs: %v", err, listenAddrs)
	}

	// Initialize DHT now that Host is ready
	// We use a custom ProtocolPrefix to bypass strict libp2p /ipfs validator checks
	// and to isolate the Zypo Mesh DHT from the public IPFS network.
	dhtMode := dht.ModeAuto
	if cfg.IsCommandCenter {
		dhtMode = dht.ModeServer
	}

	log.Printf("[Mesh] Initializing Kademlia DHT (Prefix: /zypo, Mode: %s)...", func() string {
		if cfg.IsCommandCenter {
			return "Server"
		}
		return "Auto"
	}())
	kDHT, err = dht.New(ctx, h,
		dht.Mode(dhtMode),
		dht.Validator(validator),
		dht.ProtocolPrefix("/zypo"),
		// Разрешаем приватные/локальные IP-адреса, чтобы работало в Docker, LAN и через VPN (ZeroTier)
		dht.RoutingTableFilter(func(d interface{}, p peer.ID) bool { return true }),
		dht.QueryFilter(func(d interface{}, p peer.AddrInfo) bool { return true }),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to init DHT: %w", err)
	}

	// Update config with actual bound port if it was 0
	actualCfg := cfg
	if cfg.ListenPort == 0 {
		for _, addr := range h.Addrs() {
			if p, err := addr.ValueForProtocol(multiaddr.P_TCP); err == nil {
				fmt.Sscanf(p, "%d", &actualCfg.ListenPort)
				break
			}
		}
	}

	node := &ZypoNode{
		Host:              h,
		DHT:               kDHT,
		ctx:               ctx,
		cfg:               actualCfg,
		PrivKey:           priv,
		validator:         zypoVal,
		localDNSOverrides: make(map[string]LocalDNSOverride),
		bootstrapHealth:   make(map[peer.ID]time.Time),
		dhtRefreshTrigger: make(chan struct{}, 1),
		streamPool:        make(map[peer.ID][]network.Stream),
	}
	node.EconomyManager = NewEconomyManager(node, cfg.DataDir)

	h.SetStreamHandler(ZypoProtocolID, node.handleZypoStream)

	os.MkdirAll(cfg.SitesDir, 0755)

	return node, nil
}

func (n *ZypoNode) announceRelayPresence() {
	// Wait for network to stabilize
	time.Sleep(30 * time.Second)

	relayCid, _ := cid.Parse("bafkreigh2ak363shsr5u4u47bc3cc6ot76sqscvxv277dtkcycyu4m3y3e")

	for {
		// Bullet-proof: Check if we have any public-looking address
		hasPublic := false
		for _, addr := range n.Host.Addrs() {
			if isPublicAddr(addr) {
				hasPublic = true
				break
			}
		}

		if hasPublic {
			ctx, cancel := context.WithTimeout(n.ctx, 1*time.Minute)
			log.Printf("[Mesh] Announcing our node as a Public Relay...")
			n.DHT.Provide(ctx, relayCid, true)
			cancel()
		}

		select {
		case <-n.ctx.Done():
			return
		case <-time.After(1 * time.Hour):
		}
	}
}

func isPublicAddr(ma multiaddr.Multiaddr) bool {
	ipStr, err := ma.ValueForProtocol(multiaddr.P_IP4)
	if err != nil {
		ipStr, err = ma.ValueForProtocol(multiaddr.P_IP6)
	}
	if err != nil {
		return false
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}

	if ip4 := ip.To4(); ip4 != nil {
		// RFC 1918
		switch {
		case ip4[0] == 10:
			return false
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return false
		case ip4[0] == 192 && ip4[1] == 168:
			return false
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // CGNAT
			return false
		case ip4[0] == 169 && ip4[1] == 254: // Link-local
			return false
		}
	} else {
		// IPv6 private ranges (ULA)
		if (ip[0] & 0xfe) == 0xfc {
			return false
		}
	}

	return true
}

func (n *ZypoNode) GetContext() context.Context {
	return n.ctx
}

func (n *ZypoNode) UpdateVPNConfig(location string, flag string, price float64, bandwidth int) {
	n.cfg.VpnLocation = location
	n.cfg.VpnFlag = flag
	n.cfg.VpnPrice = price
	n.cfg.VpnBandwidth = bandwidth
	log.Printf("[VPN] Config updated: %s %s %.2f ZPCN/GB %d Mbps", flag, location, price, bandwidth)
}

func (n *ZypoNode) GetSignalingURL() string {
	n.bootstrapMu.Lock()
	defer n.bootstrapMu.Unlock()

	// 1. Check verified healthy bootstraps first
	for _, bid := range n.BootstrapIDs {
		// Only use peers that are actually connected
		if n.Host.Network().Connectedness(bid) == network.Connected {
			addrs := n.Host.Peerstore().Addrs(bid)
			for _, a := range addrs {
				// Prefer public IPs if we have them, but take what we can get
				val, err := a.ValueForProtocol(multiaddr.P_IP4)
				if err == nil {
					// Check if it's a private IP. In a LAN environment, this is preferred.
					// In a global environment, it might be the only thing we have before NAT traversal.
					return fmt.Sprintf("ws://%s:8905/ws", val)
				}
			}
		}
	}
	return ""
}

func (n *ZypoNode) SetOraclePubKey(pub crypto.PubKey) {
	if n.validator != nil {
		n.validator.OraclePubKey = pub
		log.Printf("[Mesh] Oracle public key updated")
	}
}

func (n *ZypoNode) Start(extraPeers []string) error {
	log.Printf("=== Node ID: %s ===", n.Host.ID().String())
	log.Printf("=== Listening on: %v ===", n.Host.Addrs())

	n.loadDNSOverrides()
	n.loadOraclePubKey()

	if n.cfg.EnableMdns {
		log.Println("Network: Starting mDNS discovery...")
		mdnsService := mdns.NewMdnsService(n.Host, DiscoveryTag, &discoveryNotifee{h: n.Host, node: n})
		mdnsService.Start()
	}

	// Always run UDP broadcast scanner as fallback/supplement to mDNS
	log.Println("Network: Starting LAN UDP Scanner for isolated environments...")
	n.StartLANScanner()

	// Global Tor-like Hidden Service scanner for finding the CC without hardcoded IPs
	log.Println("Network: Starting Global DHT Scanner for Command Center discovery...")
	n.StartGlobalScanner()

	// Register as a Relay Provider in the network
	go n.announceRelayPresence()

	if err := n.DHT.Bootstrap(n.ctx); err != nil {
		return fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	allBootstrap := append([]string{}, n.cfg.BootstrapNodes...)
	allBootstrap = append(allBootstrap, extraPeers...)
	knownPeers := n.loadKnownPeers()
	allBootstrap = append(allBootstrap, knownPeers...)
	if len(n.cfg.DiscoveryURLs) > 0 {
		go func() {
			httpNodes := fetchBootstrapFromHTTP(n.cfg.DiscoveryURLs)
			for _, addr := range httpNodes {
				n.connectToBootstrap(addr)
			}
		}()
	}

	connectedRelays := make([]peer.AddrInfo, 0)
	connectedSet := make(map[string]bool)

	for _, addrStr := range allBootstrap {
		maddr, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil || addrInfo.ID == n.Host.ID() {
			continue
		}

		if connectedSet[addrInfo.ID.String()] {
			continue
		}
		connectedSet[addrInfo.ID.String()] = true

		if ai := n.connectToBootstrap(addrStr); ai != nil {
			connectedRelays = append(connectedRelays, *ai)
		}
	}

	if len(connectedRelays) > 0 && !n.cfg.IsCommandCenter {
		go func() {
			time.Sleep(3 * time.Second)
			n.updateStaticRelays(connectedRelays)
		}()
	}

	go n.peerPersistenceLoop()
	go n.routingTableRefreshLoop()
	go n.ensureBootstrapConnectivityLoop()
	go n.bootstrapLivenessLoop()

	return nil
}

func (n *ZypoNode) ensureBootstrapConnectivityLoop() {
	interval := 5 * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()

	zeroPeerBackoff := 2 * time.Second

	for {
		select {
		case <-timer.C:
			peerCount := len(n.Host.Network().Peers())
			if peerCount == 0 {
				log.Printf("Network: CRITICAL - 0 peers connected. Attempting aggressive re-bootstrap (interval=%v)...", zeroPeerBackoff)
				n.BootstrapNetwork()

				// Increase backoff for next attempt if we still have 0 peers, but cap it at 30s
				zeroPeerBackoff *= 2
				if zeroPeerBackoff > 30*time.Second {
					zeroPeerBackoff = 30 * time.Second
				}
				interval = zeroPeerBackoff
			} else {
				// Reset backoff and use standard 5s interval when healthy
				zeroPeerBackoff = 2 * time.Second
				interval = 5 * time.Second
			}

			timer.Reset(interval)
		case <-n.ctx.Done():
			return
		}
	}
}

func (n *ZypoNode) updateStaticRelays(relays []peer.AddrInfo) {
	validRelays := make([]peer.AddrInfo, 0, len(relays))
	for _, r := range relays {
		if r.ID != n.Host.ID() {
			validRelays = append(validRelays, r)
		}
	}
	if len(validRelays) == 0 {
		return
	}
	for _, r := range validRelays {
		n.Host.Peerstore().AddAddrs(r.ID, r.Addrs, peerstore.PermanentAddrTTL)
	}
}

func (n *ZypoNode) peerPersistenceLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			n.persistKnownPeers()
		case <-n.ctx.Done():
			return
		}
	}
}

func (n *ZypoNode) persistKnownPeers() {
	connectedPeers := n.Host.Network().Peers()
	infos := make([]peer.AddrInfo, 0, len(connectedPeers))
	for _, pid := range connectedPeers {
		if pid == n.Host.ID() {
			continue
		}
		addrs := n.Host.Peerstore().Addrs(pid)
		if len(addrs) > 0 {
			infos = append(infos, peer.AddrInfo{ID: pid, Addrs: addrs})
		}
	}
	if len(infos) > 0 {
		n.savePeers(infos)
	}
}

func (n *ZypoNode) routingTableRefreshLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			n.refreshDHT()
		case <-n.dhtRefreshTrigger:
			log.Println("DHT: Instant refresh triggered")
			n.refreshDHT()
		case <-n.ctx.Done():
			return
		}
	}
}

func (n *ZypoNode) refreshDHT() {
	log.Println("DHT: Refreshing routing table...")
	if err := n.DHT.Bootstrap(n.ctx); err != nil {
		log.Printf("DHT refresh error: %v", err)
	}
	n.persistKnownPeers()
}

func (n *ZypoNode) connectToBootstrap(addrStr string) *peer.AddrInfo {
	maddr, err := multiaddr.NewMultiaddr(addrStr)
	if err != nil {
		return nil
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil || addrInfo.ID == n.Host.ID() {
		return nil
	}

	ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
	defer cancel()

	if err := n.Host.Connect(ctx, *addrInfo); err != nil {
		return nil
	}

	log.Printf("Connected to bootstrap peer: %s", addrInfo.ID)

	// Track this ID as a trusted bootstrap peer
	exists := false
	n.bootstrapMu.Lock()
	for _, id := range n.BootstrapIDs {
		if id == addrInfo.ID {
			exists = true
			break
		}
	}
	if !exists {
		n.BootstrapIDs = append(n.BootstrapIDs, addrInfo.ID)
	}
	n.bootstrapHealth[addrInfo.ID] = time.Now()
	n.bootstrapMu.Unlock()

	// Resolve oracle public key from peerstore if not yet set.
	n.resolveOraclePubKey(addrInfo.ID)

	// Signal immediate DHT refresh upon successful bootstrap connection
	select {
	case n.dhtRefreshTrigger <- struct{}{}:
	default:
	}

	return addrInfo
}

type discoveryNotifee struct {
	h    host.Host
	node *ZypoNode
}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() {
		return
	}
	err := n.h.Connect(context.Background(), pi)
	if err == nil {
		go n.node.persistKnownPeers()
	}
}

func (n *ZypoNode) loadOraclePubKey() {
	if n.cfg.IsCommandCenter {
		return
	}
	b, err := os.ReadFile(filepath.Join(n.cfg.DataDir, "oracle.pub"))
	if err == nil {
		pub, err := crypto.UnmarshalPublicKey(b)
		if err == nil {
			n.validator.OraclePubKey = pub
			log.Printf("[DHT] Loaded Oracle public key from disk")
		}
	}
	
	pidb, err := os.ReadFile(filepath.Join(n.cfg.DataDir, "oracle.id"))
	if err == nil {
		n.validator.OraclePeerID = string(pidb)
		log.Printf("[DHT] Loaded Oracle Peer ID from disk: %s", string(pidb))
	}
}

// resolveOraclePubKey reads the CC peer's Ed25519 public key from the
// peerstore. libp2p populates the peerstore with the full key during the
// TLS/Noise handshake — this is the only reliable way for Ed25519 keys
// because the peer ID is merely a multihash of the key, not the key itself.
func (n *ZypoNode) resolveOraclePubKey(pid peer.ID) {
	if n.cfg.IsCommandCenter {
		return // CC is its own oracle; key already set
	}
	if n.validator.OraclePubKey != nil && n.validator.OraclePeerID != "" {
		return // already resolved
	}
	pub := n.Host.Peerstore().PubKey(pid)
	if pub == nil {
		log.Printf("[DHT] Warning: peerstore has no public key yet for %s (will retry on next connect)", pid)
		return
	}
	n.validator.OraclePubKey = pub
	n.validator.OraclePeerID = pid.String()
	log.Printf("[DHT] Oracle public key resolved from peerstore for peer %s", pid)

	// Persist for offline verification
	b, err := crypto.MarshalPublicKey(pub)
	if err == nil {
		os.MkdirAll(n.cfg.DataDir, 0755)
		os.WriteFile(filepath.Join(n.cfg.DataDir, "oracle.pub"), b, 0644)
		os.WriteFile(filepath.Join(n.cfg.DataDir, "oracle.id"), []byte(pid.String()), 0644)
	}
}

// GetConfig returns the node configuration
func (n *ZypoNode) GetConfig() Config {
	return n.cfg
}

func (n *ZypoNode) BootstrapNetwork() error {
	log.Println("Network: Bootstrapping DHT and connecting to bootstrap nodes...")
	if err := n.DHT.Bootstrap(n.ctx); err != nil {
		log.Printf("DHT bootstrap error: %v", err)
	}

	allBootstrap := append([]string{}, n.cfg.BootstrapNodes...)
	knownPeers := n.loadKnownPeers()
	allBootstrap = append(allBootstrap, knownPeers...)

	for _, addrStr := range allBootstrap {
		n.connectToBootstrap(addrStr)
	}
	return nil
}

// AnnounceDomain triggers a persistent background announcement of a domain
// to the Command Center until success.
func (n *ZypoNode) AnnounceDomain(domain string) {
	n.AnnounceDomainToCC(domain)
}

// AnnounceDomainToCC sends a domain registration request to the Command Center
// via P2P so CC can sign the record and publish it to DHT. This is the correct
// path for non-CC nodes to get their domains discoverable by the rest of the
// network. The local DNS override should be set before calling this so the node
// itself doesn't need DHT for self-resolution.
func (n *ZypoNode) AnnounceDomainToCC(domain string) {
	go func() {
		retries := 0
		maxRetries := 5
		delay := 5 * time.Second

		for retries < maxRetries {
			log.Printf("[Mesh] Attempting to announce domain %s to Command Center (Attempt %d/%d)...", domain, retries+1, maxRetries)

			// Collect all candidate peers (bootstrap first, then any connected peer).
			seen := make(map[peer.ID]bool)
			var targets []peer.ID
			n.bootstrapMu.Lock()
			for _, bid := range n.BootstrapIDs {
				if !seen[bid] {
					seen[bid] = true
					targets = append(targets, bid)
				}
			}
			n.bootstrapMu.Unlock()

			for _, p := range n.Host.Network().Peers() {
				if !seen[p] {
					seen[p] = true
					targets = append(targets, p)
				}
			}

			success := false
			for _, target := range targets {
				if target == n.Host.ID() {
					continue
				}

				ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
				s, err := n.Host.NewStream(ctx, target, ZypoProtocolID)
				cancel()
				if err != nil {
					continue
				}

				log.Printf("[Mesh] Sending registration for %s to peer %s", domain, target)

				body, _ := json.Marshal(map[string]string{"domain": domain, "peer_id": n.Host.ID().String()})
				req := ZypoRequest{
					Action:   "domain_request",
					Resource: domain,
					Method:   "POST",
					Size:     int64(len(body)),
				}
				reqBytes, _ := json.Marshal(req)
				s.Write(append(reqBytes, '\n'))
				s.Write(append(body, '\n'))

				// Wait for response
				reader := bufio.NewReader(s)
				s.SetReadDeadline(time.Now().Add(5 * time.Second))
				line, err := reader.ReadString('\n')
				s.Close()

				if err == nil {
					var resp ZypoHeader
					if json.Unmarshal([]byte(line), &resp) == nil && resp.Status == 200 {
						log.Printf("[Mesh] Successfully announced %s to %s", domain, target)
						success = true
						break
					}
				}
			}

			if success {
				return
			}

			log.Printf("[Mesh] No CC found to announce %s, retrying in %v...", domain, delay)
			select {
			case <-n.ctx.Done():
				return
			case <-time.After(delay):
			}
			retries++
			delay *= 2 // Exponential backoff
		}
		log.Printf("[Mesh] Warn: Failed to announce %s to CC after %d attempts. Operating in Mesh-Only Mode.", domain, maxRetries)
	}()
}

func (n *ZypoNode) PingPeer(pid peer.ID) bool {
	ctx, cancel := context.WithTimeout(n.ctx, 3*time.Second)
	defer cancel()

	s, err := n.Host.NewStream(ctx, pid, ZypoProtocolID)
	if err != nil {
		return false
	}
	defer s.Close()

	s.SetWriteDeadline(time.Now().Add(2 * time.Second))
	json.NewEncoder(s).Encode(ZypoRequest{Action: "ping"})

	var resp ZypoHeader
	s.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewDecoder(s).Decode(&resp); err == nil && resp.Status == 200 {
		n.bootstrapMu.Lock()
		n.bootstrapHealth[pid] = time.Now()
		n.bootstrapMu.Unlock()
		return true
	}
	return false
}

func (n *ZypoNode) bootstrapLivenessLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var toCheck []peer.ID
			n.bootstrapMu.Lock()
			for _, pid := range n.BootstrapIDs {
				toCheck = append(toCheck, pid)
			}
			n.bootstrapMu.Unlock()

			for _, pid := range toCheck {
				// We don't check connected status here,
				// we just try to ping. PingPeer will try to connect if needed.
				if !n.PingPeer(pid) {
					log.Printf("Network: Bootstrap peer %s is unresponsive", pid)
				}
			}
		case <-n.ctx.Done():
			return
		}
	}
}

func (n *ZypoNode) IsPeerHealthy(pid peer.ID) bool {
	n.bootstrapMu.Lock()
	defer n.bootstrapMu.Unlock()
	lastSeen, exists := n.bootstrapHealth[pid]
	if !exists {
		return false
	}
	return time.Since(lastSeen) < 2*time.Minute
}

func (n *ZypoNode) IsBootstrap(pid peer.ID) bool {
	n.bootstrapMu.Lock()
	defer n.bootstrapMu.Unlock()
	for _, id := range n.BootstrapIDs {
		if id == pid {
			return true
		}
	}
	return false
}

func (n *ZypoNode) IsCCConnected() bool {
	n.bootstrapMu.Lock()
	defer n.bootstrapMu.Unlock()
	for _, bid := range n.BootstrapIDs {
		if n.Host.Network().Connectedness(bid) == network.Connected {
			return true
		}
	}
	return false
}

func (n *ZypoNode) getStream(ctx context.Context, target peer.ID) (network.Stream, error) {
	n.streamPoolMu.Lock()
	if pool, ok := n.streamPool[target]; ok && len(pool) > 0 {
		s := pool[len(pool)-1]
		n.streamPool[target] = pool[:len(pool)-1]
		n.streamPoolMu.Unlock()

		// Validate stream is still alive
		// A simple way is to check the connection
		if s.Conn().IsClosed() {
			s.Reset()
			return n.getStream(ctx, target)
		}
		return s, nil
	}
	n.streamPoolMu.Unlock()

	return n.Host.NewStream(ctx, target, ZypoProtocolID)
}

func (n *ZypoNode) putStream(target peer.ID, s network.Stream) {
	if s == nil {
		return
	}

	// Don't pool closed or reset streams
	if s.Conn().IsClosed() {
		s.Reset()
		return
	}

	n.streamPoolMu.Lock()
	defer n.streamPoolMu.Unlock()

	pool := n.streamPool[target]
	if len(pool) < 10 { // Max 10 idle streams per peer
		n.streamPool[target] = append(pool, s)
	} else {
		s.Close()
	}
}

func (n *ZypoNode) Close() error {
	log.Println("[Node] Initiating Graceful Shutdown...")
	var err error
	if n.DHT != nil {
		err = n.DHT.Close()
	}
	if n.Host != nil {
		if e := n.Host.Close(); e != nil {
			err = e
		}
	}
	log.Println("[Node] Shutdown complete")
	return err
}
