//go:build tinygo

package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tinywasm/fmt"
	"tinygo.org/x/drivers/netlink"

	"github.com/bmcpi/esp32-p4-kvm/pkg/ethernet"
	"github.com/bmcpi/esp32-p4-kvm/pkg/power"
)

type ResetRequest struct {
	ResetType string `json:"ResetType"`
}

const (
	resetActionTokenHeader  = "X-BMC-Reset-Token"
	resetActionMaxBodyBytes = 128
)

var (
  // Set at build time (for example with -ldflags "-X api.configuredResetAuthToken=<token>").
	configuredResetAuthToken string
	powerActionOnce          sync.Once
	// Allow one queued command while one is executing; additional requests are rejected.
	powerActionQueue = make(chan time.Duration, 1)
)

// Configure sets the reset authentication token. Call this from main (where
// the token is injected via -ldflags) before StartPowerActionWorker or
// StartAPIServer.
func Configure(token string) {
	configuredResetAuthToken = token
}

func StartAPIServer() {
	link, _ := ethernet.Probe()
	if err := link.NetConnect(&netlink.ConnectParams{}); err != nil {
		fmt.Printf("Network connect failed: %s\n", err)
		return
	}

	// go serial.StartSerialTerminal()

	http.HandleFunc("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", handlePowerReset)
	http.HandleFunc("/redfish/v1/Systems/1", handleSystemStatus)
	http.HandleFunc("/healthz", handleHealthz)
	http.HandleFunc("/", handleNotFound)

	fmt.Println("BMC API starting on port 80...")
	if err := http.ListenAndServe(":80", nil); err != nil {
		fmt.Printf("HTTP server failed: %s\n", err)
	}
}

func handlePowerReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if !authorizePowerReset(w, r) {
		return
	}

	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, resetActionMaxBodyBytes)

	var req ResetRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var duration time.Duration
	switch req.ResetType {
	case "On", "GracefulShutdown":
		duration = 500 * time.Millisecond
	case "ForceOff":
		duration = 6 * time.Second
	default:
		http.Error(w, "Invalid ResetType", http.StatusBadRequest)
		return
	}

	if !enqueuePowerAction(duration) {
		http.Error(w, "Power action busy", http.StatusTooManyRequests)
		return
	}

	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(map[string]string{"Status": "Accepted"}); err != nil {
		fmt.Printf("failed to encode reset action response: %s\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if _, err := w.Write(payload.Bytes()); err != nil {
		fmt.Printf("failed to write reset action response: %s\n", err)
		return
	}
}

func handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	powerState := "Off"
	if !power.Sense.Get() {
		powerState = "On"
	}

	response := map[string]string{
		"Id":         "1",
		"Name":       "Managed Host",
		"PowerState": powerState,
	}

	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(response); err != nil {
		fmt.Printf("failed to encode system status response: %s\n", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload.Bytes()); err != nil {
		fmt.Printf("failed to write system status response: %s\n", err)
		return
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
		fmt.Printf("failed to write healthz response: %s\n", err)
	}
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Not Found", http.StatusNotFound)
}

func authorizePowerReset(w http.ResponseWriter, r *http.Request) bool {
	if configuredResetAuthToken == "" {
		http.Error(w, "Reset action disabled", http.StatusServiceUnavailable)
		return false
	}

	token := r.Header.Get(resetActionTokenHeader)
	if subtle.ConstantTimeCompare([]byte(token), []byte(configuredResetAuthToken)) != 1 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}

	return true
}

func StartPowerActionWorker() {
	powerActionOnce.Do(func() {
		go func() {
			for duration := range powerActionQueue {
				fmt.Printf("Executing power action for %s\n", duration)
				power.PressButton(power.Button, duration)
			}
		}()
	})
}

func enqueuePowerAction(duration time.Duration) bool {
	select {
	case powerActionQueue <- duration:
		return true
	default:
		return false
	}
}
