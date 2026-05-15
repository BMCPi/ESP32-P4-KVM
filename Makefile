.PHONY: clean-screen build flash terminal cross-compile all

# Dynamically find ESP32 USB port
CONSOLE_PORT ?= $(shell ls /dev/cu.usb* 2>/dev/null | head -1)

# Clean up detached screen sessions to prevent blocking
clean-screen:
	@echo "Cleaning up detached screen sessions..."
	sudo screen -wipe || true
	sudo screen -ls | grep Detached | cut -d. -f1 | awk '{print $$1}' | xargs -I {} sudo screen -X -S {} quit || true

# Build firmware with TinyGo
build:
	@echo "Building ESP32-P4 firmware..."
	tinygo build -target esp32p4 -ldflags="-X main.configuredResetAuthToken=change-me" -o firmware.bin .

# Alias for build (cross-compile for consistency with workspace tasks)
cross-compile: build

# Flash firmware to device (clean screen first)
flash: clean-screen
	@echo "Flashing ESP32-P4 firmware on $(CONSOLE_PORT)..."
	tinygo flash -target esp32p4 -port $(CONSOLE_PORT) .

# Open serial console and auto-disconnect after 30 lines
terminal:
	@if [ -z "$(CONSOLE_PORT)" ]; then \
		echo "No /dev/cu.usbmodem* console device found."; \
		exit 1; \
	fi
	@echo "Opening console on $(CONSOLE_PORT) (115200), stopping after 30 lines..."
	sudo screen $(CONSOLE_PORT) 115200 | head -n 30 || true

# Build and flash in one command
all: build flash
