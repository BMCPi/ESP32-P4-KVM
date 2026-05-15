//go:build tinygo && !esp32p4_ethernet

package main

import "errors"

// initEthernet is a stub for targets where the TinyGo machine package does
// not yet implement ESP32-P4 RMII Ethernet support (machine.EthConfig /
// machine.InitEthernet).  Build with the "esp32p4_ethernet" tag once the
// machine package support lands.
func initEthernet() error {
	return errors.New("ethernet: not supported on this target")
}
