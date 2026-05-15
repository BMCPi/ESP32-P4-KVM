//go:build esp32p4

package interrupt

import (
	"device/riscv"
	"errors"
)

//go:extern tinygo_saved_ra
var tinygo_saved_ra uintptr

// Enable registers the CPU interrupt channel.
// The ESP32-P4 RISC-V core enables individual CPU interrupt channels
// via the machine interrupt-enable (mie) CSR.
// The INTERRUPT_CORE0 peripheral maps peripheral sources to these channels.
func (i Interrupt) Enable() error {
	if i.num < 1 || i.num > 31 {
		return errors.New("interrupt for ESP32-P4 must be in range of 1 through 31")
	}
	mask := riscv.DisableInterrupts()
	defer riscv.EnableInterrupts(mask)
	riscv.MIE.SetBits(1 << uint(i.num))
	riscv.Asm("fence")
	return nil
}

// Adding pseudo function calls replaced by the compiler with actual
// functions registered through interrupt.New.
//
//go:linkname callHandlers runtime/interrupt.callHandlers
func callHandlers(num int)

//go:linkname signalInterrupt runtime.signalInterrupt
func signalInterrupt()

const (
	IRQNUM_1 = 1 + iota
	IRQNUM_2
	IRQNUM_3
	IRQNUM_4
	IRQNUM_5
	IRQNUM_6
	IRQNUM_7
	IRQNUM_8
	IRQNUM_9
	IRQNUM_10
	IRQNUM_11
	IRQNUM_12
	IRQNUM_13
	IRQNUM_14
	IRQNUM_15
	IRQNUM_16
	IRQNUM_17
	IRQNUM_18
	IRQNUM_19
	IRQNUM_20
	IRQNUM_21
	IRQNUM_22
	IRQNUM_23
	IRQNUM_24
	IRQNUM_25
	IRQNUM_26
	IRQNUM_27
	IRQNUM_28
	IRQNUM_29
	IRQNUM_30
	IRQNUM_31
)

//go:inline
func callHandler(n int) {
	switch n {
	case IRQNUM_1:
		callHandlers(IRQNUM_1)
	case IRQNUM_2:
		callHandlers(IRQNUM_2)
	case IRQNUM_3:
		callHandlers(IRQNUM_3)
	case IRQNUM_4:
		callHandlers(IRQNUM_4)
	case IRQNUM_5:
		callHandlers(IRQNUM_5)
	case IRQNUM_6:
		callHandlers(IRQNUM_6)
	case IRQNUM_7:
		callHandlers(IRQNUM_7)
	case IRQNUM_8:
		callHandlers(IRQNUM_8)
	case IRQNUM_9:
		callHandlers(IRQNUM_9)
	case IRQNUM_10:
		callHandlers(IRQNUM_10)
	case IRQNUM_11:
		callHandlers(IRQNUM_11)
	case IRQNUM_12:
		callHandlers(IRQNUM_12)
	case IRQNUM_13:
		callHandlers(IRQNUM_13)
	case IRQNUM_14:
		callHandlers(IRQNUM_14)
	case IRQNUM_15:
		callHandlers(IRQNUM_15)
	case IRQNUM_16:
		callHandlers(IRQNUM_16)
	case IRQNUM_17:
		callHandlers(IRQNUM_17)
	case IRQNUM_18:
		callHandlers(IRQNUM_18)
	case IRQNUM_19:
		callHandlers(IRQNUM_19)
	case IRQNUM_20:
		callHandlers(IRQNUM_20)
	case IRQNUM_21:
		callHandlers(IRQNUM_21)
	case IRQNUM_22:
		callHandlers(IRQNUM_22)
	case IRQNUM_23:
		callHandlers(IRQNUM_23)
	case IRQNUM_24:
		callHandlers(IRQNUM_24)
	case IRQNUM_25:
		callHandlers(IRQNUM_25)
	case IRQNUM_26:
		callHandlers(IRQNUM_26)
	case IRQNUM_27:
		callHandlers(IRQNUM_27)
	case IRQNUM_28:
		callHandlers(IRQNUM_28)
	case IRQNUM_29:
		callHandlers(IRQNUM_29)
	case IRQNUM_30:
		callHandlers(IRQNUM_30)
	case IRQNUM_31:
		callHandlers(IRQNUM_31)
	}
}

//export handleInterrupt
func handleInterrupt() {
	mcause := riscv.MCAUSE.Get()
	exception := mcause&(1<<31) == 0
	interruptNumber := uint32(mcause & 0x1f)

	if !exception && interruptNumber > 0 {
		// Save MSTATUS & MEPC, which may be overwritten by a nested interrupt.
		mstatus := riscv.MSTATUS.Get()
		mepc := riscv.MEPC.Get()

		// Temporarily disable this interrupt channel to prevent re-entry.
		riscv.MIE.ClearBits(1 << interruptNumber)
		riscv.Asm("fence")

		// Re-enable machine interrupts to allow higher-priority nesting.
		riscv.MSTATUS.SetBits(riscv.MSTATUS_MIE)

		// Call the registered interrupt handler(s).
		callHandler(int(interruptNumber))

		// Signal to sleepTicks that an interrupt has occurred.
		signalInterrupt()

		// Disable machine interrupts before restoring state.
		riscv.MSTATUS.ClearBits(riscv.MSTATUS_MIE)

		// Re-enable this interrupt channel.
		riscv.MIE.SetBits(1 << interruptNumber)
		riscv.Asm("fence")

		// Zero MCAUSE so that interrupt.In() returns false once we
		// return to normal (non-interrupt) code.
		riscv.MCAUSE.Set(0)

		// Restore MSTATUS & MEPC.
		riscv.MSTATUS.Set(mstatus)
		riscv.MEPC.Set(mepc)
	} else {
		// Exception (mcause bit 31 clear) or unexpected cause.
		handleException(mcause)
	}
}

func handleException(mcause uintptr) {
	println("*** Exception:     pc:", riscv.MEPC.Get())
	println("*** Exception:   code:", uint32(mcause&0x1f))
	println("*** Exception: mcause:", mcause)
	println("*** Exception:     ra:", tinygo_saved_ra)
	switch uint32(mcause & 0x1f) {
	case riscv.InstructionAccessFault:
		println("***    virtual address:", riscv.MTVAL.Get())
	case riscv.IllegalInstruction:
		println("***            opcode:", riscv.MTVAL.Get())
	case riscv.LoadAccessFault:
		println("***      read address:", riscv.MTVAL.Get())
	case riscv.StoreOrAMOAccessFault:
		println("***     write address:", riscv.MTVAL.Get())
	}
	for {
		riscv.Asm("wfi")
	}
}
