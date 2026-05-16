# Firmware Boot Investigation — ESP32-P4 ECO2

## Session Summary (May 15, 2026)

### Context

The firmware loads cleanly from flash (no "Invalid image block" — fixed in prior sessions) but crashes immediately with **Illegal instruction** at `PC = 0x40006b9e`, the very first instruction of `main()` in IROM. MTVAL = 0x00000000, meaning the CPU fetches `0x0000` (C.UNIMP) instead of the expected `0x1141` (c.addi sp,-16).

---

### Changes Made

#### 1. `bundling/targets/esp32p4.ld` — Global pointer fix

Added `__global_pointer$` to the `.data` section so the linker emits a valid `gp` value:

```ld
_sdata = ABSOLUTE(.);
__global_pointer$ = _sdata + 0x800;
```

**Why:** Without this, `gp` was `0x00000800` (raw `0x800` with no base), causing all GP-relative loads/stores in SRAM to access wrong addresses. After the fix, `gp = 0x4ff42ba0` (confirmed in crash dumps).

This file has been copied to `/opt/homebrew/Cellar/tinygo/0.41.1/targets/esp32p4.ld`.

---

#### 2. `bundling/src/device/esp/esp32p4.S` — Cache register fix in `call_start_cpu0`

Expanded `call_start_cpu0` (the ROM entry point, runs in SRAM) to explicitly clear three cache-related registers before jumping to `_start`:

```asm
call_start_cpu0:
    lui  t0, 0x3ff10          // t0 = 0x3ff10000 = CACHE peripheral base

    // 1. L1_ICACHE_CTRL (0x3ff10000): clear SHUT_IBUS0 and SHUT_IBUS1 (bits 0-1)
    lw   t1, 0(t0)
    andi t1, t1, -4           // ~0x3 → clear bits 0-1
    sw   t1, 0(t0)

    // 2. L2_BYPASS_CACHE_CONF (0x3ff10274): clear BYPASS_L2_CACHE_EN (bit 5)
    lw   t1, 0x274(t0)
    andi t1, t1, -33          // ~0x20 → clear bit 5
    sw   t1, 0x274(t0)

    // 3. L2_CACHE_FREEZE_CTRL (0x3ff1028C): clear L2_CACHE_FREEZE_EN (bit 20)
    lw   t1, 0x28c(t0)
    lui  t2, 0x100            // t2 = 0x00100000 = (1 << 20)
    not  t2, t2               // t2 = 0xFFEFFFFF = ~(1 << 20)
    and  t1, t1, t2
    sw   t1, 0x28c(t0)

    j    _start
```

**Why:** Hypothesis was that the ROM leaves the L2 cache frozen or bypassed on ECO2 before jumping to our code, causing instruction fetches from IROM to return `0x0000`.

**What the crash dumps revealed:** All three registers were already `0` when our code ran (confirmed via register state in the Guru Meditation dumps):

- `T1 = 0x00000000` after step 1 → `L1_ICACHE_CTRL` was already `0` (SHUT_IBUS0/1 already clear)
- `T1 = 0x00000000` after step 2 → `L2_BYPASS_CACHE_CONF` was already `0` (no bypass)
- `T2 = 0xFFEFFFFF` and `T1 = 0x00000000` after step 3 → `L2_CACHE_FREEZE_CTRL` was already `0` (not frozen)

The cache peripheral is in a correct state. These writes are no-ops but harmless.

This file has been copied to `/opt/homebrew/Cellar/tinygo/0.41.1/src/device/esp/esp32p4.S`.

---

#### 3. `bundling/src/runtime/runtime_esp32p4.go` — Diagnostic checkpoints

Added `rawput` character markers (`>`, `W`, `B`, `V`, `T`, `I`) at the start of `main()` to detect how far the runtime gets before crashing. None have appeared in serial output yet — the crash happens before the `main()` body executes, even though PC = `0x40006b9e` is the first instruction of `main()`.

---

### Key Findings

| Register / Address | Value | Meaning |
|---|---|---|
| `L1_ICACHE_CTRL` @ 0x3ff10000 | `0x00000000` | SHUT_IBUS0/1 clear — instruction buses open ✓ |
| `L2_BYPASS_CACHE_CONF` @ 0x3ff10274 | `0x00000000` | Cache bypass disabled ✓ |
| `L2_CACHE_FREEZE_CTRL` @ 0x3ff1028C | `0x00000000` | Cache not frozen ✓ |
| `GP` | `0x4ff42ba0` | Global pointer fixed ✓ |
| `firmware.bin` @ file offset 0x4b9e | `0x41 0x11` | Correct bytes for `c.addi sp,-16` ✓ |

The cache hardware is configured correctly. The firmware binary contains the right instruction bytes at the IROM crash location. The crash is still happening.

---

### Unresolved: Why Does IROM Fetch Return `0x0000`?

Despite the cache appearing functional, the CPU reads `0x0000` from VMA `0x40006b9e`. Remaining hypotheses:

1. **L2 cache MMU not set up** — The ROM prints `load:0x40002020,len:0x15dbc` but may not be configuring the L2 MMU page table on ECO2. The IROM window (`0x40000000+`) has no path to flash without MMU entries. The MMU is accessed indirectly via `SPI_MEM_MMU_ITEM_INDEX` (SPI0 + 0x380) and `SPI_MEM_MMU_ITEM_CONTENT` (SPI0 + 0x37C).

2. **Wrong flash page mapped** — The ROM might map the wrong flash page (e.g., off-by-one due to the firmware header at flash offset 0x2000), causing valid cache lookups that return wrong data. The expected mapping: VMA page 0 (`0x40000000–0x4000FFFF`) → flash page 0 (`0x0000–0xFFFF`).

3. **L1 cache bypass** — `L1_BYPASS_CACHE_CONF` (CACHE + 0x8 = 0x3ff10008) was not read/cleared. If L1 instruction bypass is enabled and L2 has no MMU mapping, fetches could silently return 0.

---

### Next Steps

1. **Read the MMU table** — Add assembly in `call_start_cpu0` to read MMU entry 0 via SPI0 indirect registers and print to UART0 (TX FIFO @ 0x500ca000), then compare to expected value `0x80000000` (valid + page 0).

2. **Manually set up MMU** — If MMU entry 0 is 0 (not set up), write it directly:
   - Write `0` to `SPI_MEM_MMU_ITEM_INDEX` (SPI0 + 0x380 = 0x5008c380)
   - Write `0x80000000` (valid bit + page number 0) to `SPI_MEM_MMU_ITEM_CONTENT` (SPI0 + 0x37C = 0x5008c37C)
   - Repeat for page 1 (index=1, content=`0x80000001`)

3. **Check `L1_BYPASS_CACHE_CONF`** — Read CACHE + 0x08 and clear any bypass bits.

4. **Try calling `Cache_FLASH_MMU_Set`** — The linker script defines `Cache_FLASH_MMU_Set = 0x4fc00518` as verified for ECO2. Call it from assembly with correct args to establish the IROM → flash mapping, then jump to `_start`.

---

### Build Workflow Reminder

`bundling/` files are a mirror — edits there have **no effect** until manually copied to the TinyGo installation:

```bash
cp bundling/src/device/esp/esp32p4.S /opt/homebrew/Cellar/tinygo/0.41.1/src/device/esp/esp32p4.S
cp bundling/targets/esp32p4.ld       /opt/homebrew/Cellar/tinygo/0.41.1/targets/esp32p4.ld
cp bundling/src/runtime/runtime_esp32p4.go /opt/homebrew/Cellar/tinygo/0.41.1/src/runtime/runtime_esp32p4.go
```

Then: `make build && make flash`.

---

## Open Tasks

### Current Error

```
Guru Meditation Error: Core 0 panic'ed (Illegal instruction)
PC      : 0x40006b9e  RA      : 0x4ff48cf4  SP      : 0x4ff42000  GP      : 0x4ff42ba0
T0      : 0x3ff10000  T1      : 0x00000000  T2      : 0xffefffff
MTVAL   : 0x00000000
```

The CPU fetches `0x0000` (C.UNIMP) from VMA `0x40006b9e` (first instruction of `main()`) every boot. All cache control registers are confirmed correct. The firmware binary has the right bytes (`0x41 0x11`) at flash file offset `0x4b9e`. The L2 cache MMU mapping from the IROM window to flash is the prime suspect.

### Tasks

- [ ] **Read L2 cache MMU entry 0** — In `call_start_cpu0`, before jumping to `_start`, write index `0` to `SPI_MEM_MMU_ITEM_INDEX` (SPI0 + 0x380 = `0x5008c380`), read back via `SPI_MEM_MMU_ITEM_CONTENT` (SPI0 + 0x37C = `0x5008c37C`), and print the value to UART0 TX FIFO (`0x500ca000`). Expected: `0x80000000` (valid bit set, page 0). If `0x00000000`, the ROM did not set up the IROM MMU mapping.

- [ ] **Read L2 cache MMU entry 1** — Same as above with index `1`. Expected: `0x80000001` (valid + page 1) to cover IROM VMA `0x40010000–0x4001FFFF`.

- [ ] **Manually write MMU entries if missing** — If entries are 0, add writes in `call_start_cpu0` immediately after reading:

  ```asm
  // SPI0 base = 0x5008c000
  lui  t3, 0x5008c      // t3 = 0x5008c000
  sw   zero, 0x380(t3)  // MMU_ITEM_INDEX = 0
  lui  t4, 0x80000      // t4 = 0x80000000 (valid bit)
  sw   t4, 0x37c(t3)    // MMU_ITEM_CONTENT = 0x80000000 (page 0)
  li   t4, 1
  sw   t4, 0x380(t3)    // MMU_ITEM_INDEX = 1
  lui  t4, 0x80000
  ori  t4, t4, 1
  sw   t4, 0x37c(t3)    // MMU_ITEM_CONTENT = 0x80000001 (page 1)
  ```

- [ ] **Check `L1_BYPASS_CACHE_CONF`** — Read CACHE + 0x08 (`0x3ff10008`) in `call_start_cpu0` and clear any set bits. If L1 instruction bypass is on and L2 has no MMU mapping, fetches return `0x0000` silently.

- [ ] **Verify MMU page size matches flash layout** — The expected mapping assumes 64 KB pages and that VMA `0x40000000` aligns to flash physical `0x0000`. Confirm this by checking the `L2_CACHE_BLOCKSIZE_CONF` register (CACHE + 0x27C = `0x3ff1027C`) — value should be `4` (2^(4+12) = 64 KB pages). If it is a different value, recalculate page numbers accordingly.

- [ ] **Remove diagnostic `rawput` checkpoints from runtime** — Once the boot crash is resolved and the serial markers (`>`, `W`, `B`, `V`, `T`, `I`) appear, remove them from `bundling/src/runtime/runtime_esp32p4.go` and copy the cleaned file to the TinyGo installation before the final build.

- [ ] **Verify HTTP API after full boot** — Once `main()` executes, confirm the Redfish endpoint responds:

  ```bash
  curl http://<device-ip>/healthz
  curl http://<device-ip>/redfish/v1/Systems/1
  ```
