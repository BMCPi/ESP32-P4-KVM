//go:build tinygo

package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type ResetRequest struct {
	ResetType string `json:"ResetType"`
}

const (
	resetActionTokenHeader   = "X-BMC-Reset-Token"
	resetActionMaxBodyBytes  = 128
	configuredResetAuthToken = ""
)

var (
	powerActionOnce  sync.Once
	powerActionQueue chan time.Duration
)

func startAPIServer() {
	if err := initEthernet(); err != nil {
		fmt.Printf("Ethernet initialization failed: %s\n", err)
		return
	}

	http.HandleFunc("/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", handlePowerReset)
	http.HandleFunc("/redfish/v1/Systems/1", handleSystemStatus)

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

	w.WriteHeader(http.StatusAccepted)
}

func handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	powerState := "Off"
	if !sensePin.Get() {
		powerState = "On"
	}

	response := map[string]string{
		"Id":         "1",
		"Name":       "Managed Host",
		"PowerState": powerState,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		fmt.Printf("failed to write system status response: %s\n", err)
		return
	}
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

func startPowerActionWorker() {
	powerActionOnce.Do(func() {
		powerActionQueue = make(chan time.Duration, 1)
		go func() {
			for duration := range powerActionQueue {
				fmt.Printf("Executing power action for %s\n", duration)
				pressButton(pwrButton, duration)
			}
		}()
	})
}

func enqueuePowerAction(duration time.Duration) bool {
	startPowerActionWorker()

	select {
	case powerActionQueue <- duration:
		return true
	default:
		return false
	}
}
