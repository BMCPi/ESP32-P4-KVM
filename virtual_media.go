//go:build tinygo && (rp2040 || rp2350 || atsamd51 || atsamd21 || nrf52840)

package main

import "machine/usb/msc"

// startVirtualMedia registers blockDevice with the TinyGo USB Mass Storage
// Class driver. After this call, the managed host (e.g. Raspberry Pi) sees the
// SD card as a raw USB disk and can boot from it.
//
// This is the embedded equivalent of the Linux bulk-transfer path in
// cmd/rpiboot/main.go: instead of pushing a payload into the Pi BootROM over
// a USB bulk OUT endpoint, the firmware exposes the storage device directly as
// a USB MSC device so the host can read boot files at its own pace.
func startVirtualMedia() {
	port := msc.Port(blockDevice)
	port.SetVendorID("BMCPi")
	port.SetProductID("KVM VirtualMedia")
	port.SetProductRev("1.0")
	println("Virtual media USB MSC started.")
}
