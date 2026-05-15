//go:build esp32p4

package machine

import (
	"runtime/interrupt"
	"unsafe"
)

/*
#include <stdint.h>

// ESP32-P4 ROM SPI flash functions.
// Addresses from esp32p4.rom.api.ld (ESP-IDF v5.x).
// Flash is memory-mapped on the DROM bus starting at 0x44000000.
extern int esp_rom_spiflash_read(uint32_t src_addr, uint32_t *data, uint32_t len);
extern int esp_rom_spiflash_write(uint32_t dest_addr, const uint32_t *data, uint32_t len);
extern int esp_rom_spiflash_erase_sector(uint32_t sector_num);
extern int esp_rom_spiflash_unlock(void);
extern void Cache_Invalidate_All(void);
*/
import "C"

// compile-time check for ensuring we fulfill BlockDevice interface
var _ BlockDevice = flashBlockDevice{}

// Flash is the on-board SPI flash storage on the ESP32-P4.
var Flash flashBlockDevice

type flashBlockDevice struct{}

// ReadAt reads the given number of bytes from the block device.
// Uses the DROM memory-mapped window (0x44000000) for zero-copy reads.
func (f flashBlockDevice) ReadAt(p []byte, off int64) (n int, err error) {
	if FlashDataStart()+uintptr(off)+uintptr(len(p)) > FlashDataEnd() {
		return 0, errFlashCannotReadPastEOF
	}
	data := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(FlashDataStart()), off)), len(p))
	copy(p, data)
	return len(p), nil
}

// WriteAt writes the given number of bytes to the block device.
// The destination must already be erased.
func (f flashBlockDevice) WriteAt(p []byte, off int64) (n int, err error) {
	return f.writeAt(p, off)
}

// Size returns the number of bytes in this block device.
func (f flashBlockDevice) Size() int64 {
	return int64(FlashDataEnd() - FlashDataStart())
}

const writeBlockSize = 4

// WriteBlockSize returns the minimum write granularity.
func (f flashBlockDevice) WriteBlockSize() int64 {
	return writeBlockSize
}

const eraseBlockSizeValue = 1 << 12 // 4 KiB sectors

func eraseBlockSize() int64 {
	return eraseBlockSizeValue
}

// EraseBlockSize returns the smallest erasable unit.
func (f flashBlockDevice) EraseBlockSize() int64 {
	return eraseBlockSize()
}

// EraseBlocks erases the given number of blocks.
func (f flashBlockDevice) EraseBlocks(start, length int64) error {
	return f.eraseBlocks(start, length)
}

// flashDROMStart is the base of the DROM bus window on P4.
const flashDROMStart = uintptr(0x44000000)

// readAddress converts a flash-data offset to the memory-mapped DROM address.
func readAddress(off int64) uintptr {
	return FlashDataStart() + uintptr(off)
}

// writeAddress converts a flash-data offset to the physical flash byte address.
func writeAddress(off int64) uint32 {
	return uint32(readAddress(off) - flashDROMStart)
}

func (f flashBlockDevice) writeAt(p []byte, off int64) (n int, err error) {
	if readAddress(off)+uintptr(len(p)) > FlashDataEnd() {
		return 0, errFlashCannotWritePastEOF
	}

	address := writeAddress(off)
	padded := flashPad(p, int(f.WriteBlockSize()))

	state := interrupt.Disable()
	defer interrupt.Restore(state)

	C.esp_rom_spiflash_unlock()
	res := C.esp_rom_spiflash_write(
		C.uint32_t(address),
		(*C.uint32_t)(unsafe.Pointer(&padded[0])),
		C.uint32_t(len(padded)),
	)
	C.Cache_Invalidate_All()
	if res != 0 {
		return 0, errFlashCannotWriteData
	}
	return len(padded), nil
}

func (f flashBlockDevice) eraseBlocks(start, length int64) error {
	startAddr := writeAddress(start * f.EraseBlockSize())
	if uintptr(startAddr)+flashDROMStart+uintptr(length*f.EraseBlockSize()) > FlashDataEnd() {
		return errFlashCannotErasePastEOF
	}

	state := interrupt.Disable()
	defer interrupt.Restore(state)

	C.esp_rom_spiflash_unlock()
	sector := startAddr / uint32(f.EraseBlockSize())

	for i := int64(0); i < length; i++ {
		res := C.esp_rom_spiflash_erase_sector(C.uint32_t(sector + uint32(i)))
		C.Cache_Invalidate_All()
		if res != 0 {
			return errFlashCannotErasePage
		}
	}
	return nil
}
