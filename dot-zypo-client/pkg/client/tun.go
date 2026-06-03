package client

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/songgao/water"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

type TUNManager struct {
	iface         *water.Interface
	s             *stack.Stack
	vpnClient     *P2PVPNClient
	excludedIPs   []string
	origGateway   string
	isActive      bool
	BytesSent     uint64
	BytesReceived uint64
}

var GlobalTUN *TUNManager

func StartTUN(name string, vpnClient *P2PVPNClient, excludedIPs []string) (*TUNManager, error) {
	config := water.Config{DeviceType: water.TUN}
	config.Name = name
	iface, err := water.New(config)
	if err != nil {
		return nil, err
	}

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	// Increase buffer sizes (e.g. 4MB)
	rcvBufSize := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: 1048576, Max: 4194304}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcvBufSize)

	sndBufSize := tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: 1048576, Max: 4194304}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sndBufSize)

	// Increase channel buffer to hold more packets
	linkID := channel.New(4096, 1500, "")
	if terr := s.CreateNIC(1, linkID); terr != nil {
		return nil, fmt.Errorf("NIC error: %v", terr)
	}

	// CRITICAL: Allow gvisor to accept packets destined for any IP
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)

	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := iface.Read(buf)
			if err != nil {
				return
			}
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(buf[:n])})
			linkID.InjectInbound(ipv4.ProtocolNumber, pkt)
			pkt.DecRef()
		}
	}()

	go func() {
		for {
			pkt := linkID.ReadContext(context.Background())
			if pkt == nil {
				return
			}
			buf := make([]byte, 0, pkt.Size())
			for _, v := range pkt.AsSlices() {
				buf = append(buf, v...)
			}
			iface.Write(buf)
		}
	}()

	ipAddr := tcpip.AddrFromSlice([]byte{10, 0, 0, 1})
	if terr := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: ipAddr.WithPrefix()}, stack.AddressProperties{}); terr != nil {
		return nil, fmt.Errorf("IP error: %v", terr)
	}

	subnet, _ := tcpip.NewSubnet(tcpip.AddrFromSlice([]byte{0, 0, 0, 0}), tcpip.MaskFrom("\x00\x00\x00\x00"))
	s.SetRouteTable([]tcpip.Route{{Destination: subnet, NIC: 1}})

	tm := &TUNManager{iface: iface, s: s, vpnClient: vpnClient, excludedIPs: excludedIPs}
	GlobalTUN = tm

	tm.detectGateway()
	tm.setupHandlers()

	log.Printf("[TUN] Interface %s initialized with TCP+UDP forwarding (Down/Idle).", name)
	return tm, nil
}

func (tm *TUNManager) runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[TUN] CMD Error: %s %v -> %s (Err: %v)", name, args, string(out), err)
		return err
	}
	return nil
}

func (tm *TUNManager) Activate() error {
	if tm.isActive {
		return nil
	}
	log.Printf("[TUN] Activating system-wide routing...")

	// 1. Bring interface up and set IP
	if err := tm.configureOS(); err != nil {
		return fmt.Errorf("OS config failed: %v", err)
	}

	// Re-detect the gateway in case the network changed since daemon startup
	tm.detectGateway()

	// 2. Set routes
	if err := tm.configureRouting(); err != nil {
		return fmt.Errorf("routing failed: %v", err)
	}

	tm.isActive = true
	log.Printf("[TUN] VPN Routing ACTIVE.")
	return nil
}

func (tm *TUNManager) Deactivate() {
	if !tm.isActive {
		return
	}
	log.Printf("[TUN] Deactivating system-wide routing...")
	tm.cleanupRoutes()

	// Bring interface down
	name := tm.iface.Name()
	if runtime.GOOS == "darwin" {
		tm.runCmd("ifconfig", name, "down")
	} else {
		tm.runCmd("ip", "link", "set", "dev", name, "down")
	}

	tm.isActive = false
	log.Printf("[TUN] VPN Routing DEACTIVATED.")
}

func (tm *TUNManager) detectGateway() {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sh", "-c", "netstat -nr | grep default | awk '{print $2}' | head -n 1").Output()
		if err == nil {
			tm.origGateway = strings.TrimSpace(string(out))
			log.Printf("[TUN] Detected original gateway (darwin): %s", tm.origGateway)
		}
	} else if runtime.GOOS == "linux" {
		out, err := exec.Command("sh", "-c", "ip route show default | awk '/default/ {print $3}' | head -n 1").Output()
		if err == nil {
			tm.origGateway = strings.TrimSpace(string(out))
			log.Printf("[TUN] Detected original gateway (linux): %s", tm.origGateway)
		}
	}
}

func (tm *TUNManager) Cleanup() {
	tm.Deactivate()
	tm.iface.Close()
}

// OS-specific functions (cleanupRoutes, configureRouting, configureOS) are implemented in tun_linux.go, tun_darwin.go, etc.

func (tm *TUNManager) setupHandlers() {
	// TCP forwarder: intercepts all TCP connections and tunnels them via P2P VPN
	tcpForwarder := tcp.NewForwarder(tm.s, 0, 65535, func(r *tcp.ForwarderRequest) {
		var w waiter.Queue
		ep, terr := r.CreateEndpoint(&w)
		if terr != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)
		go tm.handleTCPConn(gonet.NewTCPConn(&w, ep))
	})
	tm.s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)

	// UDP forwarder: intercepts UDP datagrams (DNS on :53, etc.) and tunnels via P2P VPN
	// This enables DNS-over-VPN and UDP app support through the TUN interface.
	udpForwarder := udp.NewForwarder(tm.s, func(r *udp.ForwarderRequest) bool {
		var w waiter.Queue
		ep, terr := r.CreateEndpoint(&w)
		if terr != nil {
			return false
		}
		go tm.handleUDPConn(gonet.NewUDPConn(&w, ep))
		return true
	})
	tm.s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
}

func (tm *TUNManager) handleTCPConn(conn net.Conn) {
	defer conn.Close()
	if tm.vpnClient == nil {
		return
	}

	// In a transparent proxy, LocalAddr is the destination the client was trying to reach,
	// and RemoteAddr is the client's own source IP.
	targetAddr := conn.LocalAddr().String()
	log.Printf("[TUN] Intercepted TCP connection to %s, forwarding via P2P VPN...", targetAddr)

	vpnConn, err := tm.vpnClient.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("[TUN ERR] Failed to connect VPN stream for %s: %v", targetAddr, err)
		return
	}
	defer vpnConn.Close()
	log.Printf("[TUN] Successfully established P2P VPN stream for %s", targetAddr)

	// Bidirectional pipe
	errChan := make(chan error, 2)
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				vpnConn.Write(buf[:n])
				atomic.AddUint64(&tm.BytesSent, uint64(n))
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := vpnConn.Read(buf)
			if n > 0 {
				conn.Write(buf[:n])
				atomic.AddUint64(&tm.BytesReceived, uint64(n))
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()

	err2 := <-errChan
	if err2 != nil {
		log.Printf("[TUN] Connection closed for %s: %v", targetAddr, err2)
	} else {
		log.Printf("[TUN] Connection closed for %s (clean shutdown)", targetAddr)
	}
}

// handleUDPConn handles intercepted UDP datagrams and forwards them through the P2P VPN.
// This enables DNS-over-VPN (port 53) and general UDP app support.
func (tm *TUNManager) handleUDPConn(conn *gonet.UDPConn) {
	defer conn.Close()
	if tm.vpnClient == nil {
		return
	}

	// LocalAddr is the original destination the client was sending UDP to
	targetAddr := conn.LocalAddr().String()

	isDNS := false
	if _, port, err := net.SplitHostPort(targetAddr); err == nil && port == "53" {
		isDNS = true
		log.Printf("[TUN/UDP] Intercepted DNS query → %s", targetAddr)
	} else {
		log.Printf("[TUN/UDP] Intercepted UDP datagram → %s", targetAddr)
	}
	_ = isDNS

	// Open a VPN stream for UDP forwarding using a dedicated protocol
	vpnConn, err := tm.vpnClient.DialUDP(targetAddr)
	if err != nil {
		log.Printf("[TUN/UDP] Failed to open UDP VPN stream for %s: %v", targetAddr, err)
		return
	}
	defer vpnConn.Close()

	// Bidirectional pipe for UDP datagrams
	errChan := make(chan error, 2)
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				vpnConn.Write(buf[:n])
				atomic.AddUint64(&tm.BytesSent, uint64(n))
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := vpnConn.Read(buf)
			if n > 0 {
				conn.Write(buf[:n])
				atomic.AddUint64(&tm.BytesReceived, uint64(n))
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()

	<-errChan
}
