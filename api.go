//go:build tinygo

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type ResetRequest struct {
	ResetType string `json:"ResetType"`
}

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

	defer r.Body.Close()

	var req ResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	switch req.ResetType {
	case "On", "GracefulShutdown":
		fmt.Println("Triggering short press...")
		pressButton(pwrButton, 500*time.Millisecond)
	case "ForceOff":
		fmt.Println("Triggering long press...")
		pressButton(pwrButton, 6*time.Second)
	default:
		http.Error(w, "Invalid ResetType", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}
