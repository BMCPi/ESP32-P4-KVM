//go:build tinygo

package main

import "fmt"

func startVirtualMedia() {
	// Integration point for an ESP-IDF TinyUSB MSC bridge that maps
	// blockDevice read/write callbacks to host USB requests.
	fmt.Println("Virtual media bridge initialized (stub).")
}
