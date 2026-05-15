.PHONY: clean-screen build flash cross-compile all

# Dynamically find ESP32 USB port
SERIAL_PORT ?= $(shell ls /dev/cu.usb* 2>/dev/null | head -1)

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
	@echo "Flashing ESP32-P4 firmware on $(SERIAL_PORT)..."
	tinygo flash -target esp32p4 -port $(SERIAL_PORT) .

# Build and flash in one command
all: build flash
