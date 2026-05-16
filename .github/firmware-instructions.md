# Firmware Boot — ESP32-P4 ECO2

## Status: Booting ✅ (as of 2026-05-15)

The firmware boots cleanly from flash, runs through `_start`, executes
`main.main`, and enters its main loop without crashing.  A representative
boot trace:

```text
ESP-ROM:esp32p4-eco2-20240710
…
load:0x40002020,len:0x15dc0
load:0x4ff423a0,len:0x69d4
entry 0x4ff48cf4
SM                                        ← call_start_cpu0 diagnostic markers
>WBVTI                                    ← runtime diagnostic markers
Starting ESP32-P4 KVM Controller
Setting up GPIO...
GPIO setup complete.
Initializing storage...
Storage warning: Virtual Media unavailable - SD card configure:
Main loop: running...
Main loop: running...
```

---

## Root causes and fixes

### 1. IROM fetched 0x0000 → Illegal instruction at `main()` entry

**Symptom (pre-fix):** Every boot crashed at `PC = 0x40006b9e` (the first
instruction of `main()` in IROM) with `MTVAL = 0x00000000`.  The CPU was
fetching `0x0000` (C.UNIMP) instead of the expected `0x1141`
(`c.addi sp,-16`) even though the firmware binary contained the correct
bytes at flash offset `0x4b9e`.

**Cause:** The ECO2 ROM bootloader does not leave the L2 cache FLASH MMU
in a state the hardware understands for the IROM window.  ESP-IDF v5.4's
`soc/ext_mem_defs.h` shows the entry encoding for ESP32-P4:

```c
#define SOC_MMU_FLASH_VALID    BIT(12)   /* 0x1000 */
#define SOC_MMU_PSRAM_VALID    BIT(11)
#define SOC_MMU_ACCESS_PSRAM   BIT(10)
#define SOC_MMU_PAGE_SIZE      0x10000   /* 64 KB pages */
```

(Earlier notes that the valid bit was `0x80000000` were wrong — that
position is from older Espressif chips.  The actual FLASH valid bit on
ESP32-P4 is `BIT(12)`.)

**Fix — [bundling/src/device/esp/esp32p4.S](../bundling/src/device/esp/esp32p4.S):**
`call_start_cpu0` now:

1. Emits `S` on UART0 (diagnostic; proves SRAM execution).
2. Clears `L1_ICACHE_CTRL`, `L1_BYPASS_CACHE_CONF`,
   `L2_BYPASS_CACHE_CONF`, and `L2_CACHE_FREEZE_CTRL` to known-good
   states.
3. Writes 32 FLASH MMU entries via the SPI0 indirect registers
   (`SPI_MEM_C_MMU_ITEM_INDEX_REG @ 0x5008C380`,
   `SPI_MEM_C_MMU_ITEM_CONTENT_REG @ 0x5008C37C`):
   each entry value is `0x1000 | i`, mapping virtual page `i` of the
   IROM window to flash physical page `i`.  This covers
   `0x40000000..0x40200000` (2 MB), more than the ~96 KB `.text`
   actually uses.
4. Invalidates all caches via `SYNC_CTRL` (CACHE base `+0x98`) so any
   stale `0x0000` cache lines installed by the ROM are dropped.
5. Emits `M` on UART0 (diagnostic; proves MMU/cache work completed).
6. Jumps to `_start`.

### 2. Global pointer was uninitialised

**Cause:** `gp` was `0x00000800` (raw `0x800` with no base symbol),
breaking every GP-relative load/store in SRAM.

**Fix — [bundling/targets/esp32p4.ld](../bundling/targets/esp32p4.ld):**
added `__global_pointer$ = _sdata + 0x800;` inside `.data`.  Confirmed by
`gp = 0x4ff42ba0` in subsequent crash dumps.

### 3. DROM segment was DMA-loaded to 0x44000000 by the ROM

**Cause:** The ECO2 ROM bootloader does not handle the separate DROM
flash window; placing `.rodata` there made the ROM try to DMA-copy it
into read-only address space (`Store/AMO access fault`).

**Fix — [bundling/targets/esp32p4.ld](../bundling/targets/esp32p4.ld):**
`.rodata` is placed in SRAM alongside `.data`.  Only `.text` lives in
flash via the IROM window.

### 4. Watchdog timers reset the chip mid-boot

**Fix — [bundling/src/runtime/runtime_esp32p4.go](../bundling/src/runtime/runtime_esp32p4.go):**
runtime `main()` disables TIMG0 WDT, TIMG1 WDT, LP_WDT RWDT, and
LP_WDT SWD before doing anything else.  ESP32-P4 uses the same
write-protect key (`0x50D83AA1`) for RWDT and SWD, unlike older C3/S3
(which use `0x8F1D312A` for SWD).

### 5. SD-card driver crashed `runtime.alloc` with 65535 per-CMD allocations

**Symptom (after the IROM fix):** Boot ran successfully through
`Initializing storage...` and then crashed inside `runtime.alloc`
(`PC = 0x40005026`, `RA = sdcard.cmd+0x10e`, `MCAUSE = 0x38000005`,
fault addr `0xFFFFFFFF`).  The disassembly at the crash PC is the
list-unlink step of `popFreeRange`, with `a3` (the freeRange node
pointer) holding garbage — GC free-list corruption after sustained heap
pressure.

**Cause:** `tinygo.org/x/drivers/sdcard` v0.31.0 `cmd()` allocates a
fresh `[]byte{0xFF}` on every iteration of its 65 535-iteration response
poll.  With no SD card attached, that loop runs to completion on every
CMD0 attempt, producing tens of thousands of 1-byte heap allocations.
TinyGo's blocks GC corrupts its free list under that pressure and
panics inside `popFreeRange`.

**Fix — [pkg/sdcard/](../pkg/sdcard/):** vendor the
`tinygo.org/x/drivers/sdcard@v0.31.0` package into the project and
change `Device.cmd` to reuse the pre-allocated `cmdbuf[:1]` (set to
`0xFF`) instead of a literal each iteration.  [pkg/storage/storage.go](../pkg/storage/storage.go)
now imports `github.com/bmcpi/esp32-p4-kvm/pkg/sdcard` instead of the
upstream path.  No more per-iteration allocation, GC stays healthy,
and the driver returns `0xFF` to the caller which surfaces as
`fmt.Errorf("no SD card")` → handled by the optional-feature path in
[main.go](../main.go).

---

## Build workflow

`make build` is now self-healing for the TinyGo install: it runs
`apply-overrides` first, which copies every file under `bundling/` to
its destination inside `$(tinygo env TINYGOROOT)`.  A `brew upgrade
tinygo` no longer silently regresses the firmware.

| Source under `bundling/`                                | Destination                                                                            |
|---------------------------------------------------------|----------------------------------------------------------------------------------------|
| `src/device/esp/esp32p4.S`                              | `$(tinygo env TINYGOROOT)/src/device/esp/esp32p4.S`                                    |
| `targets/esp32p4.ld`                                    | `$(tinygo env TINYGOROOT)/targets/esp32p4.ld`                                          |
| `src/runtime/runtime_esp32p4.go`                        | `$(tinygo env TINYGOROOT)/src/runtime/runtime_esp32p4.go`                              |

The sdcard fix lives in [pkg/sdcard/](../pkg/sdcard/) (a fork of
`tinygo.org/x/drivers/sdcard@v0.31.0` carrying the cmdbuf-reuse change)
and is imported directly by `pkg/storage`, so it needs no module-cache
override.

To rebuild + flash + observe:

```bash
make flash
sudo screen /dev/cu.usbmodem5B5F0916211 115200
# detach with Ctrl+A k y
```

---

## Open follow-ups

- [ ] **Remove the diagnostic `S`/`M` UART writes** from
  `call_start_cpu0` once we are confident the IROM/MMU fix is stable.
  Currently they emit two stray bytes on every boot — harmless but
  noisy.
- [ ] **Remove the `>WBVTI` markers** from the runtime for the same
  reason.
- [ ] **Upstream the sdcard fix** to `tinygo.org/x/drivers` so we can
  delete the module-cache patch step.
- [ ] **Verify with a real SD card attached** — current testing only
  proves the no-card error path is graceful.  With a card present,
  `storage.StartVirtualMedia()` and the USB MSC pipeline should also
  come up.
- [ ] **Re-enable the API server** (`api.StartAPIServer`) in `main.go`
  once `pkg/api` builds cleanly on this target, and verify the Redfish
  endpoint:

  ```bash
  curl http://<device-ip>/healthz
  curl http://<device-ip>/redfish/v1/Systems/1
  ```
