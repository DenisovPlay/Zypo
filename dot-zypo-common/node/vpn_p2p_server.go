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

		// TODO: Add ACL/Restriction check here
		log.Printf("[P2P VPN] Dialing %s on behalf of %s", targetAddr, s.Conn().RemotePeer())

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
