# Copilot Instructions for ESP32-P4-KVM

## TinyGo Only

**Every source file in this project uses `//go:build tinygo`.** This is a TinyGo firmware project — it is not compiled with standard Go and does not run on a host machine. Do not add `//go:build !tinygo` stubs, host shims, or any code intended to run outside of TinyGo. Standard Go tooling (`go build`, `go test`) will not work on this codebase and that is expected.

## Build, Test, and Lint Commands

### Build Firmware
```bash
# Requires TinyGo 0.41.1+
tinygo build -target esp32p4 -ldflags="-X api.configuredResetAuthToken=change-me" -o firmware.bin .
```

### Flash to Device
```bash
tinygo flash -target esp32p4 .
```

### Linting
```bash
golangci-lint run
```

Strict config in `.golangci.yml`. Note: golangci-lint runs standard Go and cannot type-check TinyGo-specific imports (e.g., `machine`); `typecheck` errors on `//go:build tinygo` files are expected and harmless.

### Testing
Testing is done on the board only. Flash the firmware and exercise the HTTP endpoints:
```bash
curl http://<device-ip>/healthz
curl http://<device-ip>/redfish/v1/Systems/1
```

## High-Level Architecture

### Build Tags
- **`//go:build tinygo`**: All source files use this tag — there are no host-only files.
- **`//go:build tinygo && esp32p4`**: ESP32-P4-specific code (virtual media USB MSC).

### Core Components

1. **API Server** (`api.go`)
   - HTTP server on port 80 using standard `net/http` (TinyGo's implementation, backed by the EMAC netdev)
   - `/redfish/v1/Systems/1`: System status and power state
   - `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset`: Power control (On, GracefulShutdown, ForceOff)
   - `/healthz`: Health check
   - Reset endpoint secured by `X-BMC-Reset-Token` header (token configured at build time via ldflags)
   - Power actions queued with capacity 1 (one executing, one pending max)
   - Uses `Probe()` + `link.NetConnect()` pattern (see `ethernet.go`) to bring up the network

2. **GPIO and Power Control** (`main.go`)
   - GPIO16: Power button output (open-drain mode on ESP32-P4)
   - GPIO17: Power sense input
   - `pressButton()`: Simulates button press (low for duration, then high)
   - Hardware-specific register access for GPIO (P4 uses `DR_REG_GPIO_BASE = 0x50110000`)

3. **Storage Subsystem** (`storage.go`, `storage_lfs.go`)
   - SD card (via SPI) partitioned into two regions:
     - **MSC (front)**: Exposed to remote machine over USB-C as raw disk
     - **LFS (tail)**: 64 MiB reserved for local firmware storage (LittleFS)
   - `PartitionDevice`: Wrapper providing block device interface over a byte range

4. **Virtual Media** (`virtual_media.go`)
   - USB Mass Storage Class (MSC) bridge connecting front SD partition to remote host
   - Build tag: `//go:build tinygo && esp32p4`

5. **Ethernet** (`ethernet.go`)
   - Implements `netdev.Netdever` (BSD socket API) and `netlink.Netlinker` (L2 connect) on top of `machine.DefaultEMAC`
   - Provides `Probe()` following the `tinygo.org/x/drivers/netlink/probe` pattern so `net/http` works
   - Minimal TCP/IP stack in Go: ARP responder, TCP server (SYN/SYN-ACK/ACK, data, FIN/RST)
   - Static IP configured via `ethernetIP` and `ethernetMAC` package vars at the top of the file
   - Up to 8 concurrent TCP connections via a socket table

6. **Memcache** (`memcache.go`)
   - Fixed-slot LRU cache backed by a pre-allocated byte arena
   - Designed to reside in PSRAM when the heap linker script is updated

7. **Serial Terminal** (`serial.go`)
   - Optional serial debugging interface; non-fatal if unavailable

### Initialization Order
1. GPIO setup (buttons, sense pins)
2. Power action worker goroutine
3. Storage initialization (SD card, LittleFS)
4. Serial terminal
5. API server goroutine: `Probe()` → `NetConnect()` → `http.ListenAndServe(":80", nil)`

## Key Conventions

### TinyGo Compatibility
- Use `github.com/tinywasm/fmt` for format strings — standard `fmt` is not fully supported in TinyGo
- All imports must be TinyGo-compatible; standard library packages that rely on OS syscalls are not available
- Do not use `go test`, host stubs, or `//go:build !tinygo` — there is no host compilation path

### Error Handling
- Non-fatal errors (storage, serial) are logged but do not block startup
- HTTP errors use `http.Error()` with standard status codes and JSON payloads

### Authentication
- Reset endpoint requires `X-BMC-Reset-Token` header
- Token configured at build time: `-X api.configuredResetAuthToken=<token>`
- Empty token disables reset action (returns 503)
- Comparison uses `crypto/subtle.ConstantTimeCompare`

### Concurrency
- Power actions queued in a buffered channel (`powerActionQueue`, capacity 1)
- `sync.Once` ensures the power action worker starts only once
- The Ethernet frame-dispatch goroutine runs for the lifetime of the firmware

### JSON Encoding
- Encode to a `bytes.Buffer` first (not directly to `http.ResponseWriter`) for cleaner error handling
- `json.Decoder.DisallowUnknownFields()` on all incoming request bodies

### GPIO and Registers
- Direct register manipulation via `unsafe.Pointer` to GPIO base (`DR_REG_GPIO_BASE = 0x50110000`)
- `PinOutputModeGPOpenDrain = 4` for the power button (open-drain)
- Power sense pin: inverse logic — low means power is on

## Build Configuration

### ldflags Variables
- `api.configuredResetAuthToken`: authentication token for the reset endpoint

### Go Module
- TinyGo 0.41.1+
- `tinygo.org/x/drivers` v0.31.0 — EMAC driver, netdev/netlink interfaces
- `github.com/tinywasm/fmt` — TinyGo-compatible fmt
- `tinygo.org/x/tinyfs` v0.5.0 — LittleFS

### Copyright Headers
The `goheader` linter is disabled for all files (excluded via `path-except: zz_generated.deepcopy.go`), so no copyright header is required on source files.
