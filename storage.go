//go:build tinygo

package main

import (
	"fmt"
	"machine"

	"tinygo.org/x/drivers/sdcard"
	"tinygo.org/x/tinyfs/littlefs"
)

var (
	// blockDevice is the raw SD card block device. It is passed directly to
	// the USB MSC driver so the managed host sees a raw disk image.
	blockDevice *sdcard.Device

	// filesystem is a LittleFS instance mounted on blockDevice. The firmware
	// uses it for its own file access (boot payloads, configs).
	//
	// NOTE: LittleFS and the USB MSC driver share the same underlying block
	// device. Concurrent access is safe only when no MSC write transfer is
	// in progress; unmount the filesystem before transferring large images.
	filesystem *littlefs.LFS
)

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

	// Mount LittleFS. On a fresh card the superblock won't be present, so
	// format first and then retry the mount.
	filesystem = littlefs.New(blockDevice)
	if err := filesystem.Mount(); err != nil {
		fmt.Println("LittleFS mount failed, formatting:", err)
		if fmtErr := filesystem.Format(); fmtErr != nil {
			return fmt.Errorf("LittleFS format: %w", fmtErr)
		}
		if err := filesystem.Mount(); err != nil {
			return fmt.Errorf("LittleFS mount after format: %w", err)
		}
	}

	fmt.Println("Storage ready (SD card + LittleFS).")
	return nil
}

// readPayload reads a named file from LittleFS and returns its contents.
// This mirrors the os.ReadFile call in cmd/rpiboot/main.go, adapted for the
// embedded filesystem.
func readPayload(name string) ([]byte, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem not mounted")
	}
	f, err := filesystem.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}

	buf := make([]byte, info.Size())
	if _, err := f.Read(buf); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return buf, nil
}
