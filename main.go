//go:build tinygo

package main

import (
	"time"
	"unsafe"

	"github.com/bmcpi/esp32-p4-kvm/pkg/api"
	"github.com/bmcpi/esp32-p4-kvm/pkg/power"
  "github.com/bmcpi/esp32-p4-kvm/pkg/serial"
)

const (
	DR_REG_GPIO_BASE = 0x50110000                // P4 specific GPIO base
	GPIO_OUT_W1TS    = DR_REG_GPIO_BASE + 0x0008 // Set register
	GPIO_OUT_W1TC    = DR_REG_GPIO_BASE + 0x000C // Clear register
	pplClockFreq     = 80_000_000
	// Define RMII / MAC registers here
)

func setPin(pin int, high bool) {
	reg := GPIO_OUT_W1TC
	if high {
		reg = GPIO_OUT_W1TS
	}
	*(*uint32)(unsafe.Pointer(uintptr(reg))) = (1 << uint32(pin))
}

func main() {
	println("Starting ESP32-P4 KVM Controller")

	println("Setting up GPIO...")
	power.Setup()
	println("GPIO setup complete.")

	println("Starting power action worker...")
	api.StartPowerActionWorker()
	println("Power action worker started.")

	println("Initializing storage...")

	// if err := storage.InitStorage(); err != nil {
	// 	println("Storage warning: Virtual Media unavailable -", err.Error())
	// } else {
	// 	storage.StartVirtualMedia()
	// }

	if err := serial.InitSerial(); err != nil {
		println("Serial warning:", err.Error())
	}

	// go api.StartAPIServer()

	for {
		println("Main loop: running...")
		time.Sleep(1 * time.Second)
	}
}
