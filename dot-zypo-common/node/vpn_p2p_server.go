package node

import (
	"bufio"
	"io"
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
		log.Printf("[P2P VPN] Incoming connection from %s", s.Conn().RemotePeer())

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
		peerID := s.Conn().RemotePeer().String()
		if n.EconomyManager != nil && n.EconomyManager.GetBalance(peerID) <= 0 {
			log.Printf("[P2P VPN] Rejected %s: Insufficient balance", peerID)
			s.Write([]byte("ERR: INSUFFICIENT_FUNDS\n"))
			s.Close()
			return
		}

		// SSRF Protection: Do not allow dialing local/private IPs
		host, _, err := net.SplitHostPort(targetAddr)
		if err != nil {
			host = targetAddr
		}
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
				log.Printf("[P2P VPN] Rejected %s: SSRF attempt to %s", peerID, targetAddr)
				s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
				s.Close()
				return
			}
		} else if host == "localhost" {
			log.Printf("[P2P VPN] Rejected %s: SSRF attempt to localhost", peerID)
			s.Write([]byte("ERR: FORBIDDEN_TARGET\n"))
			s.Close()
			return
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
			io.Copy(nc, br)
			nc.Close()
		}()
		go func() {
			defer wg.Done()
			io.Copy(s, nc)
			s.Close()
		}()

		wg.Wait()
		log.Printf("[P2P VPN] Closed connection to %s", targetAddr)
	})
}
