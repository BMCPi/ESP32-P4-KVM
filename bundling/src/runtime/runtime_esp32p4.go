package runtime
//go:build esp32p4

package runtime

import (
	"device/esp"
	"device/riscv"
	"machine"
	"runtime/interrupt"
	"runtime/volatile"
	"unsafe"
)

// main is called from _start (in src/device/riscv/start.S) after the stack
// pointer has been set and flash MMU has been configured by call_start_cpu0
// (in src/device/esp/esp32p4.S).
//
//export main
func main() {
	// Watchdog timers were already disabled by call_start_cpu0 via the ROM
	// disable_default_watchdog call.  No register writes needed here.

	// Disable the TIMG0 watchdog just in case it was re-enabled.
	esp.TIMG0.WDTWPROTECT.Set(0x50D83AA1) // write-protection key
	esp.TIMG0.WDTCONFIG0.Set(0)

	// Clear .bss: zero-initialized global variables.
	// The ROM bootloader has already loaded .data from flash.
	clearbss()

	// Set the interrupt address (vectored mode: bit[1:0]=1).
	// The _vector_table is 256-byte aligned (see esp32p4.S).
	riscv.MTVEC.Set((uintptr(unsafe.Pointer(&_vector_table))) | 1)

	// Initialize main system timer used for time.Now / time.Sleep.
	initTimer()

	// Initialize timer alarm interrupt for the scheduler.
	initTimerInterrupt()

	// Initialize the heap, call main.main, etc.
	run()

	// Fallback: if main ever returns, hang.
	exit(0)
}

func init() {
	machine.InitSerial()
}

//go:extern _sbss
var _sbss [0]byte

//go:extern _ebss
var _ebss [0]byte

// clearbss zeroes the .bss section.  The ROM bootloader loads .data for us so
// we only need to zero .bss here.
func clearbss() {
	ptr := unsafe.Pointer(&_sbss)
	for ptr != unsafe.Pointer(&_ebss) {
		*(*uint32)(ptr) = 0
		ptr = unsafe.Add(ptr, 4)
	}
}

// initTimer configures TIMG0 timer 0 as a free-running 40 MHz counter
// (APB clock / prescaler 2).  This matches the C3 setup so that the same
// nanosecondsToTicks / ticksToNanoseconds constants apply.
func initTimer() {
	esp.TIMG0.T0CONFIG.Set(esp.TIMG_T0CONFIG_EN | esp.TIMG_T0CONFIG_INCREASE | 2<<esp.TIMG_T0CONFIG_DIVIDER_Pos)
	esp.TIMG0.T0LOADLO.Set(0)
	esp.TIMG0.T0LOADHI.Set(0)
	esp.TIMG0.T0LOAD.Set(0) // any write triggers the load
}

// ticks returns the current timer tick count.
func ticks() timeUnit {
	// Write any value to T0UPDATE to latch the counter into T0LO/T0HI.
	esp.TIMG0.T0UPDATE.Set(0)
	return timeUnit(uint64(esp.TIMG0.T0LO.Get()) | uint64(esp.TIMG0.T0HI.Get())<<32)
}

// nanosecondsToTicks converts nanoseconds to timer ticks.
// At APB=80 MHz and prescaler=2 the tick period is 25 ns.
func nanosecondsToTicks(ns int64) timeUnit {
	return timeUnit(ns / 25)
}

// ticksToNanoseconds converts timer ticks to nanoseconds.
func ticksToNanoseconds(ticks timeUnit) int64 {
	return int64(ticks) * 25
}

// CPU interrupt number used for the TIMG0 timer alarm.
const timerAlarmCPUInterrupt = 9

var interruptPending volatile.Register8

func signalInterrupt() {
	interruptPending.Set(1)
}

// interruptInit sets up MTVEC (already done in main above) and resets MIE.
func interruptInit() {
	mie := riscv.DisableInterrupts()

	// Reset all INTERRUPT_CORE0 priority map entries to 0 (CPU interrupt 0 =
	// disabled on P4; valid CPU interrupt numbers start at 1).
	priReg := &esp.INTERRUPT_CORE0.LP_RTC_INT_MAP
	for i := 0; i < 64; i++ {
		priReg.Set(0)
		priReg = (*volatile.Register32)(unsafe.Add(unsafe.Pointer(priReg), 4))
	}

	riscv.EnableInterrupts(mie)
}

// initTimerInterrupt routes the TIMG0 T0 alarm to CPU interrupt channel 9
// and enables it.
func initTimerInterrupt() {
	// Map TIMG0 T0 alarm peripheral interrupt → CPU interrupt channel 9.
	esp.INTERRUPT_CORE0.TIMERGRP0_T0_INT_MAP.Set(timerAlarmCPUInterrupt)

	// Enable T0 interrupt at the timer group level.
	esp.TIMG0.INT_ENA_TIMERS.SetBits(1)

	interrupt.New(timerAlarmCPUInterrupt, func(interrupt.Interrupt) {
		esp.TIMG0.INT_CLR_TIMERS.Set(1)
		interruptPending.Set(1)
	})

	mie := riscv.DisableInterrupts()
	riscv.EnableInterrupts(mie | (1 << (timerAlarmCPUInterrupt + 16)))
}

// sleepTicks spins until the given number of ticks have elapsed, using the
// TIMG0 alarm interrupt to avoid busy-waiting for the full duration.
func sleepTicks(d timeUnit) {
	machine.FlushSerial()
	target := ticks() + d
	for ticks() < target {
		interruptPending.Set(0)

		esp.TIMG0.T0ALARMLO.Set(uint32(target))
		esp.TIMG0.T0ALARMHI.Set(uint32(target >> 32))
		esp.TIMG0.T0CONFIG.SetBits(esp.TIMG_T0CONFIG_ALARM_EN)

		for interruptPending.Get() == 0 {
			if ticks() >= target {
				return
			}
		}
	}
}

func exit(code int) {
	abort()
}

func abort() {
	for {
		riscv.Asm("wfi")
	}
}

func putchar(c byte) {
	machine.Serial.WriteByte(c)
}

func getchar() byte {
	for machine.Serial.Buffered() == 0 {
		Gosched()
	}
	v, _ := machine.Serial.ReadByte()
	return v
}

func buffered() int {
	return machine.Serial.Buffered()
}

//go:extern _vector_table
var _vector_table [0]uintptr
