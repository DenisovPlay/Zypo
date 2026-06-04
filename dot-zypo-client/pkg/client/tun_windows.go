//go:build windows

package client

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
	"golang.org/x/sys/windows"

	"golang.zx2c4.com/wintun"
)

type wintunIface struct {
	adapter *wintun.Adapter
	session wintun.Session
	name    string
	closed  bool
	mu      sync.Mutex
}

func (w *wintunIface) Name() string {
	return w.name
}

func (w *wintunIface) Read(b []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("wintun closed")
	}
	for {
		packet, err := w.session.ReceivePacket()
		if err != nil {
			// Typically means we need to wait for a packet
			if err.Error() == "The handle is invalid." || w.closed {
				return 0, fmt.Errorf("wintun closed")
			}
			windows.WaitForSingleObject(w.session.ReadWaitEvent(), windows.INFINITE)
			continue
		}
		
		n := copy(b, packet)
		w.session.ReleaseReceivePacket(packet)
		return n, nil
	}
}

func (w *wintunIface) Write(b []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("wintun closed")
	}
	packet, err := w.session.AllocateSendPacket(len(b))
	if err != nil {
		return 0, err
	}
	copy(packet, b)
	w.session.SendPacket(packet)
	return len(b), nil
}

func (w *wintunIface) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.session.End()
	return w.adapter.Close()
}

func openTUN(name string) (TUNInterface, error) {
	// Require Administrator privilege. If not, this fails.
	adapter, err := wintun.CreateAdapter(name, "Wintun", nil)
	if err != nil {
		// If it exists, try opening it
		adapter, err = wintun.OpenAdapter(name)
		if err != nil {
			return nil, fmt.Errorf("Failed to create or open Wintun adapter: %v", err)
		}
	}

	// 0x400000 is 4MB capacity
	session, err := adapter.StartSession(0x400000)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("Failed to start Wintun session: %v", err)
	}

	return &wintunIface{
		adapter: adapter,
		session: session,
		name:    name,
	}, nil
}

func (tm *TUNManager) cleanupRoutes() {
	log.Printf("[TUN] Cleaning up system routes (Windows)...")
	tm.runCmd("route", "delete", "0.0.0.0", "mask", "128.0.0.0")
	tm.runCmd("route", "delete", "128.0.0.0", "mask", "128.0.0.0")
	for _, ip := range tm.excludedIPs {
		tm.runCmd("route", "delete", ip)
	}
}

func (tm *TUNManager) configureRouting() error {
	if tm.origGateway != "" {
		for _, ipStr := range tm.excludedIPs {
			log.Printf("[TUN] Excluding %s from VPN (via %s)", ipStr, tm.origGateway)
			tm.runCmd("route", "add", ipStr, "mask", "255.255.255.255", tm.origGateway)
		}
	}

	err1 := tm.runCmd("route", "add", "0.0.0.0", "mask", "128.0.0.0", "10.0.0.1")
	err2 := tm.runCmd("route", "add", "128.0.0.0", "mask", "128.0.0.0", "10.0.0.1")
	if err1 != nil {
		return err1
	}
	return err2
}

func (tm *TUNManager) configureOS() error {
	name := tm.iface.Name()
	hostIP := "10.0.0.2"

	log.Printf("[TUN] Configuring Windows interface %s to %s", name, hostIP)
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", "name="+name, "static", hostIP, "255.255.255.0", "10.0.0.1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh failed: %v, output: %s", err, string(out))
	}
	return nil
}
