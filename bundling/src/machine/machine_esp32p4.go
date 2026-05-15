//go:build esp32p4

package machine

import (
	"device/esp"
	"runtime/volatile"
	"unsafe"
)

// deviceName is the chip name reported by machine.Device.
const deviceName = "ESP32-P4"

// PinMode constants for ESP32-P4.
// These values must match what the application code uses.
// main.go uses PinOutputModeGPOpenDrain = 4, so PinOutputOpenDrain must equal 4.
const (
	PinInput           PinMode = 0
	PinInputPullup     PinMode = 1
	PinInputPulldown   PinMode = 2
	PinOutput          PinMode = 3
	PinOutputOpenDrain PinMode = 4
	PinAnalog          PinMode = 5
)

// ESP32-P4 GPIO pin constants (0–53).
const (
	GPIO0  Pin = 0
	GPIO1  Pin = 1
	GPIO2  Pin = 2
	GPIO3  Pin = 3
	GPIO4  Pin = 4
	GPIO5  Pin = 5
	GPIO6  Pin = 6
	GPIO7  Pin = 7
	GPIO8  Pin = 8
	GPIO9  Pin = 9
	GPIO10 Pin = 10
	GPIO11 Pin = 11
	GPIO12 Pin = 12
	GPIO13 Pin = 13
	GPIO14 Pin = 14
	GPIO15 Pin = 15
	GPIO16 Pin = 16
	GPIO17 Pin = 17
	GPIO18 Pin = 18
	GPIO19 Pin = 19
	GPIO20 Pin = 20
	GPIO21 Pin = 21
	GPIO22 Pin = 22
	GPIO23 Pin = 23
	GPIO24 Pin = 24
	GPIO25 Pin = 25
	GPIO26 Pin = 26
	GPIO27 Pin = 27
	GPIO28 Pin = 28
	GPIO29 Pin = 29
	GPIO30 Pin = 30
	GPIO31 Pin = 31
	GPIO32 Pin = 32
	GPIO33 Pin = 33
	GPIO34 Pin = 34
	GPIO35 Pin = 35
	GPIO36 Pin = 36
	GPIO37 Pin = 37
	GPIO38 Pin = 38
	GPIO39 Pin = 39
	GPIO40 Pin = 40
	GPIO41 Pin = 41
	GPIO42 Pin = 42
	GPIO43 Pin = 43
	GPIO44 Pin = 44
	GPIO45 Pin = 45
	GPIO46 Pin = 46
	GPIO47 Pin = 47
	GPIO48 Pin = 48
	GPIO49 Pin = 49
	GPIO50 Pin = 50
	GPIO51 Pin = 51
	GPIO52 Pin = 52
	GPIO53 Pin = 53
)

// UART is a minimal stub required by machine/uart.go.
// ESP32-P4 uses USB CDC for serial output (see machine_esp32p4_usb.go).
type UART struct {
	Buffer *RingBuffer
}

var (
	UART0       = &_UART0
	_UART0      = UART{Buffer: NewRingBuffer()}
	UART1       = &_UART1
	_UART1      = UART{Buffer: NewRingBuffer()}
	DefaultUART = UART0
)

func (uart *UART) Configure(config UARTConfig) error { return nil }
func (uart *UART) writeByte(c byte) error            { return nil }
func (uart *UART) flush()                            {}

// IO_MUX register bit positions (same layout for all GPIOn pads).
const (
	iomuxFunWPD     = uint32(1 << 7)  // pull-down enable
	iomuxFunWPU     = uint32(1 << 8)  // pull-up enable
	iomuxFunIE      = uint32(1 << 9)  // input enable
	iomuxMcuSel1    = uint32(1 << 12) // MCU_SEL bit0: GPIO function = 1
	iomuxMcuSelMask = uint32(0x7 << 12)
)

// GPIO PINn register bit positions.
const pinPadDriver = uint32(1 << 2) // open-drain mode

// io_mux_reg returns a pointer to the IO_MUX GPIOn register for pin p.
// IO_MUX.GPIO0 is at IO_MUX base+0x4, GPIOn at base+0x4+n*4.
func (p Pin) io_mux_reg() *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Add(unsafe.Pointer(&esp.IO_MUX.GPIO0), uintptr(p)*4))
}

// pin_reg returns a pointer to the GPIO PINn register for pin p.
// GPIO.PIN0 is at GPIO base+0x74, PINn at base+0x74+n*4.
func (p Pin) pin_reg() *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Add(unsafe.Pointer(&esp.GPIO.PIN0), uintptr(p)*4))
}

// Configure sets the direction and pull-up/down of the pin.
func (p Pin) Configure(config PinConfig) {
	// Configure IO_MUX: select GPIO function, set IE/WPU/WPD.
	mux := p.io_mux_reg()
	val := volatile.LoadUint32(&mux.Reg)
	val &^= iomuxMcuSelMask | iomuxFunIE | iomuxFunWPU | iomuxFunWPD
	val |= iomuxMcuSel1 // GPIO function

	switch config.Mode {
	case PinInput:
		val |= iomuxFunIE
	case PinInputPullup:
		val |= iomuxFunIE | iomuxFunWPU
	case PinInputPulldown:
		val |= iomuxFunIE | iomuxFunWPD
	}
	volatile.StoreUint32(&mux.Reg, val)

	// Configure open-drain mode via GPIO PINn.PAD_DRIVER.
	pinReg := p.pin_reg()
	if config.Mode == PinOutputOpenDrain {
		pinReg.SetBits(pinPadDriver)
	} else {
		pinReg.ClearBits(pinPadDriver)
	}

	// Enable/disable output.
	bit := uint32(1) << (p & 31)
	switch config.Mode {
	case PinInput, PinInputPullup, PinInputPulldown, PinAnalog:
		if p < 32 {
			esp.GPIO.ENABLE_W1TC.Set(bit)
		} else {
			esp.GPIO.ENABLE1_W1TC.Set(bit)
		}
	default:
		if p < 32 {
			esp.GPIO.ENABLE_W1TS.Set(bit)
		} else {
			esp.GPIO.ENABLE1_W1TS.Set(bit)
		}
	}
}

// Set drives the pin high (true) or low (false).
func (p Pin) Set(value bool) {
	bit := uint32(1) << (p & 31)
	if value {
		if p < 32 {
			esp.GPIO.OUT_W1TS.Set(bit)
		} else {
			esp.GPIO.OUT1_W1TS.Set(bit)
		}
	} else {
		if p < 32 {
			esp.GPIO.OUT_W1TC.Set(bit)
		} else {
			esp.GPIO.OUT1_W1TC.Set(bit)
		}
	}
}

// Get returns the current logical level of the pin.
func (p Pin) Get() bool {
	if p < 32 {
		return esp.GPIO.IN.Get()&(1<<(p&31)) != 0
	}
	return esp.GPIO.IN1.Get()&(1<<(p&31)) != 0
}

// outFunc returns the FUNC{p}_OUT_SEL_CFG register for routing this GPIO pin
// to a peripheral output signal via the GPIO matrix.
// FUNC0_OUT_SEL_CFG is at GPIO base+0x558; pin n is at base+0x558+n*4.
func (p Pin) outFunc() *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Add(unsafe.Pointer(&esp.GPIO.FUNC0_OUT_SEL_CFG), uintptr(p)*4))
}

// pinReg returns the GPIO PINn register for pin p.
func (p Pin) pinReg() *volatile.Register32 {
	return p.pin_reg()
}

// inFunc returns the GPIO FUNC{signal}_IN_SEL_CFG register for routing an
// input peripheral signal to a GPIO pad via the GPIO matrix.
// On P4 HP GPIO, the first register is FUNC1_IN_SEL_CFG at 0x15C (signal 1).
// For signal index n (n >= 1): register = FUNC1_IN_SEL_CFG + (n-1)*4.
func inFunc(signal uint32) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Add(unsafe.Pointer(&esp.GPIO.FUNC1_IN_SEL_CFG), uintptr(signal-1)*4))
}

// configure sets a pin to GPIO function and routes it through the GPIO matrix
// to peripheral signal fn. For output modes, the output path is used; for
// input modes, the input path is used. fn==256 means no peripheral routing.
func (p Pin) configure(config PinConfig, fn uint32) {
	p.Configure(config)
	if fn == 256 { // SIG_GPIO_OUT_IDX — no peripheral routing
		return
	}
	if config.Mode == PinOutput || config.Mode == PinOutputOpenDrain {
		p.outFunc().Set(fn)
	} else {
		inFunc(fn).Set(esp.GPIO_FUNC_IN_SEL_CFG_SEL | uint32(p)<<esp.GPIO_FUNC_IN_SEL_CFG_IN_SEL_Pos)
	}
}
