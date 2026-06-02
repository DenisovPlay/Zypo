package node

import (
	"context"
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
const magicToken = "ZYPO_LAN_V1"

// StartLANScanner aggressively searches for local peers using UDP broadcasts across ALL interfaces.
// This ensures that even in an isolated intranet without mDNS or Command Center,
// nodes can find each other and establish a mesh network.
func (n *ZypoNode) StartLANScanner() {
	go n.listenBroadcasts()
	go n.broadcastPresence()
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
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	nodeType := "client"
	if n.cfg.IsCommandCenter {
		nodeType = "cc"
	}
	payload := fmt.Sprintf("%s|%s|%d|%s", magicToken, n.Host.ID().String(), n.cfg.ListenPort, nodeType)

	for _, iface := range ifaces {
		if iface.Flags&net.FlagBroadcast == 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}

			// Calculate broadcast address for this specific interface/subnet
			broadcastIP := make(net.IP, 4)
			for i := range ipNet.IP.To4() {
				broadcastIP[i] = ipNet.IP.To4()[i] | ^ipNet.Mask[i]
			}

			n.sendUDP(broadcastIP.String(), payload)
		}
	}

	// Also send to global broadcast for good measure
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

func (n *ZypoNode) listenBroadcasts() {
	addr := fmt.Sprintf(":%d", udpBroadcastPort)
	// Bullet-proof: Use reuseport to allow multiple nodes on the same machine
	// to share the LAN discovery port. Essential for Browser + YAN on one Mac.
	conn, err := reuseport.ListenPacket("udp4", addr)
	if err != nil {
		log.Printf("[LAN] Failed to start UDP listener (reuse mode failed, falling back to exclusive): %v", err)
		// Fallback to standard listen if reuseport not supported by OS
		conn, err = net.ListenPacket("udp4", addr)
		if err != nil {
			log.Printf("[LAN] Failed to listen on UDP port %d: %v", udpBroadcastPort, err)
			return
		}
	}
	defer conn.Close()

	log.Printf("[LAN] UDP Discovery listener active on %s (shared)", addr)

	buf := make([]byte, 1024)
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
		parts := strings.Split(data, "|")
		if len(parts) >= 3 && parts[0] == magicToken {
			peerIDStr, port := parts[1], parts[2]
			nodeType := "client"
			if len(parts) >= 4 {
				nodeType = parts[3]
			}

			if peerIDStr == n.Host.ID().String() {
				continue
			}

			pid, err := peer.Decode(peerIDStr)
			if err != nil {
				continue
			}

			// Get IP from sender
			ip := "127.0.0.1"
			if udpAddr, ok := remoteAddr.(*net.UDPAddr); ok {
				ip = udpAddr.IP.String()
			}

			maddrStr := fmt.Sprintf("/ip4/%s/tcp/%s", ip, port)
			maddr, err := multiaddr.NewMultiaddr(maddrStr)
			if err != nil {
				continue
			}

			n.Host.Peerstore().AddAddr(pid, maddr, time.Hour)

			ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
			err = n.Host.Connect(ctx, peer.AddrInfo{ID: pid, Addrs: []multiaddr.Multiaddr{maddr}})
			cancel()

			if err == nil {
				log.Printf("[LAN] Connected to %s %s at %s", nodeType, pid, maddrStr)
				if nodeType == "cc" {
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
						log.Printf("[LAN] Added discovered Command Center %s to bootstraps", pid)
					}
					n.bootstrapMu.Unlock()
				}
			}
		}
	}
}
