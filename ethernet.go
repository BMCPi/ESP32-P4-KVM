//go:build tinygo

package main

import "fmt"

func initEthernet() error {
	// Placeholder for ESP32-P4 ETH PHY setup. This must be configured with
	// board-specific pins/driver calls before deploying on hardware.
	fmt.Println("Initializing Ethernet interface...")
	return nil
}
