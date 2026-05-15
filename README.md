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
tinygo build -target esp32p4 -o firmware.bin .
tinygo flash -target esp32p4 .
```

## API examples

```bash
curl http://<board-ip>/redfish/v1/Systems/1

curl -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType":"ForceOff"}' \
  http://<board-ip>/redfish/v1/Systems/1/Actions/ComputerSystem.Reset
```
