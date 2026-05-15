//go:build tinygo && (rp2040 || rp2350 || atsamd51 || atsamd21 || nrf52840)

package main

import (
	"fmt"

	"tinygo.org/x/tinyfs/littlefs"
)

// filesystem is a LittleFS instance mounted on lfsDevice, the tail partition
// of the SD card. It provides local storage for the ESP32 (configs, payloads)
// and is invisible to the remote machine, which sees only mscDevice.
var filesystem *littlefs.LFS

// mountFilesystem mounts LittleFS on lfsDevice (the last 64 MiB of the SD
// card). On a blank partition the superblock won't be present, so it formats
// and retries automatically.
func mountFilesystem() error {
	filesystem = littlefs.New(lfsDevice)
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
