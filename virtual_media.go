//go:build tinygo && esp32p4

package main

import "machine/usb/msc"

// startVirtualMedia registers mscDevice with the TinyGo USB Mass Storage Class
// driver. The remote machine (e.g. Raspberry Pi) connected via the USB-C port
// sees the front partition of the SD card as a raw disk and can format it,
// write boot images to it, or boot from it directly.
//
// The tail partition (lfsDevice / LittleFS) is not exposed over MSC and
// remains private to the ESP32 firmware.
func startVirtualMedia() {
	port := msc.Port(mscDevice)
	port.SetVendorID("BMCPi")
	port.SetProductID("KVM VirtualMedia")
	port.SetProductRev("1.0")
	println("Virtual media USB MSC started.")
}
