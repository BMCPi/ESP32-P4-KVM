/* Linker script for the ESP32-P4 with PSRAM heap.
 *
 * This is a modified copy of the standard esp32p4.ld that redirects the
 * TinyGo GC heap from internal SRAM into external PSRAM (SPI RAM).
 *
 * Requirements before use:
 *   1. Call psramInit() (or the ESP-IDF esp_psram_init()) in your startup
 *      code BEFORE any heap allocation (i.e., before NewMemCache or make()).
 *   2. Update targets/esp32p4.json (or a local copy) to point at this file:
 *        "linkerscript": "bundling/esp32p4-psram.ld"
 *
 * PSRAM address on ESP32-P4:
 *   External SPI RAM is cache-mapped starting at 0x3C000000.
 *   Adjust LENGTH to match your chip variant (16 MiB for R16, 32 MiB for R32).
 */

/* Linker script for the ESP32-P4 (HP core, RISC-V RV32IMC)
 *
 * Key differences from ESP32-C3:
 *   - HP SRAM is unified (no separate IRAM/DRAM virtual address spaces):
 *     accessible at 0x4FF40000..0x4FFAEFC0 (~444KB, for chip rev ≥ v1.3).
 *     (0x4FF00000..0x4FF40000 is reserved for L2 cache with 256KB config.)
 *   - IROM is on the instruction bus at 0x40000000.
 *   - The firmware binary is read from flash offset 0x2000 (not 0x0):
 *     the P4 ROM bootloader looks for the second-stage image at 0x2000.
 *   - The MMU still works in 64KB pages, so paddr%64KB == vaddr%64KB must hold.
 *
 * Note: The ROM bootloader (ECO2) only handles IROM (0x40000000) as a
 * flash-mapped region. It does NOT correctly handle a separate DROM window
 * (0x44000000) — it tries to DMA-copy DROM segments to that address, which
 * crashes because 0x44000000 is read-only. To avoid this, .rodata is placed
 * in SRAM alongside .data. Only .text lives in flash via IROM.
 */

MEMORY
{
    /* HP SRAM: unified instruction+data, chip rev ≥ v1.3.
     * 0x4FF00000 + 256KB L2-cache = 0x4FF40000 start.
     * Ends at 0x4FFAEFC0 (iram_loader_seg boundary used by the bootloader).
     * Stack, BSS, data, and rodata live here; the heap is redirected to PSRAM.
     */
    SRAM (rwx) : ORIGIN = 0x4FF40000, LENGTH = 0x6EFC0  /* ~444 KB */

    /* External SPI RAM, cache-mapped by the L2-cache MMU.
     * Origin 0x3C000000 is the standard cache window for ESP32-P4 PSRAM.
     * Change LENGTH to 16M for the R16 chip variant.
     * PSRAM must be initialized (psramInit / esp_psram_init) before use.
     */
    PSRAM (rw) : ORIGIN = 0x3C000000, LENGTH = 32M

    /* Flash mapped via L2-cache MMU — instruction bus */
    IROM (rx)  : ORIGIN = 0x40000000, LENGTH = 64M
}

ENTRY(call_start_cpu0)

SECTIONS
{
    /* ── IROM: code mapped via instruction-bus L2-cache MMU ─────────────────
     *
     * esptool sorts ELF LOAD segments by virtual address when writing the
     * binary, so the actual on-flash order is always:
     *
     *   [0x2000] image_header (0x18 bytes)
     *   [0x2018] IROM segment header (0x8 bytes)   ← FIRST (VMA 0x40…)
     *   [0x2020] IROM data (.text)
     *   [0x2020+T] SRAM segment header(s)           ← LAST  (VMA 0x4FF…)
     *
     * The cache MMU maps 64 KB pages linearly: virtual 0x40000000 + off
     * → flash physical off (with paddr=0 in Cache_FLASH_MMU_Set).
     * The low-16-bit alignment constraint requires:
     *   VMA % 0x10000 == physical_flash_address % 0x10000
     *
     * IROM data starts at physical 0x2000 + 0x18 + 0x8 = 0x2020.
     * → IROM VMA must have low-16 = 0x2020.
     * → .text_dummy advances by 0x2020 = 0x2000 + 0x18 + 0x8.
     */
    .text_dummy (NOLOAD): ALIGN(4)
    {
        . += 0x2000;   /* P4 ROM reads image from flash 0x2000 */
        . += 0x18;     /* esp_image_header_t */
        . += 0x8;      /* IROM segment header (IROM is first in binary) */
    } > IROM

    /* Code that runs from flash via the L2-cache IROM MMU mapping. */
    .text : ALIGN(4)
    {
        . = ALIGN(256);
        *(.text.exception_vectors)
        . = ALIGN(4);
        *(.text .text.*)
        _irom_end = .;
    } > IROM

    /* ── SRAM: stack, BSS, initialized data, read-only data, startup code ───
     *
     * All segments below are copied by the ROM bootloader into SRAM at their
     * load addresses. .rodata is placed here (not in a separate DROM window)
     * because the ECO2 ROM bootloader does not correctly handle the DROM
     * window at 0x44000000.
     */

    /* Stack at the bottom of SRAM. */
    .stack (NOLOAD) :
    {
        . = ALIGN(16);
        . += _stack_size;
        _stack_top = .;
    } > SRAM

    /* Zero-initialized globals. */
    .bss (NOLOAD) : ALIGN(4)
    {
        . = ALIGN(4);
        _sbss = ABSOLUTE(.);
        *(.sbss)
        *(.sbss.*)
        *(.bss .bss.*)
        . = ALIGN(4);
        _ebss = ABSOLUTE(.);
    } > SRAM

    /* Initialized globals and read-only data — all in SRAM.
     * .rodata is included here rather than in a DROM flash-mapped window
     * because the ECO2 ROM does not support the 0x44000000 DROM bus.
     */
    .data : ALIGN(4)
    {
        . = ALIGN(4);
        _sdata = ABSOLUTE(.);
        *(.sdata)
        *(.sdata.*)
        *(.data .data.*)
        *(.dram*)
        *(.rodata*)
        *(.srodata*)
        . = ALIGN(4);
        _edata = ABSOLUTE(.);
        /* _sidata == _sdata: ROM bootloader already loaded .data into SRAM. */
        _sidata = _sdata;
    } > SRAM

    /* Startup code — loaded into SRAM by the ROM bootloader. */
    .init : ALIGN(4)
    {
        __init_start = .;
        *(.init)
        __init_end = .;
    } > SRAM

    /* No DROM flash-mapped data; rodata is in SRAM so GC covers it via
     * _globals_start.._globals_end. Set flash data range to empty. */
    PROVIDE(__flash_data_start = 0);
    PROVIDE(__flash_data_end   = 0);

    /DISCARD/ :
    {
        *(.eh_frame)
    }

    ASSERT((_edata + SIZEOF(.init)) <= 0x4FFAEFC0,
           "SRAM overflow: .data + .rodata + .init are too large for the ESP32-P4 SRAM region")
}

/* Symbols used by the TinyGo GC and runtime. */
_globals_start = _sbss;
_globals_end   = _edata;
/* Heap redirected to PSRAM — all make() and heap allocations land in PSRAM. */
_heap_start = ORIGIN(PSRAM);
_heap_end   = ORIGIN(PSRAM) + LENGTH(PSRAM);

_stack_size = 8K;

/* ─── ROM functions for cache / flash MMU setup ───────────────────────────
 * Addresses verified against esp32p4-eco2-20240710 ROM.
 */
disable_default_watchdog = 0x4fc000a4;
Cache_Invalidate_All     = 0x4fc00404;
Cache_Suspend_L2_Cache   = 0x4fc00508;
Cache_Resume_L2_Cache    = 0x4fc0050c;
Cache_FLASH_MMU_Init     = 0x4fc00510;
Cache_FLASH_MMU_Set      = 0x4fc00518;

/* ─── libgcc from esp32p4.rom.libgcc.ld ──────────────────────────────────
 * Soft-float and integer helpers provided by the P4 ROM.
 * (The P4 HP core supports the F extension; single-precision ops like
 *  __addsf3 use hardware FP instructions and are absent from ROM libgcc.)
 */
__absvdi2     = 0x4fc00730;
__absvsi2     = 0x4fc00734;
__adddf3      = 0x4fc00738;
__addvdi3     = 0x4fc0073c;
__addvsi3     = 0x4fc00740;
__ashldi3     = 0x4fc00744;
__ashrdi3     = 0x4fc00748;
__bswapdi2    = 0x4fc0074c;
__bswapsi2    = 0x4fc00750;
__clear_cache = 0x4fc00754;
__clrsbdi2    = 0x4fc00758;
__clrsbsi2    = 0x4fc0075c;
__clzdi2      = 0x4fc00760;
__clzsi2      = 0x4fc00764;
__cmpdi2      = 0x4fc00768;
__ctzdi2      = 0x4fc0076c;
__ctzsi2      = 0x4fc00770;
__divdc3      = 0x4fc00774;
__divdf3      = 0x4fc00778;
__divdi3      = 0x4fc0077c;
__divsc3      = 0x4fc00780;
__divsi3      = 0x4fc00784;
__eqdf2       = 0x4fc00788;
__extendsfdf2 = 0x4fc0078c;
__ffsdi2      = 0x4fc00790;
__ffssi2      = 0x4fc00794;
__fixdfdi     = 0x4fc00798;
__fixdfsi     = 0x4fc0079c;
__fixsfdi     = 0x4fc007a0;
__fixunsdfsi  = 0x4fc007a4;
__fixunssfdi  = 0x4fc007a8;
__fixunssfsi  = 0x4fc007ac;
__floatdidf   = 0x4fc007b0;
__floatdisf   = 0x4fc007b4;
__floatsidf   = 0x4fc007b8;
__floatundidf = 0x4fc007bc;
__floatundisf = 0x4fc007c0;
__floatunsidf = 0x4fc007c4;
__gcc_bcmp    = 0x4fc007c8;
__gedf2       = 0x4fc007cc;
__gtdf2       = 0x4fc007d0;
__ledf2       = 0x4fc007d4;
__lshrdi3     = 0x4fc007d8;
__ltdf2       = 0x4fc007dc;
__moddi3      = 0x4fc007e0;
__modsi3      = 0x4fc007e4;
__muldc3      = 0x4fc007e8;
__muldf3      = 0x4fc007ec;
__muldi3      = 0x4fc007f0;
__mulsc3      = 0x4fc007f4;
__mulsi3      = 0x4fc007f8;
__mulvdi3     = 0x4fc007fc;
__mulvsi3     = 0x4fc00800;
__nedf2       = 0x4fc00804;
__negdf2      = 0x4fc00808;
__negdi2      = 0x4fc0080c;
__negvdi2     = 0x4fc00810;
__negvsi2     = 0x4fc00814;
__paritysi2   = 0x4fc00818;
__popcountdi2 = 0x4fc0081c;
__popcountsi2 = 0x4fc00820;
__powidf2     = 0x4fc00824;
__subdf3      = 0x4fc00828;
__subvdi3     = 0x4fc0082c;
__subvsi3     = 0x4fc00830;
__ucmpdi2     = 0x4fc00834;
__udivdi3     = 0x4fc00838;
__udivmoddi4  = 0x4fc0083c;
__udivsi3     = 0x4fc00840;
__udiv_w_sdiv = 0x4fc00844;
__umoddi3     = 0x4fc00848;
__umodsi3     = 0x4fc0084c;
__unorddf2    = 0x4fc00850;
__extenddftf2 = 0x4fc00854;
__trunctfdf2  = 0x4fc00858;
