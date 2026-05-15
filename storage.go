//go:build tinygo

package main

import (
	"fmt"
	"machine"

	"tinygo.org/x/drivers/sdcard"
)

var blockDevice *sdcard.Device

func initStorage() error {
	spi := machine.SPI0

	cfg := machine.SPIConfig{
		Frequency: 400000,
		SCK:       machine.GPIO12,
		SDO:       machine.GPIO11,
		SDI:       machine.GPIO13,
	}
	if err := spi.Configure(cfg); err != nil {
		return err
	}

	sdPin := machine.GPIO10
	blockDevice = sdcard.New(spi, sdPin)

	if err := blockDevice.Configure(); err != nil {
		return fmt.Errorf("failed to configure SD card: %v", err)
	}

	fmt.Println("SD card initialized successfully.")

	cfg.Frequency = 20000000
	if err := spi.Configure(cfg); err != nil {
		return err
	}

	return nil
}
