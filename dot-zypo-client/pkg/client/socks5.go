package client

import (
	"context"
	"fmt"
	"log"
	"net"

	"github.com/things-go/go-socks5"
)

type SOCKS5Manager struct {
	server *socks5.Server
	vpn    *P2PVPNClient
}

func StartSOCKS5(port int, vpn *P2PVPNClient) (*SOCKS5Manager, error) {
	m := &SOCKS5Manager{vpn: vpn}

	server := socks5.NewServer(
		socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return m.dialViaVPN(ctx, network, addr)
		}),
	)
	m.server = server

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	go func() {
		log.Printf("[SOCKS5] Server started on %s", addr)
		if err := server.ListenAndServe("tcp", addr); err != nil {
			log.Printf("[SOCKS5] Error: %v", err)
		}
	}()

	return m, nil
}

func (m *SOCKS5Manager) dialViaVPN(ctx context.Context, network, addr string) (net.Conn, error) {
	if m.vpn == nil {
		return nil, fmt.Errorf("VPN not connected")
	}

	log.Printf("[SOCKS5] Dialing %s via P2P VPN...", addr)

	conn, err := m.vpn.Dial(network, addr)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

