package client

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/dot-zypo/daemon/common/node"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// P2PVPNClient handles raw VPN traffic over libp2p instead of WebRTC
type P2PVPNClient struct {
	node       *node.ZypoNode
	providerID string
}

var GlobalP2PVPNClient *P2PVPNClient

func NewP2PVPNClient(n *node.ZypoNode) *P2PVPNClient {
	GlobalP2PVPNClient = &P2PVPNClient{node: n}
	return GlobalP2PVPNClient
}

func (c *P2PVPNClient) ConnectToProvider(providerID string) error {
	pid, err := peer.Decode(providerID)
	if err != nil {
		return fmt.Errorf("invalid provider peer ID: %v", err)
	}
	c.providerID = providerID
	log.Printf("[VPN] Target provider set to %s", providerID)

	// Test connection to provider first before prepaying
	ctx, cancel := context.WithTimeout(c.node.GetContext(), 10*time.Second)
	defer cancel()
	if err := c.node.Host.Connect(ctx, peer.AddrInfo{ID: pid}); err != nil {
		c.providerID = ""
		return fmt.Errorf("failed to connect to provider: %v", err)
	}

	if c.node.EconomyManager != nil {
		price := c.node.GetConfig().VpnPrice
		if price <= 0 {
			price = 0.5
		}
		// Prepay for 100 MB to avoid large token drains on reconnects
		prepayAmount := price * 0.1
		if prepayAmount < 0.001 {
			prepayAmount = 0.001
		}
		_, err := c.node.EconomyManager.CreateAndSendTransaction(providerID, prepayAmount, "VPN Prepay")
		if err != nil {
			log.Printf("[VPN] Warning: Failed to prepay VPN provider: %v", err)
			c.providerID = ""
			return fmt.Errorf("prepayment failed: %v", err)
		}
		log.Printf("[VPN] Prepaid %.4f ZPCN to VPN provider %s", prepayAmount, providerID)
	}

	// Try to open a stream to verify the VPN protocol is supported and reachable
	s, err := c.node.Host.NewStream(ctx, pid, "/zypo/vpn/1.0.0")
	if err != nil {
		c.providerID = ""
		return fmt.Errorf("failed to verify VPN stream with provider: %v", err)
	}
	s.Reset() // Force close the stream immediately since it was just a test

	// Automatically activate TUN routing if available
	if GlobalTUN != nil {
		// Dynamically find and exclude provider's IPs to prevent routing loops
		addrs := c.node.Host.Peerstore().Addrs(pid)
		for _, maddr := range addrs {
			if ipStr, err := maddr.ValueForProtocol(multiaddr.P_IP4); err == nil {
				// Append if not already in the list
				exists := false
				for _, excluded := range GlobalTUN.excludedIPs {
					if excluded == ipStr {
						exists = true
						break
					}
				}
				if !exists {
					GlobalTUN.excludedIPs = append(GlobalTUN.excludedIPs, ipStr)
				}
			}
		}

		if err := GlobalTUN.Activate(); err != nil {
			log.Printf("[VPN] Warning: Failed to activate system routing: %v", err)
		}
	}

	return nil
}

func (c *P2PVPNClient) Disconnect() {
	log.Printf("[VPN] Disconnecting from %s...", c.providerID)
	c.providerID = ""
	if GlobalTUN != nil {
		GlobalTUN.Deactivate()
	}
}

func (c *P2PVPNClient) Dial(network, addr string) (net.Conn, error) {
	if c.providerID == "" {
		return nil, fmt.Errorf("no provider selected")
	}

	pid, err := peer.Decode(c.providerID)
	if err != nil {
		return nil, err
	}

	log.Printf("[VPN] Dialing %s via provider %s...", addr, c.providerID)

	ctx, cancel := context.WithTimeout(c.node.GetContext(), 15*time.Second)
	defer cancel()

	// Ensure we are connected and clear any dial backoffs from network switches
	if len(c.node.Host.Network().ConnsToPeer(pid)) == 0 {
		log.Printf("[VPN] Not connected to %s, attempting explicit connect to clear backoff...", pid)
		_ = c.node.Host.Connect(ctx, peer.AddrInfo{ID: pid})
	}

	s, err := c.node.Host.NewStream(ctx, pid, "/zypo/vpn/1.0.0")
	if err != nil {
		log.Printf("[VPN] Stream error to %s: %v", c.providerID, err)
		return nil, fmt.Errorf("failed to open p2p vpn stream: %v", err)
	}

	// Handshake
	log.Printf("[VPN] Sending CONNECT %s request...", addr)
	_, err = s.Write([]byte(fmt.Sprintf("CONNECT %s\n", addr)))
	if err != nil {
		log.Printf("[VPN] Write error to %s: %v", c.providerID, err)
		s.Close()
		return nil, err
	}

	// Read response
	reader := bufio.NewReader(s)
	s.SetReadDeadline(time.Now().Add(15 * time.Second))
	respLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("[VPN] Handshake read error from %s: %v", c.providerID, err)
		s.Close()
		return nil, err
	}

	if respLine != "OK\n" {
		log.Printf("[VPN] Provider %s rejected connection to %s: %s", c.providerID, addr, strings.TrimSpace(respLine))
		s.Close()
		return nil, fmt.Errorf("provider rejected connection: %s", strings.TrimSpace(respLine))
	}

	log.Printf("[VPN] Connection to %s ESTABLISHED via %s", addr, c.providerID)
	return &p2pVPNConn{Stream: s, remoteAddr: addr}, nil
}

type p2pVPNConn struct {
	network.Stream
	remoteAddr string
}

func (c *p2pVPNConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (c *p2pVPNConn) RemoteAddr() net.Addr {
	host, portStr, err := net.SplitHostPort(c.remoteAddr)
	if err != nil {
		return &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0}
	}
	port, _ := net.LookupPort("tcp", portStr)
	return &net.TCPAddr{IP: net.ParseIP(host), Port: port}
}

func (c *p2pVPNConn) SetDeadline(t time.Time) error {
	return c.Stream.SetDeadline(t)
}

func (c *p2pVPNConn) SetReadDeadline(t time.Time) error {
	return c.Stream.SetReadDeadline(t)
}

func (c *p2pVPNConn) SetWriteDeadline(t time.Time) error {
	return c.Stream.SetWriteDeadline(t)
}

// DialUDP opens a P2P stream for UDP datagram forwarding.
// Uses a dedicated /zypo/vpn-udp/1.0.0 protocol to distinguish from TCP streams.
func (c *P2PVPNClient) DialUDP(addr string) (net.Conn, error) {
	if c.providerID == "" {
		return nil, fmt.Errorf("no provider selected")
	}

	pid, err := peer.Decode(c.providerID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(c.node.GetContext(), 10*time.Second)
	defer cancel()

	s, err := c.node.Host.NewStream(ctx, pid, "/zypo/vpn-udp/1.0.0")
	if err != nil {
		// Fallback: tunnel UDP over the regular VPN stream with a UDP prefix
		log.Printf("[VPN/UDP] vpn-udp protocol not supported by provider, falling back to TCP tunnel for %s", addr)
		return c.Dial("udp", addr)
	}

	// Handshake: send UDP CONNECT
	_, err = s.Write([]byte(fmt.Sprintf("UDP %s\n", addr)))
	if err != nil {
		s.Close()
		return nil, err
	}

	reader := bufio.NewReader(s)
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(resp) != "OK" {
		s.Close()
		return nil, fmt.Errorf("provider rejected UDP connection to %s: %s", addr, strings.TrimSpace(resp))
	}
	s.SetReadDeadline(time.Time{})

	log.Printf("[VPN/UDP] UDP tunnel to %s ESTABLISHED via %s", addr, c.providerID)
	return &p2pVPNConn{Stream: s, remoteAddr: addr}, nil
}
