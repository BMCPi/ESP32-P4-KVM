//go:build tinygo

package main

import (
	"machine"
	"time"
	"unsafe"
)

var (
	pwrButton machine.Pin
	sensePin  machine.Pin
)

const (
	PinOutputModeGPOpenDrain machine.PinMode = 4
	DR_REG_GPIO_BASE                         = 0x50110000                // P4 specific GPIO base
	GPIO_OUT_W1TS                            = DR_REG_GPIO_BASE + 0x0008 // Set register
	GPIO_OUT_W1TC                            = DR_REG_GPIO_BASE + 0x000C // Clear register
	pplClockFreq                             = 80_000_000
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
	setupGPIO()
	println("GPIO setup complete.")

	println("Starting power action worker...")
	startPowerActionWorker()
	println("Power action worker started.")

	println("Initializing storage...")

	// if err := initStorage(); err != nil {
	// 	println("Storage warning: Virtual Media unavailable -", err.Error())
	// } else {
	// 	startVirtualMedia()
	// }

	// if err := initSerial(); err != nil {
	// 	println("Serial warning:", err.Error())
	// }

	go startAPIServer()

	for {
		time.Sleep(1 * time.Second)
	}
}

func setupGPIO() {
	pwrButton = machine.GPIO16
	pwrButton.Configure(machine.PinConfig{Mode: PinOutputModeGPOpenDrain})
	pwrButton.High()

	sensePin = machine.GPIO17
	sensePin.Configure(machine.PinConfig{Mode: machine.PinInput})
}

func pressButton(pin machine.Pin, duration time.Duration) {
	pin.Low()
	time.Sleep(duration)
	pin.High()
}
