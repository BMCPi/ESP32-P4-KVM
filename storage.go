//go:build tinygo

package main

import (
	"fmt"
	"machine"

	"tinygo.org/x/drivers/sdcard"
)

// blockDevice is the raw SD card block device. It is passed directly to the
// USB MSC driver so the managed host sees a raw disk image. It is also handed
// to the LittleFS layer on targets that support CGo malloc wrappers.
var blockDevice *sdcard.Device

func initStorage() error {
	spi := machine.SPI0

	// Low frequency for card initialisation (≤400 kHz per SD spec).
	cfg := machine.SPIConfig{
		Frequency: 400_000,
		SCK:       machine.GPIO12,
		SDO:       machine.GPIO11,
		SDI:       machine.GPIO13,
	}
	if err := spi.Configure(cfg); err != nil {
		return fmt.Errorf("SPI configure: %w", err)
	}

	csPin := machine.GPIO10
	bd := sdcard.New(spi, csPin, machine.GPIO12, machine.GPIO11, machine.GPIO13)
	blockDevice = &bd

	if err := blockDevice.Configure(); err != nil {
		return fmt.Errorf("SD card configure: %w", err)
	}

	// Switch to high-speed clock for normal block I/O.
	cfg.Frequency = 20_000_000
	if err := spi.Configure(cfg); err != nil {
		return fmt.Errorf("SPI high-speed configure: %w", err)
	}

	return mountFilesystem()
}
