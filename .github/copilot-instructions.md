# Copilot Instructions for ESP32-P4-KVM

## Build, Test, and Lint Commands

### Build Firmware
```bash
# Build TinyGo firmware for ESP32-P4-ETH by Waveshare with POE (requires TinyGo 0.41.1+)
tinygo build -target esp32p4 -ldflags="-X main.configuredResetAuthToken=change-me" -o firmware.bin .

# Flash to device
tinygo flash -target esp32p4 .
```

**This project is built exclusively for ESP32-P4-ETH by Waveshare with POE.** The `esp32p4` target is the only supported deployment target.

### Linting
```bash
# Run all linters (requires golangci-lint)
golangci-lint run

# Specific linters can be run individually (see .golangci.yml for enabled linters)
```

**Linting Configuration:** Strict config in `.golangci.yml` with custom rules for complexity (max-complexity: 37), copyright headers (Apache 2.0 / Tinkerbell), and struct tags. Exclusions apply to test files and generated code.

### Testing
```bash
# Run Go tests (host-only; firmware uses //go:build tinygo)
go test ./...
```

**Note:** There are currently no test files in the main package. Code can be tested on host via `host_stub.go` which provides stubs for TinyGo-only features (`!tinygo` build tag).

## High-Level Architecture

### Build Tags and Target Support
- **`//go:build tinygo`**: TinyGo firmware code (api.go, main.go, storage.go, serial.go, virtual_media.go, ethernet.go)
- **`//go:build !tinygo`**: Host-only code for testing (host_stub.go runs on standard Go)
- **Feature-gated code**: `//go:build tinygo && esp32p4` and `//go:build tinygo && esp32p4_ethernet` for ESP32-P4 board-specific features

This project only deploys to **ESP32-P4-ETH by Waveshare with POE** using the `esp32p4` target. The conditional compilation patterns exist to enable host-based testing (via `host_stub.go`) and to cleanly isolate board-specific hardware initialization, but there is no multi-target support.

### Core Components

1. **API Server** (`api.go`)
   - HTTP server on port 80 with Redfish-style endpoints
   - `/redfish/v1/Systems/1`: System status and power state
   - `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset`: Power control (On, GracefulShutdown, ForceOff)
   - `/healthz`: Health check
   - Reset endpoint secured by `X-BMC-Reset-Token` header (token configured at build time via ldflags)
   - Power actions queued with capacity 1 (one executing, one pending max)

2. **GPIO and Power Control** (`main.go`)
   - GPIO16: Power button output (open-drain mode on ESP32-P4)
   - GPIO17: Power sense input
   - `pressButton()`: Simulates button press (low for duration, then high)
   - Hardware-specific register access for GPIO (P4 uses `DR_REG_GPIO_BASE = 0x50110000`)

3. **Storage Subsystem** (`storage.go`, `storage_lfs.go`, `storage_lfs_stub.go`)
   - SD card (via SPI) partitioned into two regions:
     - **MSC (front)**: Exposed to remote machine over USB-C as raw disk
     - **LFS (tail)**: 64 MiB reserved for local firmware storage (LittleFS)
   - `PartitionDevice`: Wrapper providing block device interface over a byte range
   - Supports both MSC and LittleFS drivers via structural typing

4. **Virtual Media** (`virtual_media.go`, `virtual_media_stub.go`)
   - USB Mass Storage Class (MSC) bridge connecting front SD partition to remote host
   - Available only on esp32p4 target (`//go:build tinygo && esp32p4`)
   - Remote machine can format, write boot images, or boot directly from the disk

5. **Ethernet** (`ethernet.go`, `ethernet_stub.go`)
   - RMII PHY initialization with GPIO reset
   - Configurable via `machine.EthConfig` (P4 specific pins and external clock)
   - DHCP client integration via TinyGo's `machine.InitEthernet()`
   - Stub available for non-ethernet targets

6. **Serial Terminal** (`serial.go`, `serial_stub.go`)
   - Optional serial debugging interface
   - Initialized in `main()` with error handling (non-fatal)

### Initialization Order
1. GPIO setup (buttons, sense pins)
2. Power action worker goroutine
3. Storage initialization (SD card, LittleFS)
4. Serial terminal
5. API server (goroutine with Ethernet init)

## Key Conventions

### Error Handling
- Use `fmt.Errf()` for wrapping errors (from tinywasm/fmt)
- Non-fatal errors (storage, serial) are logged but don't block startup
- HTTP errors use standard `http.Error()` for status codes and JSON payloads

### Authentication
- Reset endpoint requires `X-BMC-Reset-Token` header
- Token is **configured at build time** via ldflags: `-X main.configuredResetAuthToken=<token>`
- Empty token disables reset action (returns 503 Service Unavailable)
- Token comparison uses constant-time comparison (`crypto/subtle.ConstantTimeCompare`)

### Concurrency
- Power actions are queued in a buffered channel (`powerActionQueue`) with capacity 1
- `sync.Once` ensures power action worker starts only once
- API handlers are thread-safe; use goroutines for long-running operations

### JSON Encoding
- Manual JSON encoding to bytes (not direct http.ResponseWriter) for better error handling
- `json.Decoder.DisallowUnknownFields()` enforces strict JSON parsing
- Power reset request validates no trailing data after JSON object

### GPIO and Registers
- Direct register manipulation for ESP32-P4 (`unsafe.Pointer` to GPIO registers)
- `PinOutputModeGPOpenDrain = 4` for power button (open-drain mode)
- Button press: low for duration (active), high for release
- Sense pin is input-only (inverse logic: low = power on)

### Conditional Compilation
- Code is organized with feature-gated implementations (MSC, Ethernet, Serial)
- Stub implementations (`*_stub.go`) allow host-based testing without TinyGo
- Example: `virtual_media.go` (esp32p4 MSC) vs `virtual_media_stub.go` (host stub)
- This project only supports ESP32-P4-ETH; do not add multi-target support

## Build Configuration

### ldflags Variables
- `main.configuredResetAuthToken`: Set authentication token for reset endpoint (e.g., `-X main.configuredResetAuthToken=my-secret-token`)

### Go Module
- Requires Go 1.26.2+
- TinyGo drivers: `tinygo.org/x/drivers` v0.31.0
- String formatting: `github.com/tinywasm/fmt` (TinyGo compatible)
- File system: `tinygo.org/x/tinyfs` v0.5.0

### Copyright Headers
All source files must include Apache 2.0 copyright header:
```go
// Copyright YYYY Tinkerbell.
// Licensed under the Apache License, Version 2.0...
```
This is enforced by golangci-lint's `goheader` linter.

## Testing Strategy

### Host Testing
- Use `host_stub.go` to provide stubs for TinyGo-only features
- Run `go test ./...` on host for non-TinyGo logic
- Current status: No test files yet (opportunity for expansion)

### Board Testing
- Flash firmware and test via HTTP endpoints
- Health check: `curl http://<ip>/healthz`
- System status: `curl http://<ip>/redfish/v1/Systems/1`
- Power control (requires token): See README.md for example

### Feature Support
ESP32-P4-ETH by Waveshare with POE supports all features:
- GPIO power control and sensing
- HTTP API (Redfish-style endpoints)
- SD card (SPI) with LittleFS + MSC partitioning
- Virtual media USB MSC bridge
- Ethernet with DHCP
