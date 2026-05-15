# ESP32-P4-KVM

TinyGo firmware skeleton for ESP32-P4 ETH POE boards with:

- Redfish-style HTTP API subset
- GPIO-based host power control
- SD card initialization for virtual media
- Virtual media USB MSC bridge integration point

## Files

- `main.go`: startup orchestration and GPIO/button logic
- `api.go`: HTTP API handlers under `/redfish/v1/Systems/1`
- `storage.go`: SD card initialization using `tinygo.org/x/drivers/sdcard`
- `virtual_media.go`: USB MSC bridge stub for ESP-IDF TinyUSB integration

## Build and flash

```bash
# TinyGo 0.41.1 does not include an `esp32p4` target yet.
# Use a concrete ESP32 target that exists in your TinyGo install.
tinygo build -target esp32p4 -ldflags="-X main.configuredResetAuthToken=change-me" -o firmware.bin .
tinygo flash -target esp32p4 .
```

### Feature target matrix

| Feature | esp32s3-generic | rp2040 (pico) | atsamd51 | nrf52840 |
|---------|----------------|---------------|----------|----------|
| GPIO / HTTP API | ✓ | ✓ | ✓ | ✓ |
| SD card (sdcard driver) | ✓ | ✓ | ✓ | ✓ |
| LittleFS (`tinygo.org/x/tinyfs`) | ✗ (no `__wrap_malloc`) | ✓ | ✓ | ✓ |
| USB MSC (`machine/usb/msc`) | ✗ (stub) | ✓ | ✓ | ✓ |

The full virtual-media stack (LittleFS + USB MSC) compiles and runs on rp2040/pico.
When an ESP32-P4 target is added to TinyGo, both features are expected to work.

## API examples

```bash
curl http://<board-ip>/redfish/v1/Systems/1

curl -X POST \
  -H 'Content-Type: application/json' \
  -H 'X-BMC-Reset-Token: change-me' \
  -d '{"ResetType":"ForceOff"}' \
  http://<board-ip>/redfish/v1/Systems/1/Actions/ComputerSystem.Reset
```
