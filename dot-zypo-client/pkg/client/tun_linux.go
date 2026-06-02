//go:build linux

package client

import (
	"fmt"
	"log"
	"net"

	"github.com/vishvananda/netlink"
)

func (tm *TUNManager) cleanupRoutes() {
	log.Printf("[TUN] Cleaning up system routes (Linux/Netlink)...")

	// Helper to delete routes by dst string
	delRoute := func(dst string) {
		_, ipnet, err := net.ParseCIDR(dst)
		if err != nil {
			ip := net.ParseIP(dst)
			if ip == nil {
				return
			}
			ipnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
		}
		filter := &netlink.Route{Dst: ipnet}
		routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_DST)
		if err == nil {
			for _, r := range routes {
				netlink.RouteDel(&r)
			}
		}
	}

	delRoute("0.0.0.0/1")
	delRoute("128.0.0.0/1")
	for _, ip := range tm.excludedIPs {
		delRoute(ip)
	}
}

func (tm *TUNManager) configureRouting() error {
	link, err := netlink.LinkByName(tm.iface.Name())
	if err != nil {
		return fmt.Errorf("failed to find link %s: %v", tm.iface.Name(), err)
	}

	// 1. Add host routes for excluded IPs via original gateway
	if tm.origGateway != "" {
		gwIP := net.ParseIP(tm.origGateway)
		for _, ipStr := range tm.excludedIPs {
			log.Printf("[TUN] Excluding %s from VPN (via %s)", ipStr, tm.origGateway)
			dstIP := net.ParseIP(ipStr)
			if dstIP != nil {
				route := netlink.Route{
					Dst: &net.IPNet{IP: dstIP, Mask: net.CIDRMask(32, 32)},
					Gw:  gwIP,
				}
				netlink.RouteAdd(&route)
			}
		}
	}

	// 2. Override default route
	addRoute := func(dst string) error {
		_, ipnet, err := net.ParseCIDR(dst)
		if err != nil {
			return err
		}
		return netlink.RouteAdd(&netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       ipnet,
		})
	}

	if err := addRoute("0.0.0.0/1"); err != nil {
		return err
	}
	return addRoute("128.0.0.0/1")
}

func (tm *TUNManager) configureOS() error {
	name := tm.iface.Name()
	hostIP := "10.0.0.2"

	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("failed to find link %s: %v", name, err)
	}

	addr, _ := netlink.ParseAddr(hostIP + "/24")
	if err := netlink.AddrAdd(link, addr); err != nil {
		log.Printf("[TUN] AddrAdd warn: %v", err) // might already exist
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring link up: %v", err)
	}
	return nil
}
