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

		// Strong SSRF Protection
		host, _, err := net.SplitHostPort(targetAddr)
		if err != nil {
			host = targetAddr
		}
		
		ips, err := net.LookupIP(host)
		if err != nil {
			log.Printf("[P2P VPN] Rejected %s: Failed to resolve host %s", peerID, host)
			s.Write([]byte("ERR: RESOLVE_FAILED\n"))
			s.Close()
			return
		}

		for _, ip := range ips {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
				log.Printf("[P2P VPN] Rejected %s: SSRF attempt to %s (%s)", peerID, targetAddr, ip.String())
				s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
				s.Close()
				return
			}
		}

		log.Printf("[P2P VPN] Dialing %s on behalf of %s", targetAddr, peerID)

		nc, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
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
