//go:build esp32p4

// DWC2 USB OTG HS device driver for ESP32-P4.
//
// Hardware: Synopsys DWC_OTG v4.30a High-Speed instance.
// Base address: USB_DWC_HS = 0x50000000 (4-pin USB OTG header on Waveshare ESP32-P4-ETH).
// USB_DWC_FS = 0x50040000 (USB-C power/programming port) – not used here.
//
// This driver operates in slave mode (no DMA), Full Speed (64-byte bulk MPS).
// USB events are processed by a polling goroutine since TinyGo does not yet
// route the ESP32-P4 HP interrupt matrix (INTMTX at 0x500D6000) for USB_OTG
// (peripheral source 93).
//
// Implements the machine-package USB surface consumed by machine/usb/msc:
//   - ConfigureUSBEndpoint, AckUsbOutTransfer, SendUSBInPacket, SendZlp
//   - USBDev.InitEndpointComplete, SetStallEPIn/Out, ClearStallEPIn/Out
//   - USBCDC stub (serial console uses UART on ESP32-P4)

package machine

import (
	"device/esp"
	"machine/usb"
	"machine/usb/descriptor"
	"runtime/volatile"
	"unsafe"
)

// ============================================================
// DWC2 Register Offsets
// ============================================================

const usbDWCBase = uintptr(0x50000000) // USB_DWC_HS

// Global registers
const (
	dwcGAHBCFG   = uintptr(0x0008)
	dwcGUSBCFG   = uintptr(0x000C)
	dwcGRSTCTL   = uintptr(0x0010)
	dwcGINTSTS   = uintptr(0x0014)
	dwcGINTMSK   = uintptr(0x0018)
	dwcGRXSTSP   = uintptr(0x0020)
	dwcGRXFSIZ   = uintptr(0x0024)
	dwcGNPTXFSIZ = uintptr(0x0028)
	dwcDIEPTXFI0 = uintptr(0x0104) // TX FIFO 1 (used for all non-EP0 IN EPs)
)

// Device registers
const (
	dwcDCFG       = uintptr(0x0800)
	dwcDCTL       = uintptr(0x0804)
	dwcDSTS       = uintptr(0x0808)
	dwcDIEPMSK    = uintptr(0x0810)
	dwcDOEPMSK    = uintptr(0x0814)
	dwcDAINT      = uintptr(0x0818)
	dwcDAINTMSK   = uintptr(0x081C)
	dwcDIEPEMPMSK = uintptr(0x0834)
)

// EP0 IN / OUT special registers
const (
	dwcDIEPCTL0  = uintptr(0x0900)
	dwcDIEPINT0  = uintptr(0x0908)
	dwcDIEPTSIZ0 = uintptr(0x0910)
	dwcDTXFSTS0  = uintptr(0x0918)

	dwcDOEPCTL0  = uintptr(0x0B00)
	dwcDOEPINT0  = uintptr(0x0B08)
	dwcDOEPTSIZ0 = uintptr(0x0B10)
)

// Non-EP0 IN/OUT endpoint register banks (EP 1-15, stride 0x20 each)
const (
	dwcINEPBase    = uintptr(0x0920)
	dwcINEPStride  = uintptr(0x0020)
	dwcOUTEPBase   = uintptr(0x0B20)
	dwcOUTEPStride = uintptr(0x0020)

	// Offsets within each EP block
	dwcEPCTL    = uintptr(0x00)
	dwcEPINT    = uintptr(0x08)
	dwcEPTSIZ   = uintptr(0x10)
	dwcINTXFSTS = uintptr(0x18) // IN EPs only: TX FIFO status
)

// ============================================================
// DWC2 Register Bit Definitions
// ============================================================

// GAHBCFG
const dwcGAHBCFG_GLBINTRMSK = uint32(1 << 0)

// GRSTCTL
const (
	dwcGRSTCTL_CSFTRST     = uint32(1 << 0)
	dwcGRSTCTL_CSFTRSTDONE = uint32(1 << 29) // DWC v4.30a: reset-done flag
	dwcGRSTCTL_AHBIDLE     = uint32(1 << 31)
	dwcGRSTCTL_TXFFLSH     = uint32(1 << 5)
	dwcGRSTCTL_RXFFLSH     = uint32(1 << 4)
)

// GUSBCFG
const dwcGUSBCFG_FDMOD = uint32(1 << 30)

// GINTSTS / GINTMSK
const (
	dwcGINT_RXFLVL   = uint32(1 << 4)
	dwcGINT_USBSUSP  = uint32(1 << 11)
	dwcGINT_USBRST   = uint32(1 << 12)
	dwcGINT_ENUMDONE = uint32(1 << 13)
	dwcGINT_IEPINT   = uint32(1 << 18)
	dwcGINT_OEPINT   = uint32(1 << 19)
	dwcGINT_RESETDET = uint32(1 << 23)
)

// DCFG
const (
	dwcDCFG_DEVSPD_FS_HSPHY = uint32(1) // Full Speed using HS PHY (UTMI+)
	dwcDCFG_DEVADDR_SHIFT   = 4         // bits 10:4
	dwcDCFG_DEVADDR_MASK    = uint32(0x7F << 4)
)

// DCTL
const (
	dwcDCTL_SFTDISCON = uint32(1 << 1)
	dwcDCTL_CGNPINNAK = uint32(1 << 8) // Clear global non-periodic IN NAK
)

// DIEPCTL / DOEPCTL shared bits
const (
	dwcEPCTL_EPENA    = uint32(1 << 31)
	dwcEPCTL_EPDIS    = uint32(1 << 30)
	dwcEPCTL_CNAK     = uint32(1 << 26)
	dwcEPCTL_SNAK     = uint32(1 << 27)
	dwcEPCTL_STALL    = uint32(1 << 21)
	dwcEPCTL_USBACTEP = uint32(1 << 15)
	dwcEPCTL_SetD0PID = uint32(1 << 28) // DATA0 PID reset
	// EP type bits 19:18
	dwcEPCTL_TYPE_CTRL = uint32(0 << 18)
	dwcEPCTL_TYPE_BULK = uint32(2 << 18)
	dwcEPCTL_TYPE_INTR = uint32(3 << 18)
	// TX FIFO number bits 25:22 (IN EPs only)
	dwcEPCTL_TXFNUM_SHIFT = 22
)

// GRXSTSP packet status values (device mode, bits 20:17)
const (
	dwcPKTSTS_OUT_NAK    = uint32(1)
	dwcPKTSTS_OUT_DATA   = uint32(2)
	dwcPKTSTS_OUT_DONE   = uint32(3)
	dwcPKTSTS_SETUP_DONE = uint32(4)
	dwcPKTSTS_SETUP_PKT  = uint32(6)
)

// GRXSTSP field masks / shifts
const (
	dwcRXSTS_EPNUM_MASK   = uint32(0xF)
	dwcRXSTS_BCNT_MASK    = uint32(0x7FF << 4)
	dwcRXSTS_BCNT_SHIFT   = 4
	dwcRXSTS_PKTSTS_MASK  = uint32(0xF << 17)
	dwcRXSTS_PKTSTS_SHIFT = 17
)

// FIFO sizes (32-bit words)
const (
	dwcRXFIFO_WORDS   = uint32(256)                         // 1 KiB shared RX FIFO
	dwcNPTXFIFO_WORDS = uint32(64)                          // 256 B non-periodic TX (EP0 IN)
	dwcTXFIFO1_WORDS  = uint32(256)                         // 1 KiB bulk IN TX FIFO (FIFO 1)
	dwcTXFIFO1_START  = dwcRXFIFO_WORDS + dwcNPTXFIFO_WORDS // word 320
)

// Full Speed bulk max packet size
const usbBulkMPS = 64

// ============================================================
// USB Device State
// ============================================================

// NumberOfUSBEndpoints is the number of USB endpoints on this platform.
const NumberOfUSBEndpoints = 8

// endPoints maps endpoint index → type+direction (used at SET_CONFIGURATION).
var endPoints = []uint32{
	usb.CONTROL_ENDPOINT:  usb.ENDPOINT_TYPE_CONTROL,
	usb.CDC_ENDPOINT_ACM:  usb.ENDPOINT_TYPE_DISABLE,
	usb.CDC_ENDPOINT_OUT:  usb.ENDPOINT_TYPE_DISABLE,
	usb.CDC_ENDPOINT_IN:   usb.ENDPOINT_TYPE_DISABLE,
	usb.HID_ENDPOINT_IN:   usb.ENDPOINT_TYPE_DISABLE,
	usb.HID_ENDPOINT_OUT:  usb.ENDPOINT_TYPE_DISABLE,
	usb.MIDI_ENDPOINT_IN:  usb.ENDPOINT_TYPE_DISABLE,
	usb.MIDI_ENDPOINT_OUT: usb.ENDPOINT_TYPE_DISABLE,
}

// Endpoint handler tables.
var (
	usbTxHandler    [NumberOfUSBEndpoints]func()
	usbRxHandler    [NumberOfUSBEndpoints]func([]byte) bool
	usbSetupHandler [usb.NumberOfInterfaces]func(usb.Setup) bool
	usbStallHandler [NumberOfUSBEndpoints]func(usb.Setup) bool
)

var usbDescriptor descriptor.Descriptor

var (
	isEndpointHalt        = false
	isRemoteWakeUpEnabled = false
	usbConfiguration      uint8
	usbSetInterface       uint8
)

// USB String Descriptor 0 – language IDs (English US 0x0409)
var usbLangInfo = [4]byte{0x04, 0x03, 0x09, 0x04}

// Descriptor send / receive scratch buffers.
//
//go:align 4
var udd_ep_control_cache_buffer [256]uint8

//go:align 4
var udd_ep_in_cache_buffer [NumberOfUSBEndpoints][64]uint8

//go:align 4
var udd_ep_out_cache_buffer [NumberOfUSBEndpoints][64]uint8

// usb_trans_buffer is used when building string descriptors.
var usb_trans_buffer [255]uint8

// ep0SetupBuffer holds the 8-byte SETUP packet read from the RX FIFO.
var ep0SetupBuffer [8]byte

// usbRxBuffer holds received OUT data per endpoint (one FS packet each).
//
//go:align 4
var usbRxBuffer [NumberOfUSBEndpoints][usbBulkMPS]uint8

// usbRxBCNT tracks the actual byte count of the last OUT packet per endpoint.
var usbRxBCNT [NumberOfUSBEndpoints]uint32

// ep0SendPending supports multi-packet EP0 IN transfers (chunked at 64 bytes).
var ep0SendPending struct {
	data   []byte
	offset int
}

// ============================================================
// USBDevice
// ============================================================

// USBDevice holds USB device state.
type USBDevice struct {
	initcomplete         bool
	InitEndpointComplete bool
}

// USBDev is the singleton USB device instance.
var USBDev = &USBDevice{}

// ============================================================
// USBCDC stub
// Serial console on ESP32-P4 uses UART; this stub satisfies the Serialer
// interface so code referencing machine.USBCDC does not cause linker errors.
// ============================================================

// Serialer is the interface for a USB CDC serial port.
type Serialer interface {
	WriteByte(c byte) error
	Write(data []byte) (n int, err error)
	Configure(config UARTConfig) error
	Buffered() int
	ReadByte() (byte, error)
	DTR() bool
	RTS() bool
}

// USB_DEVICE is the USBCDC serial stub for ESP32-P4.
type USB_DEVICE struct {
	Buffer *RingBuffer
}

var (
	_USBCDC          = &USB_DEVICE{Buffer: NewRingBuffer()}
	USBCDC  Serialer = _USBCDC
)

func (d *USB_DEVICE) Configure(config UARTConfig) error { return nil }
func (d *USB_DEVICE) WriteByte(c byte) error            { return nil }
func (d *USB_DEVICE) Write(data []byte) (int, error)    { return 0, nil }
func (d *USB_DEVICE) Buffered() int                     { return 0 }
func (d *USB_DEVICE) ReadByte() (byte, error)           { return 0, errNoByte }
func (d *USB_DEVICE) DTR() bool                         { return false }
func (d *USB_DEVICE) RTS() bool                         { return false }

// FlushSerial is a no-op; UART flush is handled separately.
func FlushSerial() {}

// ============================================================
// Low-level DWC2 register access
// ============================================================

func dwcReg(offset uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(usbDWCBase + offset))
}

func dwcInEPReg(ep uint32, off uintptr) *volatile.Register32 {
	if ep == 0 {
		switch off {
		case dwcEPCTL:
			return dwcReg(dwcDIEPCTL0)
		case dwcEPINT:
			return dwcReg(dwcDIEPINT0)
		case dwcEPTSIZ:
			return dwcReg(dwcDIEPTSIZ0)
		case dwcINTXFSTS:
			return dwcReg(dwcDTXFSTS0)
		}
	}
	return dwcReg(dwcINEPBase + uintptr(ep-1)*dwcINEPStride + off)
}

func dwcOutEPReg(ep uint32, off uintptr) *volatile.Register32 {
	if ep == 0 {
		switch off {
		case dwcEPCTL:
			return dwcReg(dwcDOEPCTL0)
		case dwcEPINT:
			return dwcReg(dwcDOEPINT0)
		case dwcEPTSIZ:
			return dwcReg(dwcDOEPTSIZ0)
		}
	}
	return dwcReg(dwcOUTEPBase + uintptr(ep-1)*dwcOUTEPStride + off)
}

// dwcFIFOWrite writes data to the TX FIFO for IN endpoint txfnum.
// FIFO for txfnum n is at usbDWCBase + 0x1000*(n+1):
//   - txfnum 0 → EP0 IN at +0x1000
//   - txfnum 1 → bulk IN at +0x2000
func dwcFIFOWrite(txfnum uint32, data []byte) {
	fifo := (*volatile.Register32)(unsafe.Pointer(usbDWCBase + uintptr(txfnum+1)*0x1000))
	n := len(data)
	words := (n + 3) >> 2
	for i := 0; i < words; i++ {
		base := i << 2
		var w uint32
		if base < n {
			w = uint32(data[base])
		}
		if base+1 < n {
			w |= uint32(data[base+1]) << 8
		}
		if base+2 < n {
			w |= uint32(data[base+2]) << 16
		}
		if base+3 < n {
			w |= uint32(data[base+3]) << 24
		}
		fifo.Set(w)
	}
}

// dwcFIFORead reads one 32-bit word from the shared RX FIFO.
func dwcFIFORead() uint32 {
	return (*volatile.Register32)(unsafe.Pointer(usbDWCBase + uintptr(0x1000))).Get()
}

// ============================================================
// USB Device Configure (hardware init)
// ============================================================

// Configure initialises the DWC2 USB HS controller and starts the USB polling goroutine.
func (dev *USBDevice) Configure(config UARTConfig) {
	if dev.initcomplete {
		return
	}
	dev.initcomplete = true

	// 0. Enable USB_OTG20 system clock and deassert resets via HP_SYS_CLKRST /
	//    LP_AON_CLKRST before touching any DWC2 registers. Without this the AHB
	//    idle poll below spins forever and the watchdog resets the chip.
	esp.HP_SYS_CLKRST.SetSOC_CLK_CTRL1_REG_USB_OTG20_SYS_CLK_EN(1)
	// Deassert OTG20 PHY reset then OTG20 core reset (order matters: PHY first).
	esp.LP_AON_CLKRST.SetLP_AONCLKRST_HP_USB_CLKRST_CTRL1_LP_AONCLKRST_RST_EN_USB_OTG20_PHY(0)
	esp.LP_AON_CLKRST.SetLP_AONCLKRST_HP_USB_CLKRST_CTRL1_LP_AONCLKRST_RST_EN_USB_OTG20(0)
	// Enable the PHY reference clock (source 0 = XTAL, the reset default).
	esp.LP_AON_CLKRST.SetLP_AONCLKRST_HP_USB_CLKRST_CTRL1_LP_AONCLKRST_USB_OTG20_PHYREF_CLK_EN(1)
	// Short stabilisation delay (~5 µs at 360 MHz).
	for i := 0; i < 2000; i++ {
		_ = i
	}

	// 1. Wait for AHB idle before touching the core (max ~50 ms).
	for i := 0; i < 5000000; i++ {
		if dwcReg(dwcGRSTCTL).Get()&dwcGRSTCTL_AHBIDLE != 0 {
			break
		}
	}

	// 2. Core soft reset. DWC v4.30a: wait for CSFTRSTDONE (bit 29), not CSFTRST clearing.
	dwcReg(dwcGRSTCTL).SetBits(dwcGRSTCTL_CSFTRST)
	for i := 0; i < 5000000; i++ {
		if dwcReg(dwcGRSTCTL).Get()&dwcGRSTCTL_CSFTRSTDONE != 0 {
			break
		}
	}
	dwcReg(dwcGRSTCTL).ClearBits(dwcGRSTCTL_CSFTRST)

	// 3. Force device mode (GUSBCFG.FDMOD=1). Delay ≥ 25 ms for PHY mode change.
	dwcReg(dwcGUSBCFG).SetBits(dwcGUSBCFG_FDMOD)
	for i := 0; i < 200000; i++ {
		_ = dwcReg(dwcGINTSTS).Get()
	}

	// 4. Configure FIFO sizes.
	dwcReg(dwcGRXFSIZ).Set(dwcRXFIFO_WORDS)
	dwcReg(dwcGNPTXFSIZ).Set((dwcNPTXFIFO_WORDS << 16) | dwcRXFIFO_WORDS)
	dwcReg(dwcDIEPTXFI0).Set((dwcTXFIFO1_WORDS << 16) | dwcTXFIFO1_START)

	// 5. Flush all TX FIFOs then the RX FIFO.
	dwcReg(dwcGRSTCTL).Set(dwcGRSTCTL_TXFFLSH | (0x10 << 6)) // txfnum=0x10 → flush all
	for i := 0; i < 1000000; i++ {
		if dwcReg(dwcGRSTCTL).Get()&dwcGRSTCTL_TXFFLSH == 0 {
			break
		}
	}
	dwcReg(dwcGRSTCTL).Set(dwcGRSTCTL_RXFFLSH)
	for i := 0; i < 1000000; i++ {
		if dwcReg(dwcGRSTCTL).Get()&dwcGRSTCTL_RXFFLSH == 0 {
			break
		}
	}

	// 6. Device speed: Full Speed using HS PHY.
	dwcReg(dwcDCFG).Set(dwcDCFG_DEVSPD_FS_HSPHY)

	// 7. Enable USB interrupts.
	dwcReg(dwcGINTSTS).Set(0xFFFFFFFF) // clear all pending
	dwcReg(dwcGINTMSK).Set(
		dwcGINT_RXFLVL |
			dwcGINT_USBRST |
			dwcGINT_ENUMDONE |
			dwcGINT_IEPINT |
			dwcGINT_OEPINT |
			dwcGINT_USBSUSP |
			dwcGINT_RESETDET,
	)
	dwcReg(dwcDAINTMSK).Set((1 << 0) | (1 << 16)) // EP0 IN + EP0 OUT
	dwcReg(dwcDIEPMSK).Set(1 << 0)                // XFERCOMPL
	dwcReg(dwcDOEPMSK).Set((1 << 3) | (1 << 0))   // SETUPMSK | XFERCOMPL
	dwcReg(dwcGAHBCFG).SetBits(dwcGAHBCFG_GLBINTRMSK)

	// 8. Connect: clear soft disconnect.
	dwcReg(dwcDCTL).ClearBits(dwcDCTL_SFTDISCON)

	// Start polling goroutine.
	// TODO: replace with ISR once TinyGo maps INTMTX (0x500D6000) for USB_OTG (src 93).
	go usbPollLoop()
}

// initUSB is called by the TinyGo runtime to initialise USB.
func initUSB() {
	USBDev.Configure(UARTConfig{})
}

// usbPollLoop polls GINTSTS and dispatches USB events cooperatively.
func usbPollLoop() {
	for {
		gintsts := dwcReg(dwcGINTSTS).Get()
		if gintsts&dwcReg(dwcGINTMSK).Get() != 0 {
			handleUSBDWCIRQ()
		}
		gosched()
	}
}

// handleUSBDWCIRQ processes pending DWC2 USB interrupts.
func handleUSBDWCIRQ() {
	gintsts := dwcReg(dwcGINTSTS).Get() & dwcReg(dwcGINTMSK).Get()

	if gintsts&dwcGINT_USBRST != 0 {
		dwcReg(dwcGINTSTS).Set(dwcGINT_USBRST)
		handleUSBReset()
	}
	if gintsts&dwcGINT_ENUMDONE != 0 {
		dwcReg(dwcGINTSTS).Set(dwcGINT_ENUMDONE)
		handleUSBEnumDone()
	}
	if gintsts&dwcGINT_RXFLVL != 0 {
		// Mask RXFLVL while draining (avoid re-entry).
		dwcReg(dwcGINTMSK).ClearBits(dwcGINT_RXFLVL)
		handleUSBRxFIFO()
		dwcReg(dwcGINTMSK).SetBits(dwcGINT_RXFLVL)
	}
	if gintsts&dwcGINT_IEPINT != 0 {
		handleUSBInEP()
	}
	if gintsts&dwcGINT_OEPINT != 0 {
		handleUSBOutEP()
	}
	if gintsts&dwcGINT_USBSUSP != 0 {
		dwcReg(dwcGINTSTS).Set(dwcGINT_USBSUSP)
	}
	if gintsts&dwcGINT_RESETDET != 0 {
		dwcReg(dwcGINTSTS).Set(dwcGINT_RESETDET)
	}
}

// handleUSBReset handles a USB bus reset.
func handleUSBReset() {
	dwcReg(dwcGRSTCTL).Set(dwcGRSTCTL_TXFFLSH | (0x10 << 6))
	for i := 0; i < 1000000; i++ {
		if dwcReg(dwcGRSTCTL).Get()&dwcGRSTCTL_TXFFLSH == 0 {
			break
		}
	}
	dwcReg(dwcDAINTMSK).Set((1 << 0) | (1 << 16))
	dwcReg(dwcDIEPMSK).Set(1 << 0)
	dwcReg(dwcDOEPMSK).Set((1 << 3) | (1 << 0))
	// Reset device address.
	dcfg := dwcReg(dwcDCFG).Get()
	dcfg &^= dwcDCFG_DEVADDR_MASK
	dwcReg(dwcDCFG).Set(dcfg)
	armEP0OutForSetup()
	usbConfiguration = 0
	USBDev.InitEndpointComplete = false
}

// handleUSBEnumDone handles enumeration complete.
func handleUSBEnumDone() {
	dwcReg(dwcDIEPCTL0).Set(0) // EP0 IN MPS = 64 (field=0)
	dwcReg(dwcDCTL).SetBits(dwcDCTL_CGNPINNAK)
	armEP0OutForSetup()
}

// armEP0OutForSetup prepares EP0 OUT to receive a SETUP packet.
func armEP0OutForSetup() {
	// DOEPTSIZ0: supcnt=3 (bits 30:29), pktcnt=1 (bit 19), xfersize=24 (3 SETUP packets)
	dwcReg(dwcDOEPTSIZ0).Set((3 << 29) | (1 << 19) | 24)
	dwcReg(dwcDOEPCTL0).SetBits(dwcEPCTL_CNAK | dwcEPCTL_EPENA)
}

// handleUSBRxFIFO drains the shared RX FIFO while RXFLVL is set.
func handleUSBRxFIFO() {
	for dwcReg(dwcGINTSTS).Get()&dwcGINT_RXFLVL != 0 {
		rxsts := dwcReg(dwcGRXSTSP).Get()
		ep := rxsts & dwcRXSTS_EPNUM_MASK
		bcnt := (rxsts & dwcRXSTS_BCNT_MASK) >> dwcRXSTS_BCNT_SHIFT
		pktsts := (rxsts & dwcRXSTS_PKTSTS_MASK) >> dwcRXSTS_PKTSTS_SHIFT

		switch pktsts {
		case dwcPKTSTS_SETUP_PKT:
			// Read 8-byte SETUP packet (2 words).
			w0 := dwcFIFORead()
			w1 := dwcFIFORead()
			ep0SetupBuffer[0] = byte(w0)
			ep0SetupBuffer[1] = byte(w0 >> 8)
			ep0SetupBuffer[2] = byte(w0 >> 16)
			ep0SetupBuffer[3] = byte(w0 >> 24)
			ep0SetupBuffer[4] = byte(w1)
			ep0SetupBuffer[5] = byte(w1 >> 8)
			ep0SetupBuffer[6] = byte(w1 >> 16)
			ep0SetupBuffer[7] = byte(w1 >> 24)

		case dwcPKTSTS_OUT_DATA:
			if ep < uint32(len(usbRxBuffer)) && bcnt > 0 {
				readRxFIFO(ep, bcnt)
			} else if bcnt > 0 {
				discardRxFIFO(bcnt)
			}

		case dwcPKTSTS_SETUP_DONE, dwcPKTSTS_OUT_DONE, dwcPKTSTS_OUT_NAK:
			// No FIFO data; nothing to read.

		default:
			if bcnt > 0 {
				discardRxFIFO(bcnt)
			}
		}
	}
}

// readRxFIFO reads bcnt bytes from the shared RX FIFO into usbRxBuffer[ep].
func readRxFIFO(ep, bcnt uint32) {
	if bcnt > uint32(len(usbRxBuffer[ep])) {
		bcnt = uint32(len(usbRxBuffer[ep]))
	}
	usbRxBCNT[ep] = bcnt
	words := (bcnt + 3) >> 2
	for i := uint32(0); i < words; i++ {
		w := dwcFIFORead()
		off := i << 2
		if off < bcnt {
			usbRxBuffer[ep][off] = byte(w)
		}
		if off+1 < bcnt {
			usbRxBuffer[ep][off+1] = byte(w >> 8)
		}
		if off+2 < bcnt {
			usbRxBuffer[ep][off+2] = byte(w >> 16)
		}
		if off+3 < bcnt {
			usbRxBuffer[ep][off+3] = byte(w >> 24)
		}
	}
}

// discardRxFIFO discards bcnt bytes from the RX FIFO.
func discardRxFIFO(bcnt uint32) {
	words := (bcnt + 3) >> 2
	for i := uint32(0); i < words; i++ {
		_ = dwcFIFORead()
	}
}

// handleUSBInEP handles IN endpoint transfer-complete interrupts.
func handleUSBInEP() {
	daint := dwcReg(dwcDAINT).Get()
	for i := uint32(0); i < NumberOfUSBEndpoints; i++ {
		if daint&(1<<i) == 0 {
			continue
		}
		epint := dwcInEPReg(i, dwcEPINT).Get()

		if epint&(1<<0) != 0 { // XFERCOMPL
			dwcInEPReg(i, dwcEPINT).Set(1 << 0)
			if i == 0 {
				// Continue a multi-packet EP0 IN transfer if one is pending.
				if ep0SendPending.data != nil && ep0SendPending.offset < len(ep0SendPending.data) {
					chunk := ep0SendPending.data[ep0SendPending.offset:]
					if len(chunk) > usb.EndpointPacketSize {
						chunk = chunk[:usb.EndpointPacketSize]
					}
					ep0SendPending.offset += len(chunk)
					dwcSendEP0Packet(chunk)
				} else {
					ep0SendPending.data = nil
					ep0SendPending.offset = 0
				}
			} else {
				if usbTxHandler[i] != nil {
					usbTxHandler[i]()
				}
			}
		}
		if epint&(1<<7) != 0 { // TXFE – TX FIFO empty
			dwcInEPReg(i, dwcEPINT).Set(1 << 7)
			dwcReg(dwcDIEPEMPMSK).ClearBits(1 << i)
		}
	}
}

// handleUSBOutEP handles OUT endpoint interrupts (SETUP complete and XFERCOMPL).
func handleUSBOutEP() {
	daint := dwcReg(dwcDAINT).Get()
	for i := uint32(0); i < NumberOfUSBEndpoints; i++ {
		if daint&(1<<(i+16)) == 0 {
			continue
		}
		epint := dwcOutEPReg(i, dwcEPINT).Get()

		if i == 0 {
			if epint&(1<<3) != 0 { // SETUP complete
				dwcOutEPReg(0, dwcEPINT).Set(1 << 3)
				setup := usb.NewSetup(ep0SetupBuffer[:])
				var ok bool
				if setup.BmRequestType&usb.REQUEST_TYPE == usb.REQUEST_STANDARD {
					ok = handleStandardSetup(setup)
				} else {
					idx := int(setup.WIndex)
					if idx < len(usbSetupHandler) && usbSetupHandler[idx] != nil {
						ok = usbSetupHandler[idx](setup)
					}
				}
				if !ok {
					USBDev.SetStallEPIn(0)
				}
				armEP0OutForSetup()
			}
			if epint&(1<<0) != 0 { // XFERCOMPL
				dwcOutEPReg(0, dwcEPINT).Set(1 << 0)
			}
		} else {
			if epint&(1<<0) != 0 { // XFERCOMPL
				dwcOutEPReg(i, dwcEPINT).Set(1 << 0)
				bcnt := usbRxBCNT[i]
				if bcnt > 0 {
					buf := usbRxBuffer[i][:bcnt]
					if usbRxHandler[i] == nil || usbRxHandler[i](buf) {
						AckUsbOutTransfer(i)
					}
					// If handler returned false (DelayRxHandler), the MSC
					// processTasks goroutine calls AckUsbOutTransfer itself.
				} else {
					AckUsbOutTransfer(i)
				}
			}
		}
	}
}

// ============================================================
// USB Protocol Handling
// ============================================================

func usbVendorID() uint16 {
	if usb.VendorID != 0 {
		return usb.VendorID
	}
	return 0x239A // TinyGo default
}

func usbProductID() uint16 {
	if usb.ProductID != 0 {
		return usb.ProductID
	}
	return 0x0001
}

func usbManufacturer() string {
	if usb.Manufacturer != "" {
		return usb.Manufacturer
	}
	return "TinyGo"
}

func usbProduct() string {
	if usb.Product != "" {
		return usb.Product
	}
	return "TinyGo USB Device"
}

func usbSerial() string { return usb.Serial }

// ConfigureUSBEndpoint registers endpoint handlers and stores the USB descriptor.
// Called by machine/usb/msc (and other class drivers).
func ConfigureUSBEndpoint(desc descriptor.Descriptor, epSettings []usb.EndpointConfig, setup []usb.SetupConfig) {
	usbDescriptor = desc
	for _, ep := range epSettings {
		if ep.IsIn {
			endPoints[ep.Index] = uint32(ep.Type) | usb.EndpointIn
			if ep.TxHandler != nil {
				usbTxHandler[ep.Index] = ep.TxHandler
			}
		} else {
			endPoints[ep.Index] = uint32(ep.Type) | usb.EndpointOut
			if ep.RxHandler != nil {
				h := ep.RxHandler
				usbRxHandler[ep.Index] = func(b []byte) bool {
					h(b)
					return true
				}
			} else if ep.DelayRxHandler != nil {
				usbRxHandler[ep.Index] = ep.DelayRxHandler
			}
		}
		if ep.StallHandler != nil {
			usbStallHandler[ep.Index] = ep.StallHandler
		}
	}
	for _, s := range setup {
		usbSetupHandler[s.Index] = s.Handler
	}
}

// handleStandardSetup processes a USB standard SETUP request.
func handleStandardSetup(setup usb.Setup) bool {
	switch setup.BRequest {
	case usb.GET_STATUS:
		usb_trans_buffer[0] = 0
		usb_trans_buffer[1] = 0
		if setup.BmRequestType != 0 && isEndpointHalt {
			usb_trans_buffer[0] = 1
		}
		sendDescriptorData(usb_trans_buffer[:2], setup.WLength)
		return true

	case usb.CLEAR_FEATURE:
		if setup.WValueL == 1 { // DEVICEREMOTEWAKEUP
			isRemoteWakeUpEnabled = false
		} else if setup.WValueL == 0 { // ENDPOINTHALT
			if idx := int(setup.WIndex & 0x7F); idx < NumberOfUSBEndpoints && usbStallHandler[idx] != nil {
				return usbStallHandler[idx](setup)
			}
			isEndpointHalt = false
		}
		SendZlp()
		return true

	case usb.SET_FEATURE:
		if setup.WValueL == 1 { // DEVICEREMOTEWAKEUP
			isRemoteWakeUpEnabled = true
		} else if setup.WValueL == 0 { // ENDPOINTHALT
			if idx := int(setup.WIndex & 0x7F); idx < NumberOfUSBEndpoints && usbStallHandler[idx] != nil {
				return usbStallHandler[idx](setup)
			}
			isEndpointHalt = true
		}
		SendZlp()
		return true

	case usb.SET_ADDRESS:
		return handleUSBSetAddress(setup)

	case usb.GET_DESCRIPTOR:
		sendDescriptor(setup)
		return true

	case usb.SET_DESCRIPTOR:
		return false

	case usb.GET_CONFIGURATION:
		usb_trans_buffer[0] = usbConfiguration
		sendDescriptorData(usb_trans_buffer[:1], setup.WLength)
		return true

	case usb.SET_CONFIGURATION:
		if setup.BmRequestType&usb.REQUEST_RECIPIENT == usb.REQUEST_DEVICE {
			for i := 1; i < len(endPoints); i++ {
				initEndpoint(uint32(i), endPoints[i])
			}
			usbConfiguration = setup.WValueL
			USBDev.InitEndpointComplete = true
			SendZlp()
			return true
		}
		return false

	case usb.GET_INTERFACE:
		usb_trans_buffer[0] = usbSetInterface
		sendDescriptorData(usb_trans_buffer[:1], setup.WLength)
		return true

	case usb.SET_INTERFACE:
		usbSetInterface = setup.WValueL
		SendZlp()
		return true

	default:
		return true
	}
}

// sendDescriptor sends the descriptor requested in setup to the host.
func sendDescriptor(setup usb.Setup) {
	switch setup.WValueH {
	case descriptor.TypeConfiguration:
		sendDescriptorData(usbDescriptor.Configuration, setup.WLength)
		return

	case descriptor.TypeDevice:
		usbDescriptor.Configure(usbVendorID(), usbProductID())
		sendDescriptorData(usbDescriptor.Device, setup.WLength)
		return

	case descriptor.TypeString:
		switch setup.WValueL {
		case 0:
			sendDescriptorData(usbLangInfo[:], setup.WLength)
		case usb.IPRODUCT:
			sendDescriptorString(usbProduct(), setup.WLength)
		case usb.IMANUFACTURER:
			sendDescriptorString(usbManufacturer(), setup.WLength)
		case usb.ISERIAL:
			serial := usbSerial()
			if len(serial) == 0 {
				SendZlp()
			} else {
				sendDescriptorString(serial, setup.WLength)
			}
		default:
			SendZlp()
		}
		return

	case descriptor.TypeHIDReport:
		if h, ok := usbDescriptor.HID[setup.WIndex]; ok {
			sendDescriptorData(h, setup.WLength)
			return
		}
	}

	SendZlp()
}

// sendDescriptorString converts an ASCII string to a USB string descriptor (UTF-16LE)
// and sends it on EP0.
func sendDescriptorString(data string, maxLen uint16) {
	if maxLen < 2 {
		SendZlp()
		return
	}
	maxEncBytes := len(usb_trans_buffer)
	if int(maxLen) < maxEncBytes {
		maxEncBytes = int(maxLen)
	}
	maxChars := (maxEncBytes - 2) / 2
	if len(data) < maxChars {
		maxChars = len(data)
	}
	buf := usb_trans_buffer[:2+2*maxChars]
	buf[0] = byte(len(buf))
	buf[1] = descriptor.TypeString
	for i := 0; i < maxChars; i++ {
		buf[2+2*i] = byte(data[i])
		buf[2+2*i+1] = 0
	}
	sendUSBPacket(0, buf)
}

// sendDescriptorData sends a descriptor slice, truncated to maxLen bytes, on EP0.
func sendDescriptorData(data []byte, maxLen uint16) {
	n := len(data)
	if int(maxLen) < n {
		n = int(maxLen)
	}
	if n > len(udd_ep_control_cache_buffer) {
		n = len(udd_ep_control_cache_buffer)
	}
	sendUSBPacket(0, data[:n])
}

// handleUSBSetAddress sends a ZLP then updates DCFG.DEVADDR.
func handleUSBSetAddress(setup usb.Setup) bool {
	sendUSBPacket(0, nil) // acknowledge with ZLP
	addr := uint32(setup.WValueL) & 0x7F
	dcfg := dwcReg(dwcDCFG).Get()
	dcfg &^= dwcDCFG_DEVADDR_MASK
	dcfg |= addr << dwcDCFG_DEVADDR_SHIFT
	dwcReg(dwcDCFG).Set(dcfg)
	return true
}

// ============================================================
// USB Endpoint Management
// ============================================================

// initEndpoint configures a DWC2 endpoint for the given type and direction.
// Called from handleStandardSetup on SET_CONFIGURATION.
func initEndpoint(ep, config uint32) {
	if ep == 0 || config == usb.ENDPOINT_TYPE_DISABLE {
		return
	}
	isIN := config&usb.EndpointIn != 0
	epType := config & 0x3

	var dwcType uint32
	switch epType {
	case usb.ENDPOINT_TYPE_BULK:
		dwcType = dwcEPCTL_TYPE_BULK
	case usb.ENDPOINT_TYPE_INTERRUPT:
		dwcType = dwcEPCTL_TYPE_INTR
	default:
		dwcType = dwcEPCTL_TYPE_CTRL
	}

	mps := uint32(usbBulkMPS) // 64 bytes

	if isIN {
		// All non-EP0 IN EPs share TX FIFO 1 (txfnum=1).
		ctrl := dwcEPCTL_USBACTEP | dwcType | (1 << dwcEPCTL_TXFNUM_SHIFT) | mps
		dwcInEPReg(ep, dwcEPCTL).Set(ctrl)
		dwcReg(dwcDAINTMSK).SetBits(1 << ep)
		dwcReg(dwcDIEPMSK).SetBits(1 << 0)
	} else {
		ctrl := dwcEPCTL_USBACTEP | dwcType | mps
		dwcOutEPReg(ep, dwcEPCTL).Set(ctrl)
		dwcReg(dwcDAINTMSK).SetBits(1 << (ep + 16))
		dwcReg(dwcDOEPMSK).SetBits(1 << 0)
		AckUsbOutTransfer(ep) // prime the endpoint to receive the first packet
	}
}

// ============================================================
// Public USB Transfer Functions
// ============================================================

// sendUSBPacket sends data on an IN endpoint.
// EP0 chunks at 64 bytes with ep0SendPending continuation;
// bulk endpoints are set up for a single multi-packet transfer.
func sendUSBPacket(ep uint32, data []byte) {
	count := len(data)
	if ep == 0 {
		sendChunk := count
		if sendChunk > usb.EndpointPacketSize {
			sendChunk = usb.EndpointPacketSize
			ep0SendPending.data = data
			ep0SendPending.offset = sendChunk
		} else {
			ep0SendPending.data = nil
			ep0SendPending.offset = 0
		}
		// DIEPTSIZ0: pktcnt=1 (bit 19), xfersize masked to 7 bits (bits 6:0)
		dwcReg(dwcDIEPTSIZ0).Set((1 << 19) | uint32(sendChunk&0x7F))
		dwcReg(dwcDIEPCTL0).SetBits(dwcEPCTL_CNAK | dwcEPCTL_EPENA)
		if sendChunk > 0 {
			dwcFIFOWrite(0, data[:sendChunk])
		}
	} else {
		pkts := uint32(1)
		if count > 0 {
			pkts = (uint32(count) + uint32(usbBulkMPS) - 1) / uint32(usbBulkMPS)
		}
		// DIEPTSIZn: pktcnt (bits 28:19), xfersize (bits 18:0)
		dwcInEPReg(ep, dwcEPTSIZ).Set((pkts << 19) | uint32(count))
		dwcInEPReg(ep, dwcEPCTL).SetBits(dwcEPCTL_CNAK | dwcEPCTL_EPENA)
		if count > 0 {
			dwcFIFOWrite(1, data) // all bulk IN EPs use FIFO 1
		}
	}
}

// dwcSendEP0Packet sends a single ≤64 byte chunk on EP0 IN (no further splitting).
func dwcSendEP0Packet(data []byte) {
	count := len(data)
	dwcReg(dwcDIEPTSIZ0).Set((1 << 19) | uint32(count&0x7F))
	dwcReg(dwcDIEPCTL0).SetBits(dwcEPCTL_CNAK | dwcEPCTL_EPENA)
	if count > 0 {
		dwcFIFOWrite(0, data)
	}
}

// SendUSBInPacket sends a USB IN packet on the given endpoint.
func SendUSBInPacket(ep uint32, data []byte) bool {
	sendUSBPacket(ep, data)
	return true
}

// AckUsbOutTransfer re-enables an OUT endpoint to receive the next packet.
func AckUsbOutTransfer(ep uint32) {
	ep = ep & 0x7F
	if ep == 0 {
		armEP0OutForSetup()
		return
	}
	// DOEPTSIZn: pktcnt=1, xfersize=usbBulkMPS
	dwcOutEPReg(ep, dwcEPTSIZ).Set((1 << 19) | uint32(usbBulkMPS))
	dwcOutEPReg(ep, dwcEPCTL).SetBits(dwcEPCTL_CNAK | dwcEPCTL_EPENA)
}

// SendZlp sends a Zero-Length Packet on EP0 IN to acknowledge a control transfer.
func SendZlp() {
	sendUSBPacket(0, nil)
}

// ============================================================
// Stall / Clear-Stall
// ============================================================

func (dev *USBDevice) SetStallEPIn(ep uint32) {
	dwcInEPReg(ep&0x7F, dwcEPCTL).SetBits(dwcEPCTL_STALL)
}

func (dev *USBDevice) SetStallEPOut(ep uint32) {
	dwcOutEPReg(ep&0x7F, dwcEPCTL).SetBits(dwcEPCTL_STALL)
}

func (dev *USBDevice) ClearStallEPIn(ep uint32) {
	r := dwcInEPReg(ep&0x7F, dwcEPCTL)
	r.ClearBits(dwcEPCTL_STALL)
	r.SetBits(dwcEPCTL_SetD0PID)
}

func (dev *USBDevice) ClearStallEPOut(ep uint32) {
	r := dwcOutEPReg(ep&0x7F, dwcEPCTL)
	r.ClearBits(dwcEPCTL_STALL)
	r.SetBits(dwcEPCTL_SetD0PID)
}
