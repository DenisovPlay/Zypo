//go:build darwin

package client

import (
	"log"

	"github.com/songgao/water"
)

func (tm *TUNManager) cleanupRoutes() {
	log.Printf("[TUN] Cleaning up system routes (Darwin)...")
	tm.runCmd("route", "delete", "-net", "0.0.0.0/1")
	tm.runCmd("route", "delete", "-net", "128.0.0.0/1")
	for _, ip := range tm.excludedIPs {
		tm.runCmd("route", "delete", ip)
	}
}

func (tm *TUNManager) configureRouting() error {
	name := tm.iface.Name()

	// 1. Add host routes for excluded IPs
	if tm.origGateway != "" {
		for _, ip := range tm.excludedIPs {
			log.Printf("[TUN] Excluding %s from VPN (via %s)", ip, tm.origGateway)
			tm.runCmd("route", "add", "-host", ip, tm.origGateway)
		}
	}

	// 2. Override default route
	if err := tm.runCmd("route", "add", "-net", "0.0.0.0/1", "-interface", name); err != nil {
		return err
	}
	return tm.runCmd("route", "add", "-net", "128.0.0.0/1", "-interface", name)
}

func (tm *TUNManager) configureOS() error {
	name := tm.iface.Name()
	hostIP := "10.0.0.2"
	gwIP := "10.0.0.1"
	return tm.runCmd("ifconfig", name, hostIP, gwIP, "up")
}

type waterIface struct {
	*water.Interface
}

func (w *waterIface) Name() string {
	return w.Interface.Name()
}

func openTUN(name string) (TUNInterface, error) {
	config := water.Config{
		DeviceType: water.TUN,
		PlatformSpecificParams: water.PlatformSpecificParams{
			Name: name,
		},
	}
	iface, err := water.New(config)
	if err != nil {
		return nil, err
	}
	return &waterIface{iface}, nil
}
