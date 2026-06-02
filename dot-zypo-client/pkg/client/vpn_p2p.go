package client

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
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
	buf := make([]byte, 3)
	_, err = io.ReadFull(s, buf)
	if err != nil {
		log.Printf("[VPN] Handshake read error from %s: %v", c.providerID, err)
		s.Close()
		return nil, err
	}

	if string(buf) != "OK\n" {
		log.Printf("[VPN] Provider %s rejected connection to %s", c.providerID, addr)
		s.Close()
		return nil, fmt.Errorf("provider rejected connection")
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
