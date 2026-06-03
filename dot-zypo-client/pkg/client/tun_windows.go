//go:build windows

package client

import (
	"fmt"
	"log"
	"os/exec"
)

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

	err1 := tm.runCmd("route", "add", "0.0.0.0", "mask", "128.0.0.0", "10.0.0.2")
	err2 := tm.runCmd("route", "add", "128.0.0.0", "mask", "128.0.0.0", "10.0.0.2")
	if err1 != nil {
		return err1
	}
	return err2
}

func (tm *TUNManager) configureOS() error {
	name := tm.iface.Name()
	hostIP := "10.0.0.2"

	log.Printf("[TUN] Configuring Windows interface %s to %s", name, hostIP)
	cmd := exec.Command("netsh", "interface", "ip", "set", "address", "name="+name, "static", hostIP, "255.255.255.0", "none")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh failed: %v, output: %s", err, string(out))
	}
	return nil
}
