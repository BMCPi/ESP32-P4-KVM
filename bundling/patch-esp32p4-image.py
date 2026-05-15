#!/usr/bin/env python3
"""Patch a TinyGo-produced ESP32 image so the ROM bootloader on an ESP32-P4
(ImageChipID = 18) accepts it.

TinyGo's `esp32` binary-format writes ImageChipID = 0 (original ESP32) into
the image header.  The ESP32-P4 ROM loader rejects any chip_id that is not 18:

    Invalid chip id. Expected 18 read 0. Bootloader for wrong chip?

This script rewrites the image header in place so that:

  * byte 12 (chip_id)  = 18  (ESP32-P4)
  * byte 13 (chip_rev) = 0   (any/unspecified; ROM accepts when min_rev unset)

If byte 23 bit 0 is set (`hash_appended`), the trailing 32-byte SHA-256
checksum is recomputed over the new payload so the ROM image hash check
still passes.

Usage:
    patch-esp32p4-image.py <path-to-firmware.bin>

The file is patched in place. Running it twice is a no-op.

This is intentionally dependency-free (stdlib only) so it works wherever
TinyGo runs. See ESP-IDF `components/esptool_py/esptool/esptool/bin_image.py`
for the canonical image header layout used here.
"""

from __future__ import annotations

import hashlib
import sys
from pathlib import Path

ESP32P4_CHIP_ID = 18
IMAGE_MAGIC = 0xE9
HEADER_LEN = 24
HASH_LEN = 32


def patch(path: Path) -> bool:
    data = bytearray(path.read_bytes())
    if len(data) < HEADER_LEN + HASH_LEN:
        raise SystemExit(f"{path}: too small to be an ESP image ({len(data)} bytes)")
    if data[0] != IMAGE_MAGIC:
        raise SystemExit(
            f"{path}: bad magic 0x{data[0]:02x} (expected 0x{IMAGE_MAGIC:02x})"
        )

    changed = False
    if data[12] != ESP32P4_CHIP_ID:
        data[12] = ESP32P4_CHIP_ID
        changed = True
    if data[13] != 0:
        data[13] = 0
        changed = True

    # If hash_appended is set, recompute the trailing SHA-256.
    if data[23] & 0x01:
        new_hash = hashlib.sha256(bytes(data[:-HASH_LEN])).digest()
        if data[-HASH_LEN:] != new_hash:
            data[-HASH_LEN:] = new_hash
            changed = True

    if changed:
        path.write_bytes(bytes(data))
    return changed


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(f"usage: {argv[0]} <firmware.bin>", file=sys.stderr)
        return 2
    path = Path(argv[1])
    changed = patch(path)
    print(f"{'patched' if changed else 'already correct'}: {path} (chip_id=18)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
