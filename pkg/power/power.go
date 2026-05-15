//go:build tinygo

// Package power manages the host machine's power button and power-sense GPIO
// pins. It is shared between the main package (GPIO setup) and the api package
// (power-action worker).
package power

import (
	"machine"
	"time"
)

// PinOutputModeGPOpenDrain is the open-drain output mode for ESP32-P4 GPIO.
const PinOutputModeGPOpenDrain machine.PinMode = 4

var (
	// Button is the power-button GPIO output (GPIO16, open-drain).
	Button machine.Pin
	// Sense is the power-sense GPIO input (GPIO17, active-low: low = powered on).
	Sense machine.Pin
)

// Setup configures the power-button and power-sense pins. Call once from main
// before starting the power-action worker or the API server.
func Setup() {
	Button = machine.GPIO16
	Button.Configure(machine.PinConfig{Mode: PinOutputModeGPOpenDrain})
	Button.High()

	Sense = machine.GPIO17
	Sense.Configure(machine.PinConfig{Mode: machine.PinInput})
}

// PressButton drives pin low for duration then releases it high, simulating a
// physical button press on the host machine's power button header.
func PressButton(pin machine.Pin, duration time.Duration) {
	pin.Low()
	time.Sleep(duration)
	pin.High()
}
