//go:build tinygo

package main

import (
	"machine"
	"time"
)

var (
	pwrButton machine.Pin
	sensePin  machine.Pin
)

const (
	PinOutputModeGPOpenDrain machine.PinMode = 4
)

func main() {
	setupGPIO()
	startPowerActionWorker()

	if err := initStorage(); err != nil {
		println("Storage warning: Virtual Media unavailable -", err.Error())
	} else {
		startVirtualMedia()
	}

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
