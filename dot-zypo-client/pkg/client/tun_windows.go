//go:build windows

package client

import (
	"fmt"
)

func (tm *TUNManager) cleanupRoutes() {
	// Not fully implemented for Windows Wintun
}

func (tm *TUNManager) configureRouting() error {
	return fmt.Errorf("VPN routing on Windows requires Wintun APIs, currently unsupported")
}

func (tm *TUNManager) configureOS() error {
	return fmt.Errorf("VPN OS config on Windows requires Wintun APIs, currently unsupported")
}
