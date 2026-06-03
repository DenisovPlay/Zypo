package node

import (
	"bufio"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// StartP2PVPNServer listens for incoming raw VPN streams over libp2p
func (n *ZypoNode) StartP2PVPNServer() {
	n.Host.SetStreamHandler("/zypo/vpn/1.0.0", func(s network.Stream) {
		peerID := s.Conn().RemotePeer().String()
		log.Printf("[P2P VPN] Incoming connection from %s", peerID)

		br := bufio.NewReader(s)
		s.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			s.Close()
			return
		}

		parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(parts) != 2 || parts[0] != "CONNECT" {
			s.Write([]byte("ERR\n"))
			s.Close()
			return
		}

		targetAddr := strings.TrimSpace(parts[1])

		// ACL/Restriction check
		if n.EconomyManager != nil && n.EconomyManager.GetPrepaidTraffic(peerID) <= 0 {
			log.Printf("[P2P VPN] Rejected %s: Out of prepaid traffic", peerID)
			s.Write([]byte("ERR: OUT_OF_TRAFFIC\n"))
			s.Close()
			return
		}

		// SECURITY: Anti-SSRF + DNS rebinding protection.
		// We resolve the hostname ONCE, validate ALL returned IPs, then dial
		// using the resolved IP directly — never the hostname again.
		// This prevents DNS rebinding attacks where the first lookup returns a
		// public IP (passes check) but a second lookup (inside DialTimeout) returns
		// an internal IP like 10.0.0.1, giving access to the internal network.
		host, portStr, err := net.SplitHostPort(targetAddr)
		if err != nil {
			// No port — treat the whole string as a host
			host = targetAddr
			portStr = ""
		}

		ips, err := net.LookupIP(host)
		if err != nil {
			log.Printf("[P2P VPN] Rejected %s: Failed to resolve host %s", peerID, host)
			s.Write([]byte("ERR: RESOLVE_FAILED\n"))
			s.Close()
			return
		}

		var dialIP net.IP
		for _, ip := range ips {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
				log.Printf("[P2P VPN] Rejected %s: SSRF attempt to %s (%s)", peerID, targetAddr, ip.String())
				s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
				s.Close()
				return
			}
			// CGNAT range (100.64.0.0/10) — also private
			if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
				log.Printf("[P2P VPN] Rejected %s: SSRF attempt to CGNAT range %s", peerID, ip.String())
				s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
				s.Close()
				return
			}
			if dialIP == nil {
				dialIP = ip // Save the first valid public IP for dialing
			}
		}

		if dialIP == nil {
			log.Printf("[P2P VPN] Rejected %s: no routable IP for %s", peerID, host)
			s.Write([]byte("ERR: RESOLVE_FAILED\n"))
			s.Close()
			return
		}

		// Build dial address using the resolved IP, not the hostname.
		// This is the critical fix: the hostname is never used again after this point.
		var resolvedDialAddr string
		if portStr != "" {
			resolvedDialAddr = net.JoinHostPort(dialIP.String(), portStr)
		} else {
			resolvedDialAddr = dialIP.String()
		}

		log.Printf("[P2P VPN] Dialing %s (resolved: %s) on behalf of %s", targetAddr, resolvedDialAddr, peerID)

		nc, err := net.DialTimeout("tcp", resolvedDialAddr, 10*time.Second)
		if err != nil {
			log.Printf("[P2P VPN] Failed to dial %s: %v", targetAddr, err)
			s.Write([]byte("ERR\n"))
			s.Close()
			return
		}
		log.Printf("[P2P VPN] Successfully connected to %s", targetAddr)

		s.SetDeadline(time.Time{}) // Clear deadlines for long running proxy
		s.Write([]byte("OK\n"))

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			buf := make([]byte, 32*1024)
			for {
				nBytes, err := br.Read(buf)
				if nBytes > 0 {
					if n.EconomyManager != nil && !n.EconomyManager.ConsumePrepaidTraffic(peerID, int64(nBytes)) {
						log.Printf("[P2P VPN] Disconnecting %s: out of prepaid traffic", peerID)
						nc.Close()
						s.Close()
						return
					}
					_, werr := nc.Write(buf[:nBytes])
					if werr != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
			nc.Close()
		}()
		
		go func() {
			defer wg.Done()
			buf := make([]byte, 32*1024)
			for {
				nBytes, err := nc.Read(buf)
				if nBytes > 0 {
					if n.EconomyManager != nil && !n.EconomyManager.ConsumePrepaidTraffic(peerID, int64(nBytes)) {
						log.Printf("[P2P VPN] Disconnecting %s: out of prepaid traffic", peerID)
						s.Close()
						nc.Close()
						return
					}
					_, werr := s.Write(buf[:nBytes])
					if werr != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
			s.Close()
		}()

		wg.Wait()
		log.Printf("[P2P VPN] Closed connection to %s", targetAddr)
	})
}

// StartP2PVPNUDPServer handles UDP datagram forwarding over /zypo/vpn-udp/1.0.0.
// This enables DNS-over-VPN and general UDP app support for clients behind NAT.
func (n *ZypoNode) StartP2PVPNUDPServer() {
	n.Host.SetStreamHandler("/zypo/vpn-udp/1.0.0", func(s network.Stream) {
		peerID := s.Conn().RemotePeer().String()
		defer s.Close()

		br := bufio.NewReader(s)
		s.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}

		parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(parts) != 2 || parts[0] != "UDP" {
			s.Write([]byte("ERR: INVALID_CMD\n"))
			return
		}

		targetAddr := strings.TrimSpace(parts[1])

		// SECURITY: Same SSRF + DNS rebinding protection as TCP VPN
		host, portStr, err := net.SplitHostPort(targetAddr)
		if err != nil {
			host = targetAddr
			portStr = "53" // Default to DNS port for UDP
		}

		ips, err := net.LookupIP(host)
		if err != nil {
			s.Write([]byte("ERR: RESOLVE_FAILED\n"))
			return
		}

		var dialIP net.IP
		for _, ip := range ips {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
				ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
				log.Printf("[P2P VPN/UDP] Rejected %s: SSRF to %s (%s)", peerID, targetAddr, ip)
				s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
				return
			}
			if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
				s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
				return
			}
			if dialIP == nil {
				dialIP = ip
			}
		}

		if dialIP == nil {
			s.Write([]byte("ERR: RESOLVE_FAILED\n"))
			return
		}

		resolvedAddr := net.JoinHostPort(dialIP.String(), portStr)

		// Economy check
		if n.EconomyManager != nil && n.EconomyManager.GetPrepaidTraffic(peerID) <= 0 {
			s.Write([]byte("ERR: OUT_OF_TRAFFIC\n"))
			return
		}

		nc, err := net.DialTimeout("udp", resolvedAddr, 5*time.Second)
		if err != nil {
			log.Printf("[P2P VPN/UDP] Failed to dial UDP %s: %v", resolvedAddr, err)
			s.Write([]byte("ERR\n"))
			return
		}
		defer nc.Close()

		s.SetDeadline(time.Time{})
		s.Write([]byte("OK\n"))

		log.Printf("[P2P VPN/UDP] Relaying UDP to %s for %s", resolvedAddr, peerID)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			buf := make([]byte, 65535)
			for {
				nBytes, err := br.Read(buf)
				if nBytes > 0 {
					if n.EconomyManager != nil && !n.EconomyManager.ConsumePrepaidTraffic(peerID, int64(nBytes)) {
						nc.Close()
						return
					}
					nc.Write(buf[:nBytes])
				}
				if err != nil {
					break
				}
			}
			nc.Close()
		}()

		go func() {
			defer wg.Done()
			buf := make([]byte, 65535)
			for {
				nc.SetReadDeadline(time.Now().Add(30 * time.Second))
				nBytes, err := nc.Read(buf)
				if nBytes > 0 {
					if n.EconomyManager != nil && !n.EconomyManager.ConsumePrepaidTraffic(peerID, int64(nBytes)) {
						s.Close()
						return
					}
					s.Write(buf[:nBytes])
				}
				if err != nil {
					break
				}
			}
			s.Close()
		}()

		wg.Wait()
		log.Printf("[P2P VPN/UDP] Closed UDP relay to %s", targetAddr)
	})
}
