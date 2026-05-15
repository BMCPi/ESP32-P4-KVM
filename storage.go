//go:build tinygo

package main

import (
	"machine"

	"github.com/tinywasm/fmt"

	"tinygo.org/x/drivers/sdcard"
)

// lfsPartitionSize is the space reserved at the END of the SD card for the
// ESP32's own LittleFS partition (configs, payloads, etc.). The front of the
// card is presented to the remote machine over USB-C as a raw MSC disk.
const lfsPartitionSize = 64 * 1024 * 1024 // 64 MiB

// rawCard is the underlying SD card block device.
var rawCard *sdcard.Device

// mscDevice is the front partition of the SD card (from sector 0 to
// card_size - lfsPartitionSize). It is handed to the USB MSC driver so the
// remote machine sees a normal raw disk it can format or write boot images to.
var mscDevice *PartitionDevice

// lfsDevice is the tail partition of the SD card (last lfsPartitionSize bytes).
// It is mounted as LittleFS for the ESP32's local storage and is completely
// invisible to the remote machine.
var lfsDevice *PartitionDevice

// PartitionDevice presents a contiguous byte range of rawCard as an independent
// block device. Both msc.Port (machine.BlockDevice) and littlefs.New
// (tinyfs.BlockDevice) accept it via structural typing because it implements
// the same method set (ReadAt, WriteAt, Size, WriteBlockSize, EraseBlockSize,
// EraseBlocks).
type PartitionDevice struct {
	start int64 // byte offset of partition start within rawCard
	size  int64 // byte length of this partition
}

func (p *PartitionDevice) ReadAt(buf []byte, off int64) (int, error) {
	return rawCard.ReadAt(buf, p.start+off)
}

func (p *PartitionDevice) WriteAt(buf []byte, off int64) (int, error) {
	return rawCard.WriteAt(buf, p.start+off)
}

func (p *PartitionDevice) Size() int64 { return p.size }

func (p *PartitionDevice) WriteBlockSize() int64 { return rawCard.WriteBlockSize() }

func (p *PartitionDevice) EraseBlockSize() int64 { return rawCard.EraseBlockSize() }

// EraseBlocks erases count erase-blocks starting at block number start
// (block-indexed, relative to this partition). The partition start byte offset
// must be erase-block-aligned, which is guaranteed by the 64 MiB boundary.
func (p *PartitionDevice) EraseBlocks(start, count int64) error {
	blockSize := rawCard.EraseBlockSize()
	absStart := p.start/blockSize + start
	return rawCard.EraseBlocks(absStart, count)
}

func initStorage() error {
	spi := machine.SPI0

	// Low frequency for SD card initialisation (≤400 kHz per SD spec).
	cfg := machine.SPIConfig{
		Frequency: 400_000,
		SCK:       machine.GPIO12,
		SDO:       machine.GPIO11,
		SDI:       machine.GPIO13,
	}
	if err := spi.Configure(cfg); err != nil {
		return fmt.Errf("SPI configure: %w", err)
	}

	bd := sdcard.New(spi, machine.GPIO10, machine.GPIO12, machine.GPIO11, machine.GPIO13)
	rawCard = &bd

	if err := rawCard.Configure(); err != nil {
		return fmt.Errf("SD card configure: %w", err)
	}

	// Switch to high-speed clock after successful card init.
	cfg.Frequency = 20_000_000
	if err := spi.Configure(cfg); err != nil {
		return fmt.Errf("SPI high-speed configure: %w", err)
	}

	totalSize := rawCard.Size()
	if totalSize <= lfsPartitionSize {
		return fmt.Errf("SD card too small (%d bytes); need > %d", totalSize, lfsPartitionSize)
	}

	// Front of card → remote machine via USB-C MSC.
	mscDevice = &PartitionDevice{start: 0, size: totalSize - lfsPartitionSize}
	// Tail of card → LittleFS local storage, hidden from remote machine.
	lfsDevice = &PartitionDevice{start: totalSize - lfsPartitionSize, size: lfsPartitionSize}

	fmt.Printf("SD card %d MiB: MSC %d MiB | LFS %d MiB\n",
		totalSize>>20, mscDevice.size>>20, lfsDevice.size>>20)

	return mountFilesystem()
}
