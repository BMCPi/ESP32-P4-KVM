//go:build esp32p4

package machine

// ADC stub for ESP32-P4.
// The ESP32-P4-KVM project does not use ADC; these are no-op stubs
// so that code referencing machine.ADC compiles for this target.

// InitADC initializes the ADC peripheral (no-op stub).
func InitADC() {}

// Configure sets up the ADC pin configuration (no-op stub).
func (a ADC) Configure(config ADCConfig) error { return nil }

// Get returns an ADC reading (always 0; not implemented for this target).
func (a ADC) Get() uint16 { return 0 }
