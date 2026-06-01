package client

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
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
	if err != nil { return nil, err }

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	linkID := channel.New(512, 1500, "")
	if terr := s.CreateNIC(1, linkID); terr != nil { return nil, fmt.Errorf("NIC error: %v", terr) }
	
	// CRITICAL: Allow gvisor to accept packets destined for any IP
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)

	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := iface.Read(buf)
			if err != nil { return }
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(buf[:n])})
			linkID.InjectInbound(ipv4.ProtocolNumber, pkt)
			pkt.DecRef()
		}
	}()

	go func() {
		for {
			pkt := linkID.ReadContext(context.Background())
			if pkt == nil { return }
			buf := make([]byte, 0, pkt.Size())
			for _, v := range pkt.AsSlices() { buf = append(buf, v...) }
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
	
	// Exclude system DNS servers to prevent DNS drops (since UDP proxy is not implemented)
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		lines := strings.Split(string(b), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "nameserver ") {
				ip := strings.TrimSpace(strings.TrimPrefix(line, "nameserver "))
				if ip != "" && ip != "127.0.0.1" && ip != "::1" {
					tm.excludedIPs = append(tm.excludedIPs, ip)
				}
			}
		}
	}

	tm.detectGateway()
	tm.setupHandlers()
	
	log.Printf("[TUN] Interface %s initialized (Down/Idle).", name)
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
	if tm.isActive { return nil }
	log.Printf("[TUN] Activating system-wide routing...")

	// 1. Bring interface up and set IP
	if err := tm.configureOS(); err != nil {
		return fmt.Errorf("OS config failed: %v", err)
	}

	// 2. Set routes
	if err := tm.configureRouting(); err != nil {
		return fmt.Errorf("routing failed: %v", err)
	}

	tm.isActive = true
	log.Printf("[TUN] VPN Routing ACTIVE.")
	return nil
}

func (tm *TUNManager) Deactivate() {
	if !tm.isActive { return }
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
			log.Printf("[TUN] Detected original gateway: %s", tm.origGateway)
		}
	}
}

func (tm *TUNManager) Cleanup() {
	tm.Deactivate()
	tm.iface.Close()
}

func (tm *TUNManager) cleanupRoutes() {
	log.Printf("[TUN] Cleaning up system routes...")
	switch runtime.GOOS {
	case "darwin":
		tm.runCmd("route", "delete", "-net", "0.0.0.0/1")
		tm.runCmd("route", "delete", "-net", "128.0.0.0/1")
		for _, ip := range tm.excludedIPs {
			tm.runCmd("route", "delete", ip)
		}
	case "linux":
		tm.runCmd("ip", "route", "del", "0.0.0.0/1")
		tm.runCmd("ip", "route", "del", "128.0.0.0/1")
		for _, ip := range tm.excludedIPs {
			tm.runCmd("ip", "route", "del", ip)
		}
	}
}

func (tm *TUNManager) configureRouting() error {
	name := tm.iface.Name()

	// 1. Add host routes for excluded IPs (Bootstrap/Relay) via original gateway
	if tm.origGateway != "" {
		for _, ip := range tm.excludedIPs {
			log.Printf("[TUN] Excluding %s from VPN (via %s)", ip, tm.origGateway)
			if runtime.GOOS == "darwin" {
				tm.runCmd("route", "add", "-host", ip, tm.origGateway)
			} else {
				tm.runCmd("ip", "route", "add", ip, "via", tm.origGateway)
			}
		}
	}

	// 2. Override default route
	switch runtime.GOOS {
	case "darwin":
		if err := tm.runCmd("route", "add", "-net", "0.0.0.0/1", "-interface", name); err != nil { return err }
		return tm.runCmd("route", "add", "-net", "128.0.0.0/1", "-interface", name)
	case "linux":
		if err := tm.runCmd("ip", "route", "add", "0.0.0.0/1", "dev", name); err != nil { return err }
		return tm.runCmd("ip", "route", "add", "128.0.0.0/1", "dev", name)
	default:
		return nil
	}
}

func (tm *TUNManager) configureOS() error {
	name := tm.iface.Name()
	hostIP := "10.0.0.2"
	gwIP := "10.0.0.1"

	switch runtime.GOOS {
	case "darwin":
		return tm.runCmd("ifconfig", name, hostIP, gwIP, "up")
	case "linux":
		tm.runCmd("ip", "addr", "add", hostIP+"/24", "dev", name)
		return tm.runCmd("ip", "link", "set", "dev", name, "up")
	default:
		return fmt.Errorf("OS %s requires manual IP configuration (%s)", runtime.GOOS, hostIP)
	}
}

func (tm *TUNManager) setupHandlers() {
	tcpForwarder := tcp.NewForwarder(tm.s, 0, 65535, func(r *tcp.ForwarderRequest) {
		var w waiter.Queue
		ep, terr := r.CreateEndpoint(&w)
		if terr != nil { r.Complete(true); return }
		r.Complete(false)
		go tm.handleTCPConn(gonet.NewTCPConn(&w, ep))
	})
	tm.s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
}

func (tm *TUNManager) handleTCPConn(conn net.Conn) {
	defer conn.Close()
	if tm.vpnClient == nil { return }

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
			if err != nil { errChan <- err; return }
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
			if err != nil { errChan <- err; return }
		}
	}()

	err2 := <-errChan
	log.Printf("[TUN] Connection error for %s: %v", targetAddr, err2)
	log.Printf("[TUN] Connection error for %s: %v", targetAddr, err)
	log.Printf("[TUN] Closed TCP connection to %s", targetAddr)
}
