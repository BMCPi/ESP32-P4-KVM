//go:build tinygo && !(rp2040 || rp2350 || atsamd51 || atsamd21 || nrf52840 || esp32p4)

package main

// startVirtualMedia is a stub for targets that do not yet have full TinyGo
// USB MSC support (machine.BlockDevice + AckUsbOutTransfer). On those
// platforms (e.g. ESP32-S3) the USB stack does not expose the MSC driver, so
// we log a notice and return. When an ESP32-P4 target is added to TinyGo and
// its USB MSC layer is wired up, this file should be replaced by building with
// a tag that matches virtual_media.go instead.
func startVirtualMedia() {
	println("Virtual media USB MSC: not supported on this target (stub).")
}
