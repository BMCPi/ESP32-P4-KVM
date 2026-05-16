// Startup code for the ESP32-P4 HP core (RISC-V RV32IMC).
//
// When this code runs (entered from the ROM bootloader at the entry point):
//   - The ROM bootloader has read the image from flash at offset 0x2000.
//   - The .data and .init segments have been DMA-copied to HP SRAM.
//   - The ROM has already partially disabled the default watchdog timers.
//   - The ROM provides an initial stack pointer (in ROM-private memory).
//
// HOWEVER, on ECO2 (esp32p4-eco2-20240710) the ROM does NOT correctly
// configure the L2 cache MMU for the IROM (0x40000000+) window in a way
// the hardware understands.  Specifically, the SOC_MMU_FLASH_VALID bit on
// ESP32-P4 is BIT(12) (0x1000), and the IROM MMU entries either are not
// written or use a different (older-chip) bit layout.  The result is that
// instruction fetches from IROM return 0x0000 (C.UNIMP), which crashes the
// CPU on the very first instruction of main() with:
//
//   Guru Meditation Error: Core 0 panic'ed (Illegal instruction)
//   PC = first IROM instruction, MTVAL = 0x00000000
//
// To recover, call_start_cpu0 (which runs from SRAM and is therefore not
// affected by the broken IROM mapping) must:
//   1. Bring the L1/L2 caches into a known-good state
//      (clear SHUT_IBUS, clear BYPASS, clear FREEZE).
//   2. Write the L2 cache MMU entries explicitly via the SPI0 indirect
//      registers, with the correct ESP32-P4 entry format
//      (SOC_MMU_FLASH_VALID = BIT(12) | physical_page_number).
//   3. Invalidate all caches so any stale 0x0000 cache lines are dropped
//      and the next IROM fetch reloads from flash via the new MMU entry.
//   4. Jump to _start (which sets sp/gp and calls main()).
//
// Register / address constants (ESP32-P4):
//   CACHE base                     = 0x3FF10000
//     L1_ICACHE_CTRL          + 0x000  (SHUT_IBUS0..3 in bits 0..3)
//     L1_BYPASS_CACHE_CONF    + 0x008  (BYPASS_L1_ICACHE0..3, BYPASS_L1_DCACHE)
//     SYNC_CTRL               + 0x098  (INVALIDATE_ENA bit 0, SYNC_DONE bit 4)
//     SYNC_MAP                + 0x09C  (bit 0..3 = L1 IC0..3, bit 4 = L1 DC, bit 5 = L2)
//     SYNC_ADDR               + 0x0A0
//     SYNC_SIZE               + 0x0A4
//     L2_BYPASS_CACHE_CONF    + 0x274  (BYPASS_L2_CACHE_EN bit 5)
//     L2_CACHE_FREEZE_CTRL    + 0x28C  (L2_CACHE_FREEZE_EN bit 20)
//   SPI0 (FLASH_SPI0) base         = 0x5008C000
//     SPI_MEM_C_MMU_ITEM_CONTENT + 0x37C
//     SPI_MEM_C_MMU_ITEM_INDEX   + 0x380
//     SPI_MEM_C_MMU_POWER_CTRL   + 0x384
//   UART0 base                     = 0x500CA000  (FIFO at +0x00, STATUS at +0x1C)
//
// MMU entry encoding for FLASH (per ESP-IDF soc/ext_mem_defs.h, v5.4):
//   bit 12       = SOC_MMU_FLASH_VALID
//   bit 11       = SOC_MMU_PSRAM_VALID
//   bit 10       = SOC_MMU_ACCESS_PSRAM (0 ⇒ flash)
//   bits 0..10   = physical page number (mask 0x7FF for flash)
//   Page size is 64 KB; entry N maps vaddr 0x40000000 + N*0x10000
//                              to flash physical N*0x10000.

.section .init
.global call_start_cpu0
.type call_start_cpu0,@function

call_start_cpu0:
    // ── Diagnostic marker 'S': prove we executed call_start_cpu0 (SRAM). ──
    li   t0, 0x500ca000
    li   t1, 'S'
    sw   t1, 0(t0)

    // ── Step 1: cache control — bring L1/L2 into known-good state. ─────
    lui  t0, 0x3ff10              // CACHE peripheral base = 0x3FF10000

    sw   zero, 0(t0)              // L1_ICACHE_CTRL = 0  (SHUT_IBUS0..3 clear)
    sw   zero, 8(t0)              // L1_BYPASS_CACHE_CONF = 0
    sw   zero, 0x274(t0)          // L2_BYPASS_CACHE_CONF = 0
    sw   zero, 0x28c(t0)          // L2_CACHE_FREEZE_CTRL = 0

    // ── Step 2: program FLASH MMU entries via SPI0 indirect registers. ──
    // Map IROM virtual pages 0..31 → flash physical pages 0..31 (covers the
    // full 0x40000000..0x40200000 range, well above the 0x18000-byte .text).
    li   t0, 0x5008c000           // SPI0 base
    sw   zero, 0x384(t0)          // MMU_POWER_CTRL = 0 (clear FORCE_PD/ON)

    li   t1, 0                    // index = 0
    li   t2, 32                   // count
    li   t3, 0x1000               // SOC_MMU_FLASH_VALID
.mmu_loop:
    sw   t1, 0x380(t0)            // MMU_ITEM_INDEX = i
    or   t4, t3, t1               // FLASH_VALID | i  (page i mapped to itself)
    sw   t4, 0x37c(t0)            // MMU_ITEM_CONTENT = entry value
    addi t1, t1, 1
    bne  t1, t2, .mmu_loop

    // ── Step 3: invalidate all caches so stale 0x0000 lines are dropped. ──
    lui  t0, 0x3ff10
    li   t1, 0x3f                 // SYNC_MAP: all L1 + L2
    sw   t1, 0x9c(t0)
    sw   zero, 0xa0(t0)           // SYNC_ADDR = 0
    li   t1, 0xfffffff
    sw   t1, 0xa4(t0)             // SYNC_SIZE = max
    li   t1, 1
    sw   t1, 0x98(t0)             // SYNC_CTRL.INVALIDATE_ENA = 1
.wait_sync:
    lw   t1, 0x98(t0)
    andi t1, t1, 0x10             // SYNC_DONE bit
    beqz t1, .wait_sync

    // ── Diagnostic marker 'M': MMU + invalidate complete. ──
    li   t0, 0x500ca000
    li   t1, 'M'
    sw   t1, 0(t0)

    j    _start

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
