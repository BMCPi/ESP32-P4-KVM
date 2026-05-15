//go:build esp32p4

package machine

// SPI driver for the ESP32-P4 target.
// Uses SPI2 (GPSPI2) with GPIO matrix routing.
// GPIO signal indices from: soc/gpio_sig_map.h for ESP32-P4.

import (
	"device/esp"
	"errors"
	"runtime/volatile"
	"unsafe"
)

// SPI2 GPIO matrix signal indices for ESP32-P4.
const (
	SPI2CKInIDX  = uint32(53)
	SPI2CKOutIDX = uint32(53)
	SPI2QInIDX   = uint32(54) // MISO
	SPI2QOutIDX  = uint32(54)
	SPI2DInIDX   = uint32(55) // MOSI
	SPI2DOutIDX  = uint32(55)
	SPI2CSInIDX  = uint32(62)
	SPI2CSOutIDX = uint32(62)
)

const pplClockFreq = 80_000_000

// SPI mode constants (CPOL/CPHA combinations).
const (
	Mode0 = 0 // CPOL=0 CPHA=0
	Mode1 = 1 // CPOL=0 CPHA=1
	Mode2 = 2 // CPOL=1 CPHA=0
	Mode3 = 3 // CPOL=1 CPHA=1
)

var (
	ErrInvalidSPIBus  = errors.New("machine: SPI bus is invalid")
	ErrInvalidSPIMode = errors.New("machine: SPI mode is invalid")
)

// SPIConfig holds SPI bus configuration.
type SPIConfig struct {
	Frequency uint32
	SCK       Pin
	SDO       Pin  // MOSI
	SDI       Pin  // MISO
	CS        Pin
	LSBFirst  bool
	Mode      uint8
}

// SPI represents an SPI bus backed by the ESP32-P4 SPI2 (GPSPI2) peripheral.
type SPI struct {
	Bus *esp.SPI2_Type
}

var (
	SPI2 = &SPI{esp.SPI2}
	SPI0 = SPI2
	SPI1 = SPI2
)

func (spi *SPI) Configure(config SPIConfig) error {
	if spi.Bus != esp.SPI2 {
		return ErrInvalidSPIBus
	}

	// Reset and enable SPI2 clock via HP_SYS_CLKRST (replaces SYSTEM on S3/C3).
	esp.HP_SYS_CLKRST.SetHP_RST_EN2_REG_RST_EN_SPI2(1)
	esp.HP_SYS_CLKRST.SetSOC_CLK_CTRL1_REG_GPSPI2_SYS_CLK_EN(1)
	esp.HP_SYS_CLKRST.SetSOC_CLK_CTRL2_REG_GPSPI2_APB_CLK_EN(1)
	esp.HP_SYS_CLKRST.SetHP_RST_EN2_REG_RST_EN_SPI2(0)

	// Clear all SPI2 registers to a known state.
	spi.Bus.SPI_SLAVE.Set(0)
	spi.Bus.SPI_MISC.Set(0)
	spi.Bus.SPI_USER.Set(0)
	spi.Bus.SPI_USER1.Set(0)
	spi.Bus.SPI_CTRL.Set(0)
	spi.Bus.SPI_CLK_GATE.Set(0)
	spi.Bus.SPI_DMA_CONF.Set(0)
	spi.Bus.SetSPI_DMA_CONF_SPI_RX_AFIFO_RST(1)
	spi.Bus.SetSPI_DMA_CONF_SPI_BUF_AFIFO_RST(1)
	spi.Bus.SPI_CLOCK.Set(0)

	// Enable SPI2 internal clock.
	spi.Bus.SetSPI_CLK_GATE_SPI_CLK_EN(1)
	spi.Bus.SetSPI_CLK_GATE_SPI_MST_CLK_SEL(1)
	spi.Bus.SetSPI_CLK_GATE_SPI_MST_CLK_ACTIVE(1)

	// DMA slave segment trans clear (required for master mode).
	spi.Bus.SetSPI_DMA_CONF_SPI_SLV_TX_SEG_TRANS_CLR_EN(1)
	spi.Bus.SetSPI_DMA_CONF_SPI_SLV_RX_SEG_TRANS_CLR_EN(1)
	spi.Bus.SetSPI_DMA_CONF_SPI_DMA_SLV_SEG_TRANS_EN(0)

	// Enable full-duplex mode with both MOSI and MISO.
	spi.Bus.SetSPI_USER_SPI_USR_MOSI(1)
	spi.Bus.SetSPI_USER_SPI_USR_MISO(1)
	spi.Bus.SetSPI_USER_SPI_DOUTDIN(1)

	// Clock polarity/phase (SPI mode).
	switch config.Mode {
	case Mode0:
		spi.Bus.SetSPI_MISC_SPI_CK_IDLE_EDGE(0)
		spi.Bus.SetSPI_USER_SPI_CK_OUT_EDGE(0)
	case Mode1:
		spi.Bus.SetSPI_MISC_SPI_CK_IDLE_EDGE(0)
		spi.Bus.SetSPI_USER_SPI_CK_OUT_EDGE(1)
	case Mode2:
		spi.Bus.SetSPI_MISC_SPI_CK_IDLE_EDGE(1)
		spi.Bus.SetSPI_USER_SPI_CK_OUT_EDGE(1)
	case Mode3:
		spi.Bus.SetSPI_MISC_SPI_CK_IDLE_EDGE(1)
		spi.Bus.SetSPI_USER_SPI_CK_OUT_EDGE(0)
	default:
		return ErrInvalidSPIMode
	}

	// Bit order.
	if config.LSBFirst {
		spi.Bus.SetSPI_CTRL_SPI_WR_BIT_ORDER(1)
		spi.Bus.SetSPI_CTRL_SPI_RD_BIT_ORDER(1)
	} else {
		spi.Bus.SetSPI_CTRL_SPI_WR_BIT_ORDER(0)
		spi.Bus.SetSPI_CTRL_SPI_RD_BIT_ORDER(0)
	}

	// Clock frequency.
	spi.Bus.SPI_CLOCK.Set(freqToClockDiv(config.Frequency))

	if config.CS == 0 {
		config.CS = NoPin
	}

	// Route MISO, MOSI, SCK, CS through the GPIO matrix.
	config.SDI.Configure(PinConfig{Mode: PinInput})
	inFunc(SPI2QInIDX).Set(esp.GPIO_FUNC_IN_SEL_CFG_SEL | uint32(config.SDI))

	config.SDO.Configure(PinConfig{Mode: PinOutput})
	config.SDO.outFunc().Set(SPI2DOutIDX)

	config.SCK.Configure(PinConfig{Mode: PinOutput})
	config.SCK.outFunc().Set(SPI2CKOutIDX)

	if config.CS != NoPin {
		config.CS.Configure(PinConfig{Mode: PinOutput})
		config.CS.outFunc().Set(SPI2CSOutIDX)
	}

	return nil
}

// Transfer sends and receives a single byte over SPI.
func (spi *SPI) Transfer(w byte) (byte, error) {
	spi.Bus.SetSPI_MS_DLEN_SPI_MS_DATA_BITLEN(7)
	spi.Bus.SetSPI_W0(uint32(w))
	spi.Bus.SetSPI_CMD_SPI_UPDATE(1)
	for spi.Bus.GetSPI_CMD_SPI_UPDATE() != 0 {
	}
	spi.Bus.SetSPI_CMD_SPI_USR(1)
	for spi.Bus.GetSPI_CMD_SPI_USR() != 0 {
	}
	return byte(spi.Bus.GetSPI_W0()), nil
}

// Tx sends/receives data in chunks of up to 64 bytes using the SPI2 FIFO.
func (spi *SPI) Tx(w, r []byte) error {
	toTransfer := len(w)
	if len(r) > toTransfer {
		toTransfer = len(r)
	}
	for toTransfer > 0 {
		chunkSize := toTransfer
		if chunkSize > 64 {
			chunkSize = 64
		}
		// SPI_W0 is at offset 0x98; cast to a 16-word array for bulk access.
		transferWords := (*[16]volatile.Register32)(unsafe.Pointer(uintptr(unsafe.Pointer(&spi.Bus.SPI_W0))))
		spiTxFillBuffer(transferWords, w)

		spi.Bus.SetSPI_MS_DLEN_SPI_MS_DATA_BITLEN(uint32(chunkSize)*8 - 1)
		spi.Bus.SetSPI_CMD_SPI_UPDATE(1)
		for spi.Bus.GetSPI_CMD_SPI_UPDATE() != 0 {
		}
		spi.Bus.SetSPI_CMD_SPI_USR(1)
		for spi.Bus.GetSPI_CMD_SPI_USR() != 0 {
		}

		rxSize := 64
		if rxSize > len(r) {
			rxSize = len(r)
		}
		for i := 0; i < rxSize; i++ {
			r[i] = byte(transferWords[i/4].Get() >> ((i % 4) * 8))
		}

		if len(w) < chunkSize {
			w = nil
		} else {
			w = w[chunkSize:]
		}
		if len(r) < chunkSize {
			r = nil
		} else {
			r = r[chunkSize:]
		}
		toTransfer -= chunkSize
	}
	return nil
}

// freqToClockDiv computes the SPI bus clock divider register value.
// SPI2 on ESP32-P4 is clocked from APB (80 MHz).
func freqToClockDiv(hz uint32) uint32 {
	if hz >= pplClockFreq { // maximum: bypass divider
		return 1 << 31
	}
	if hz < (pplClockFreq / (16 * 64)) { // minimum frequency
		return 15<<18 | 63<<12 | 31<<6 | 63
	}
	var bestPre, bestN, bestErr uint32
	bestErr = ^uint32(0)
	for pre := uint32(0); pre <= 15; pre++ {
		preDivisor := pplClockFreq / (pre + 1)
		for n := uint32(2); n <= 64; n++ {
			clk := preDivisor / n
			diff := hz - clk
			if clk > hz {
				diff = clk - hz
			}
			if diff < bestErr {
				bestPre = pre
				bestN = n
				bestErr = diff
			}
		}
	}
	n := bestN
	h := n / 2
	return bestPre<<18 | (n-1)<<12 | (h-1)<<6 | (n - 1)
}

// spiTxFillBuffer fills the 16-word SPI TX FIFO buffer from src,
// zero-padding if src is shorter than 64 bytes.
func spiTxFillBuffer(buf *[16]volatile.Register32, src []byte) {
	for i := range buf {
		var w uint32
		j := i * 4
		if j < len(src) {
			w |= uint32(src[j])
		}
		if j+1 < len(src) {
			w |= uint32(src[j+1]) << 8
		}
		if j+2 < len(src) {
			w |= uint32(src[j+2]) << 16
		}
		if j+3 < len(src) {
			w |= uint32(src[j+3]) << 24
		}
		buf[i].Set(w)
	}
}
