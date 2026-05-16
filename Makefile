.PHONY: clean-screen build flash terminal cross-compile all apply-overrides

# Dynamically find ESP32 USB port
CONSOLE_PORT ?= $(shell ls /dev/cu.usb* 2>/dev/null | head -1)

# Location of the TinyGo install that we patch with project-local
# overrides. Resolved dynamically so the targets keep working across
# `brew upgrade tinygo`.
TINYGO_ROOT  ?= $(shell tinygo env TINYGOROOT)

# apply-overrides copies every file under bundling/ to its mirror
# destination inside the TinyGo install.  This makes builds self-healing:
# a `brew upgrade tinygo` no longer silently regresses the firmware to
# broken upstream sources.
#
# Override list (kept in lockstep with bundling/ contents):
#   bundling/src/device/esp/esp32p4.S       → $TINYGOROOT/src/device/esp/esp32p4.S
#       Custom call_start_cpu0: programs the L2 cache FLASH MMU directly
#       (BIT(12) = SOC_MMU_FLASH_VALID) and invalidates caches before
#       jumping to _start.  The ECO2 ROM does not leave the IROM mapping
#       in a state the hardware understands.
#   bundling/targets/esp32p4.ld             → $TINYGOROOT/targets/esp32p4.ld
#       Sets __global_pointer$ and keeps .rodata in SRAM (the ECO2 ROM
#       cannot DMA-load segments into the 0x44000000 DROM window).
#   bundling/src/runtime/runtime_esp32p4.go → $TINYGOROOT/src/runtime/runtime_esp32p4.go
#       Disables every watchdog via direct register writes.
#
# pkg/sdcard/ is a vendored fork of tinygo.org/x/drivers/sdcard@v0.31.0
# carrying one fix (Device.cmd reuses cmdbuf in its 65535-iteration
# response poll instead of allocating []byte{0xFF} per loop, which
# overruns TinyGo's GC when no card is attached).  It is imported
# directly by pkg/storage so no apply-overrides step is needed.
apply-overrides:
	@if [ -z "$(TINYGO_ROOT)" ]; then echo "TinyGo not found on PATH"; exit 1; fi
	@echo "Patching TinyGo install at $(TINYGO_ROOT)..."
	@install -m 644 bundling/src/device/esp/esp32p4.S      "$(TINYGO_ROOT)/src/device/esp/esp32p4.S"
	@install -m 644 bundling/targets/esp32p4.ld            "$(TINYGO_ROOT)/targets/esp32p4.ld"
	@install -m 644 bundling/src/runtime/runtime_esp32p4.go "$(TINYGO_ROOT)/src/runtime/runtime_esp32p4.go"

# Clean up ALL screen sessions (Attached or Detached) to free serial port
clean-screen:
	@echo "Cleaning up screen sessions..."
	sudo screen -wipe || true
	sudo screen -ls | grep -E 'Attached|Detached' | cut -d. -f1 | awk '{print $$1}' | xargs -I {} sudo screen -X -S {} quit || true

# Build firmware with TinyGo (ELF output) and convert to flashable .bin.
# Uses bundling/flash-esp32p4.py (custom converter) instead of esptool's
# built-in elf2image, which emits a zero-address alignment-pad segment that
# crashes the ESP32-P4 ECO2 ROM bootloader.
build: apply-overrides
	@echo "Building ESP32-P4 firmware (ELF)..."
	tinygo build -target esp32p4 -ldflags="-X api.configuredResetAuthToken=change-me" -o firmware.elf .
	@echo "Converting ELF to ESP32-P4 image..."
	.venv/bin/python bundling/flash-esp32p4.py firmware.elf --image-only firmware.bin

build-demo: apply-overrides
	@echo "Building ESP32-P4 demo firmware (ELF)..."
	tinygo build -target esp32p4 -o demo.elf ./cmd/demo
	@echo "Converting ELF to ESP32-P4 image..."
	.venv/bin/python bundling/flash-esp32p4.py demo.elf --image-only demo.bin

# Alias for build (cross-compile for consistency with workspace tasks)
cross-compile: build

# Flash firmware to device (clean screen first, retry up to 10× for WDT boot-loop churn)
flash: build clean-screen
	@if [ -z "$(CONSOLE_PORT)" ]; then echo "No /dev/cu.usb* device found."; exit 1; fi
	@echo "Flashing ESP32-P4 firmware on $(CONSOLE_PORT)..."
	@for i in $$(seq 1 10); do \
		echo "Flash attempt $$i/10..."; \
		.venv/bin/python bundling/flash-esp32p4.py firmware.elf $(CONSOLE_PORT) && exit 0; \
		echo "Attempt $$i failed, retrying in 1s..."; \
		sleep 1; \
	done; exit 1

flash-demo: build-demo clean-screen
	@if [ -z "$(CONSOLE_PORT)" ]; then echo "No /dev/cu.usb* device found."; exit 1; fi
	@echo "Flashing ESP32-P4 demo firmware on $(CONSOLE_PORT)..."
	@for i in $$(seq 1 10); do \
		echo "Flash attempt $$i/10..."; \
		.venv/bin/python bundling/flash-esp32p4.py demo.elf $(CONSOLE_PORT) && exit 0; \
		echo "Attempt $$i failed, retrying in 1s..."; \
		sleep 1; \
	done; exit 1

# Open serial console and stream for 10 seconds
terminal:
	@if [ -z "$(CONSOLE_PORT)" ]; then \
		echo "No /dev/cu.usb* console device found."; \
		exit 1; \
	fi
	@echo "Streaming $(CONSOLE_PORT) at 115200 for 10 seconds..."
	@stty -f "$(CONSOLE_PORT)" 115200 cs8 -cstopb -parenb cread clocal raw -echo
	@cat "$(CONSOLE_PORT)" & CPID=$$!; sleep 10; kill $$CPID 2>/dev/null; wait $$CPID 2>/dev/null; true

# Build and flash in one command
all: build flash
