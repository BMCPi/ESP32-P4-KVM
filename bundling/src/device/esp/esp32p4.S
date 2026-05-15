// Startup code for the ESP32-P4 HP core (RISC-V RV32IMC).
//
// When this code runs:
//   - The ROM bootloader has read the image from flash at offset 0x2000.
//   - The .data and .init segments have been copied to HP SRAM (0x4FF40000+).
//   - The ROM provides an initial stack pointer (in ROM-private memory).
//   - The L2 cache / flash MMU is NOT yet configured; IROM/DROM are inaccessible.
//
// This code:
//   1. Disables the default watchdog timers (to avoid WDT reset during setup).
//   2. Suspends the L2 cache autoloads.
//   3. Invalidates all L2 cache entries.
//   4. Initialises the flash MMU (resets all entries to invalid).
//   5. Maps flash to the IROM window (instruction bus, 0x40000000).
//   6. Maps flash to the DROM window (data bus, 0x44000000).
//   7. Resumes the L2 cache.
//   8. Falls through to the generic RISC-V _start (also in .init / SRAM),
//      which sets up the real stack pointer and global pointer, then calls main.
//
// All ROM functions are called via the RISC-V "call" pseudo-instruction
// (auipc + jalr), which has ±2 GB range — sufficient to reach ROM at
// 0x4FC00000 from SRAM at 0x4FF40000.

.section .init
.global call_start_cpu0
.type call_start_cpu0,@function

call_start_cpu0:
    // Disable the default HP_WDT and TIMG watchdog timers so they don't fire
    // while we spend time initialising the L2-cache flash MMU.
    call disable_default_watchdog   // ROM: 0x4fc000a4

    // Suspend L2 cache autoloads.  Returns the previous autoload-enable state
    // in a0; save it in s0 so we can restore it after MMU setup.
    call Cache_Suspend_L2_Cache     // ROM: 0x4fc00508
    mv s0, a0

    // Invalidate all cache lines (L1 I/D and L2).
    call Cache_Invalidate_All       // ROM: 0x4fc00404

    // Reset every MMU entry to "invalid" so stale flash-page mappings
    // cannot accidentally cause wrong reads.
    call Cache_FLASH_MMU_Init       // ROM: 0x4fc00510

    // Map flash pages to the IROM window (instruction bus, 0x40000000).
    // The linker script places code at virtual addresses 0x40002000+; with
    // page 0 of flash (0x0000–0xFFFF) mapped to virtual 0x40000000, the
    // physical address of that code (at flash 0x2000+) matches the low-16-bit
    // constraint: paddr % 64KB == vaddr % 64KB.
    //
    // Cache_FLASH_MMU_Set(sensitive, vaddr, paddr_page0_byte, psize_KB, num_pages, fixed)
    li a0, 0              // sensitive = 0 (normal access)
    li a1, 0x40000000     // vaddr = IROM base
    li a2, 0              // paddr = byte 0 of flash (page 0)
    li a3, 64             // psize = 64 KB per page
    li a4, 128            // num   = 128 pages (8 MB coverage)
    li a5, 0              // fixed = 0
    call Cache_FLASH_MMU_Set        // ROM: 0x4fc00518

    // Map the same flash pages to the DROM window (data bus, 0x44000000).
    // .rodata is placed at virtual 0x44002020 (after the 0x2020 dummy);
    // flash physical 0x2020 ↔ virtual 0x44002020 (same low 16 bits). ✓
    li a0, 0              // sensitive = 0
    li a1, 0x44000000     // vaddr = DROM base
    li a2, 0              // paddr = byte 0 of flash (page 0)
    li a3, 64             // psize = 64 KB per page
    li a4, 128            // num   = 128 pages (8 MB coverage)
    li a5, 0              // fixed = 0
    call Cache_FLASH_MMU_Set        // ROM: 0x4fc00518

    // Resume the L2 cache with the saved autoload state.
    mv a0, s0
    call Cache_Resume_L2_Cache      // ROM: 0x4fc0050c

    // Jump to the generic RISC-V startup (_start, also in .init / SRAM).
    // _start sets sp = _stack_top, gp = __global_pointer$, then calls main.
    // (The linker typically places _start immediately after call_start_cpu0
    //  so this becomes a short branch.)
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
