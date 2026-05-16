#!/usr/bin/env python3
"""flash-esp32p4.py  —  ESP32-P4 ELF-to-image converter and esptool flasher.

Usage:
  python flash-esp32p4.py <firmware.elf> <port>                 # convert + flash
  python flash-esp32p4.py <firmware.elf> --image-only <out.bin> # convert only

WHY THIS EXISTS — THE ZERO-ADDRESS PAD SEGMENT BUG
===================================================
esptool's built-in ``elf2image`` command always emits RAM segments before the
IROM (flash-mapped) segment.  To satisfy the 64 KB MMU alignment constraint
before IROM, it inserts a padding segment whose load_addr is 0x00000000.

On the original ESP32 (Xtensa), the ROM bootloader treats ``load_addr == 0``
as "this segment is just alignment padding, skip it".  The ESP32-P4 ECO2 ROM
bootloader does NOT implement this convention — it tries to DMA-copy the
padding data to physical address 0x00000000, which is not writable, causing:

    Store/AMO access fault (MCAUSE=0x38000007, MTVAL=0x00000000)

THIS CONVERTER AVOIDS THE PROBLEM ENTIRELY by placing the IROM segment first
in the binary (at file offset 0x20, immediately after the 24-byte image
header + 8-byte segment header).  With the firmware at flash offset 0x2000
(where the P4 ROM bootloader reads from), the MMU alignment constraint is:

    (file_offset + 0x2000) % 0x10000 == vaddr % 0x10000
    (0x20       + 0x2000) % 0x10000 == 0x40002020 % 0x10000
              0x2020                ==     0x2020  ✓

No pre-IROM padding is needed at all.  SRAM segments follow IROM; they have
no alignment constraint beyond being valid SRAM addresses.

This matches the intent of the ``.text_dummy`` (NOLOAD) section in
``bundling/targets/esp32p4.ld``, which advances IROM VMA by
0x2000 + 0x18 + 0x08 = 0x2020 specifically for this on-flash layout.

IMAGE FORMAT
============
The output is a standard ESP32 binary image (magic=0xE9):
  [8-byte common header]  magic, seg_count, flash_mode, flash_sz_freq, entry
  [16-byte extended hdr]  wp_pin, drive strengths, chip_id=18, append_digest
  [IROM segment]          header (8 bytes) + .text data
  [RAM segments]          header (8 bytes) + .data/.init/... data  (merged)
  [XOR checksum padding]  zero bytes to align body to 16 bytes
  [XOR checksum byte]     XOR of all segment data bytes, seed 0xEF
  [32-byte SHA-256]       sha256 over everything above

FLASHING
========
Uses esptool's Python API (detect_chip / attach_flash / write_flash) to flash
the image at flash offset 0x2000.
"""

import hashlib
import struct
import sys

# ── constants ───────────────────────────────────────────────────────────────

FLASH_OFFSET = 0x2000      # P4 ROM reads the firmware image from flash 0x2000
CHIP_ID = 18               # chip_id for ESP32-P4 in the extended image header
FLASH_MODE = 0x02          # DIO
FLASH_SZ_FREQ = 0x0F       # 80 MHz / keep existing flash size setting

IROM_LOW = 0x40000000      # inclusive start of instruction-bus MMU window
IROM_HIGH = 0x4C000000     # exclusive end

SHT_PROGBITS = 1
SHF_ALLOC = 2
CHECKSUM_SEED = 0xEF

# ── ELF parser ───────────────────────────────────────────────────────────────


def parse_elf(elf_data: bytes):
    """Return ``(entry_point, [(vaddr, data), ...])`` for every ALLOC
    PROGBITS section that has non-zero size and a non-null address.

    Only SHT_PROGBITS sections (i.e. sections with actual data bytes in the
    file) are returned.  SHT_NOBITS sections (.bss, .stack) are excluded
    because the ROM bootloader zero-initialises those implicitly.
    """
    (_, _, _, e_entry, _, e_shoff, _,
     _, _, _, e_shentsize, e_shnum, e_shstrndx
     ) = struct.unpack_from('<HHIIIIIHHHHHH', elf_data, 16)

    # Read the section-name string table
    shstr_hdr = struct.unpack_from('<IIIIIIIIII',
                                   elf_data,
                                   e_shoff + e_shstrndx * e_shentsize)
    shstr_off = shstr_hdr[4]

    sections = []
    for i in range(e_shnum):
        sh = struct.unpack_from('<IIIIIIIIII',
                                elf_data,
                                e_shoff + i * e_shentsize)
        sh_name, sh_type, sh_flags, sh_addr, sh_offset, sh_size = sh[:6]

        if sh_type != SHT_PROGBITS:
            continue
        if not (sh_flags & SHF_ALLOC):
            continue
        if sh_size == 0 or sh_addr == 0:
            continue

        sections.append((sh_addr, elf_data[sh_offset:sh_offset + sh_size]))

    return e_entry, sections


# ── segment helpers ──────────────────────────────────────────────────────────


def _is_irom(addr: int) -> bool:
    return IROM_LOW <= addr < IROM_HIGH


def _pad4(data: bytes) -> bytes:
    """Pad *data* to a 4-byte boundary with zero bytes.

    The ESP32-P4 ECO2 ROM validates that every segment length is divisible by
    4.  Segments that fail this check are rejected with "Invalid image block".
    """
    rem = len(data) % 4
    return data if rem == 0 else data + b'\x00' * (4 - rem)


def _merge_adjacent(sections: list) -> list:
    """Sort by vaddr and merge immediately adjacent sections into one
    contiguous segment (as esptool does for the final image).

    The merged segment data is padded to a 4-byte boundary so the ROM's
    segment-length alignment check succeeds.
    """
    sections = sorted(sections, key=lambda x: x[0])
    merged: list = []
    for addr, data in sections:
        if merged and merged[-1][0] + len(merged[-1][1]) == addr:
            merged[-1] = (merged[-1][0], merged[-1][1] + data)
        else:
            merged.append((addr, bytearray(data)))
    return [(a, _pad4(bytes(d))) for a, d in merged]


# ── image builder ────────────────────────────────────────────────────────────


def build_image(elf_path: str) -> bytes:
    """Convert an ELF firmware file into an ESP32-P4 flashable binary image.

    Layout guarantee: IROM segment is always emitted first (file offset 0x20),
    immediately after the 24-byte image header.  This satisfies the 64 KB MMU
    alignment constraint for BOOTLOADER_FLASH_OFFSET=0x2000 with zero padding.
    """
    with open(elf_path, 'rb') as f:
        elf_data = f.read()

    entry, sections = parse_elf(elf_data)

    irom_secs = [(a, d) for a, d in sections if _is_irom(a)]
    ram_secs = [(a, d) for a, d in sections if not _is_irom(a)]

    # IROM first — satisfies MMU alignment without a padding segment.
    # RAM segments follow in address order, no alignment constraint.
    segments = _merge_adjacent(irom_secs) + _merge_adjacent(ram_secs)
    seg_count = len(segments)

    # Build the raw segment payload: [8-byte header][data] per segment
    segs_bytes = b''.join(
        struct.pack('<II', addr, len(data)) + data
        for addr, data in segments
    )

    # XOR checksum over all segment *data* bytes (headers excluded), seed 0xEF
    checksum = CHECKSUM_SEED
    for _, data in segments:
        for b in data:
            checksum ^= b

    # Extended 16-byte header (present on ESP32-S2 and later)
    ext = bytearray(16)
    ext[0] = 0xFF                        # wp_pin disabled
    struct.pack_into('<H', ext, 4, CHIP_ID)  # chip_id = 18
    ext[15] = 1                          # append_digest = 1 → SHA-256 appended

    # 8-byte common header
    hdr = struct.pack('<BBBBI',
                      0xE9,          # magic
                      seg_count,
                      FLASH_MODE,
                      FLASH_SZ_FREQ,
                      entry)

    body_core = hdr + bytes(ext) + segs_bytes

    # Pad so that (total body length including the XOR byte) % 16 == 0
    pad_len = (16 - (len(body_core) + 1) % 16) % 16
    body = body_core + b'\x00' * pad_len + bytes([checksum])

    # Append SHA-256 over the entire body
    return body + hashlib.sha256(body).digest()


# ── flash logic ──────────────────────────────────────────────────────────────


def flash(port: str, image: bytes) -> None:
    """Write *image* to flash at FLASH_OFFSET using esptool's Python API."""
    from esptool.cmds import attach_flash, detect_chip, write_flash

    print(f"Connecting to {port}…")
    with detect_chip(port, connect_mode="default-reset") as esp:
        attach_flash(esp)
        write_flash(esp, [(FLASH_OFFSET, image)], compress=True)
    print("Flash complete.")


# ── CLI ──────────────────────────────────────────────────────────────────────


def _print_image_summary(image: bytes) -> None:
    seg_count = image[1]
    entry = struct.unpack_from('<I', image, 4)[0]
    print(f"  magic=0xe9  segments={seg_count}  entry={entry:#010x}")
    offset = 24
    for i in range(seg_count):
        addr, length = struct.unpack_from('<II', image, offset)
        flag = " ← IROM (flash-mapped)" if _is_irom(addr) else " ← RAM (DMA)"
        if addr == 0:
            flag = " ← WARNING: zero-address pad (should not exist)"
        print(f"  seg[{i}]  load={addr:#010x}  len={length:#07x}{flag}")
        offset += 8 + length
    print(f"  total image size: {len(image)} bytes")


def main() -> None:
    if len(sys.argv) not in (3, 4):
        print(__doc__, file=sys.stderr)
        print("Usage:", file=sys.stderr)
        print("  flash-esp32p4.py <firmware.elf> <port>", file=sys.stderr)
        print("  flash-esp32p4.py <firmware.elf> --image-only <out.bin>",
              file=sys.stderr)
        sys.exit(1)

    elf_path = sys.argv[1]
    image = build_image(elf_path)

    if sys.argv[2] == '--image-only':
        if len(sys.argv) != 4:
            print("--image-only requires an output path", file=sys.stderr)
            sys.exit(1)
        out_path = sys.argv[3]
        with open(out_path, 'wb') as f:
            f.write(image)
        print(f"Written → {out_path}")
        _print_image_summary(image)
    else:
        port = sys.argv[2]
        # Also write the .bin alongside the .elf for inspection
        out_bin = elf_path.removesuffix('.elf') + '.bin'
        with open(out_bin, 'wb') as f:
            f.write(image)
        print(f"Image → {out_bin}")
        _print_image_summary(image)
        flash(port, image)


if __name__ == '__main__':
    main()
