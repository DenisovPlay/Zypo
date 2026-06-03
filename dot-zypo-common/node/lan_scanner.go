package node

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	reuseport "github.com/libp2p/go-reuseport"
	"github.com/multiformats/go-multiaddr"
)

const udpBroadcastPort = 8909
const magicTokenV1 = "ZYPO_LAN_V1"
const magicTokenV2 = "ZYPO_LAN_V2"

// StartLANScanner aggressively searches for local peers using UDP broadcasts across ALL interfaces.
// V2 packets are signed with Ed25519 — prevents any peer from impersonating as CC on LAN.
func (n *ZypoNode) StartLANScanner() {
	go n.listenBroadcasts()
	go n.broadcastPresence()
}

func (n *ZypoNode) buildV2Payload() string {
	nodeType := "client"
	if n.cfg.IsCommandCenter {
		nodeType = "cc"
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	// Message to sign: token|peerID|port|nodeType|timestamp
	msg := fmt.Sprintf("%s|%s|%d|%s|%s", magicTokenV2, n.Host.ID().String(), n.cfg.ListenPort, nodeType, ts)
	sig, err := n.PrivKey.Sign([]byte(msg))
	if err != nil {
		// Fallback to V1 if signing fails (shouldn't happen)
		return fmt.Sprintf("%s|%s|%d|%s", magicTokenV1, n.Host.ID().String(), n.cfg.ListenPort, nodeType)
	}
	return fmt.Sprintf("%s|%s|%d|%s|%s|%s", magicTokenV2, n.Host.ID().String(), n.cfg.ListenPort, nodeType, ts, hex.EncodeToString(sig))
}

func (n *ZypoNode) broadcastPresence() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	n.sendBroadcastsOnAllInterfaces()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.sendBroadcastsOnAllInterfaces()
		}
	}
}

func (n *ZypoNode) sendBroadcastsOnAllInterfaces() {
	payload := n.buildV2Payload()

	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			if ipNet.IP.To4() != nil && iface.Flags&net.FlagBroadcast != 0 {
				// IPv4 broadcast
				broadcastIP := make(net.IP, 4)
				for i := range ipNet.IP.To4() {
					broadcastIP[i] = ipNet.IP.To4()[i] | ^ipNet.Mask[i]
				}
				n.sendUDP(broadcastIP.String(), payload)
			} else if ipNet.IP.IsLinkLocalUnicast() && ipNet.IP.To4() == nil {
				// IPv6 link-local multicast — sends to all nodes on the link
				n.sendUDPv6(fmt.Sprintf("[ff02::1%%%s]", iface.Name), payload)
			}
		}
	}

	// Also send to global broadcast
	n.sendUDP("255.255.255.255", payload)
}

func (n *ZypoNode) sendUDP(ip string, payload string) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", ip, udpBroadcastPort))
	if err != nil {
		return
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.Write([]byte(payload))
}

func (n *ZypoNode) sendUDPv6(addr string, payload string) {
	udpAddr, err := net.ResolveUDPAddr("udp6", fmt.Sprintf("%s:%d", addr, udpBroadcastPort))
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp6", nil, udpAddr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.Write([]byte(payload))
}

func (n *ZypoNode) listenBroadcasts() {
	addr := fmt.Sprintf(":%d", udpBroadcastPort)
	
	// Start IPv4 and IPv6 listeners concurrently
	go n.listenUDPNetwork("udp4", addr)
	go n.listenUDPNetwork("udp6", addr)
}

func (n *ZypoNode) listenUDPNetwork(networkType, addr string) {
	conn, err := reuseport.ListenPacket(networkType, addr)
	if err != nil {
		log.Printf("[LAN] Failed to start %s listener (reuse mode failed): %v", networkType, err)
		conn, err = net.ListenPacket(networkType, addr)
		if err != nil {
			log.Printf("[LAN] Failed to listen on %s port %d: %v", networkType, udpBroadcastPort, err)
			return
		}
	}
	defer conn.Close()

	log.Printf("[LAN] %s Discovery listener active on %s", networkType, addr)

	buf := make([]byte, 2048)
	for {
		if n.ctx.Err() != nil {
			return
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		bytesRead, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}

		data := string(buf[:bytesRead])
		n.handleLANPacket(data, remoteAddr)
	}
}

func (n *ZypoNode) handleLANPacket(data string, remoteAddr net.Addr) {
	parts := strings.Split(data, "|")
	if len(parts) < 3 {
		return
	}

	switch parts[0] {
	case magicTokenV2:
		n.handleV2Packet(parts, remoteAddr)
	case magicTokenV1:
		// V1: unsigned. Accept for regular client discovery but NEVER trust as CC.
		n.handleV1Packet(parts, remoteAddr, false)
	}
}

// handleV2Packet processes a signed V2 LAN discovery packet.
// Format: ZYPO_LAN_V2|peerID|port|nodeType|timestamp|sigHex
func (n *ZypoNode) handleV2Packet(parts []string, remoteAddr net.Addr) {
	if len(parts) < 6 {
		return
	}
	peerIDStr := parts[1]
	port := parts[2]
	nodeType := parts[3]
	ts := parts[4]
	sigHex := parts[5]

	if peerIDStr == n.Host.ID().String() {
		return // Ignore our own broadcasts
	}

	// Reconstruct the signed message and verify
	msg := fmt.Sprintf("%s|%s|%s|%s|%s", magicTokenV2, peerIDStr, port, nodeType, ts)
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		log.Printf("[LAN V2] Dropped packet from %s: invalid sig hex", peerIDStr[:8])
		return
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return
	}

	pub, err := pid.ExtractPublicKey()
	if err != nil {
		// Key embedded in peer ID only for Ed25519; for RSA we need the peerstore
		pub = n.Host.Peerstore().PubKey(pid)
		if pub == nil {
			log.Printf("[LAN V2] Cannot verify %s: no public key available yet", peerIDStr[:8])
			return
		}
	}

	ok, err := pub.Verify([]byte(msg), sigBytes)
	if err != nil || !ok {
		log.Printf("[LAN V2] Dropped packet from %s: invalid signature", peerIDStr[:8])
		return
	}

	// Signature valid — proceed with connection
	ip := "127.0.0.1"
	if udpAddr, ok := remoteAddr.(*net.UDPAddr); ok {
		ip = udpAddr.IP.String()
	}

	n.connectLANPeer(pid, ip, port, nodeType, true)
}

// handleV1Packet processes an unsigned legacy V1 packet.
// trustAsCC is always false for V1 — unsigned packets cannot claim CC authority.
func (n *ZypoNode) handleV1Packet(parts []string, remoteAddr net.Addr, trustAsCC bool) {
	if len(parts) < 3 {
		return
	}
	peerIDStr := parts[1]
	port := parts[2]
	nodeType := "client"
	if len(parts) >= 4 {
		nodeType = parts[3]
		if !trustAsCC && nodeType == "cc" {
			// SECURITY: V1 packet cannot prove CC identity — downgrade to regular peer
			log.Printf("[LAN V1] Ignoring CC claim in unsigned packet from %s", peerIDStr[:min(8, len(peerIDStr))])
			nodeType = "client"
		}
	}

	if peerIDStr == n.Host.ID().String() {
		return
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return
	}

	ip := "127.0.0.1"
	if udpAddr, ok := remoteAddr.(*net.UDPAddr); ok {
		ip = udpAddr.IP.String()
	}

	n.connectLANPeer(pid, ip, port, nodeType, false)
}

// connectLANPeer tries to connect to a LAN-discovered peer, attempting both TCP and QUIC.
func (n *ZypoNode) connectLANPeer(pid peer.ID, ip, port, nodeType string, verified bool) {
	// Build both TCP and QUIC addresses and try each
	addrsToTry := []string{
		fmt.Sprintf("/ip4/%s/tcp/%s", ip, port),
		fmt.Sprintf("/ip4/%s/udp/%s/quic-v1", ip, port),
	}

	for _, maddrStr := range addrsToTry {
		maddr, err := multiaddr.NewMultiaddr(maddrStr)
		if err != nil {
			continue
		}

		n.Host.Peerstore().AddAddr(pid, maddr, time.Hour)
	}

	// Attempt connection (libp2p will try all addresses in peerstore)
	ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
	tcpMaddr, _ := multiaddr.NewMultiaddr(addrsToTry[0])
	err := n.Host.Connect(ctx, peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{tcpMaddr}})
	cancel()

	if err != nil {
		// Try QUIC as fallback
		ctx2, cancel2 := context.WithTimeout(n.ctx, 5*time.Second)
		quicMaddr, _ := multiaddr.NewMultiaddr(addrsToTry[1])
		err = n.Host.Connect(ctx2, peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{quicMaddr}})
		cancel2()
	}

	if err != nil {
		return
	}

	log.Printf("[LAN] Connected to %s %s at %s:%s (verified=%v)", nodeType, pid, ip, port, verified)

	if nodeType == "cc" && verified {
		// Only accept CC claims from cryptographically verified V2 packets
		n.bootstrapMu.Lock()
		found := false
		for _, b := range n.BootstrapIDs {
			if b == pid {
				found = true
				break
			}
		}
		if !found {
			n.BootstrapIDs = append(n.BootstrapIDs, pid)
			n.bootstrapHealth[pid] = time.Now()
			log.Printf("[LAN] Added verified Command Center %s to bootstraps", pid)
		}
		n.bootstrapMu.Unlock()
		n.resolveOraclePubKey(pid)
	} else if nodeType == "cc" && !verified {
		log.Printf("[LAN] WARN: Ignoring unverified CC claim from %s (V1 packet)", pid)
	}

	go n.persistKnownPeers()
}

// handleGossipForLAN handles receiving JSON-based gossip from peers (used in cc_gossip response)
func handleGossipJSON(data []byte) []string {
	var addrs []string
	json.Unmarshal(data, &addrs)
	return addrs
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
