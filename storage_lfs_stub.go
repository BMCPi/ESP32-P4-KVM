//go:build tinygo && !(rp2040 || rp2350 || atsamd51 || atsamd21 || nrf52840)

package main

import "fmt"

// mountFilesystem is a stub for targets where tinygo.org/x/tinyfs/littlefs
// cannot be linked (e.g. ESP32-S3) because TinyGo does not provide the
// __wrap_malloc / __wrap_free CGo allocator hooks on those targets.
// The SD card block device is still initialised and available for USB MSC
// when that target gains full TinyGo USB MSC support.
func mountFilesystem() error {
	fmt.Println("Storage ready (SD card, LittleFS not available on this target).")
	return nil
}

// readPayload is a stub; payload reads require LittleFS which is unavailable
// on this target.
func readPayload(name string) ([]byte, error) {
	return nil, fmt.Errorf("readPayload: LittleFS not supported on this target")
}
