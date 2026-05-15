// Startup code for the ESP32-P4 HP core (RISC-V RV32IMC).
//
// When this code runs (entered from the ROM bootloader):
//   - The ROM bootloader has read the image from flash at offset 0x2000.
//   - The .data and .init segments have been DMA-copied to HP SRAM.
//   - The ROM has already configured the L2 cache and IROM MMU mapping
//     (0x40000000 → flash page 0), so .text in IROM is already accessible.
//   - The ROM has already partially disabled the default watchdog timers.
//   - The ROM provides an initial stack pointer (in ROM-private memory).
//
// Because the ROM bootloader on ECO2 (esp32p4-eco2-20240710) has different
// internal ROM function addresses than ECO ≥ 5, we do NOT call any ROM
// helper functions (disable_default_watchdog, Cache_FLASH_MMU_Set, etc.) —
// those addresses would land in the wrong code on ECO2 and crash.
//
// Instead we jump directly to the generic RISC-V _start (also in .init /
// SRAM), which sets sp = _stack_top and gp = __global_pointer$, then calls
// the TinyGo runtime main() which disables all watchdogs via direct register
// writes before doing anything else.

.section .init
.global call_start_cpu0
.type call_start_cpu0,@function

call_start_cpu0:
    // The ROM bootloader has already set up:
    //   - IROM MMU mapping (0x40000000 → flash), so .text is accessible
    //   - DMA copy of .data and .init into SRAM
    //   - Initial watchdog disable
    // Jump directly to the generic RISC-V _start to set SP, GP, then main.
    j _start

// ─── RISC-V interrupt / exception vector table ────────────────────────────
// The vector table must be aligned to 256 bytes (mtvec base alignment) and
// must be placed first in .text so the linker can find it.  Each slot is a
// 4-byte unconditional jump to the common handler; norvc forces 4-byte
// encoding (jal x0, offset) rather than the 2-byte compressed form.

.section .text.exception_vectors
.global _vector_table
.type _vector_table,@function

_vector_table:
    .option push
    .option norvc
    .rept 32
    j handleInterruptASM
    .endr
    .option pop

.size _vector_table, .-_vector_table
