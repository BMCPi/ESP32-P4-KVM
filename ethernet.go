//go:build tinygo

package main

import (
	"machine"
	"net"
	"time"

	"github.com/tinywasm/fmt"
)

func initEthernet() error {
	fmt.Println("Initializing Ethernet interface...")
	// 1. Initialize the PHY power/reset if necessary
	// On many P4 boards, the PHY reset is tied to a GPIO
	phyReset := machine.GPIO35
	phyReset.Configure(machine.PinConfig{Mode: machine.PinOutput})
	phyReset.Low()
	time.Sleep(100 * time.Millisecond)
	phyReset.High()

	// 2. Configure the Ethernet MAC
	// TinyGo's machine package for ESP32-P4 defines the EthConfig
	cfg := machine.EthConfig{
		MDC:        machine.GPIO31,
		MDIO:       machine.GPIO32,
		TXD0:       machine.GPIO47,
		TXD1:       machine.GPIO48,
		TXEN:       machine.GPIO49,
		RXD0:       machine.GPIO41,
		RXD1:       machine.GPIO42,
		CRSDV:      machine.GPIO40,
		ClockMode:  machine.EthClockExternal, // Usually external 50MHz on P4-ETH
		PHYAddress: 1,                        // Default for most LAN8720/IP101 setups
	}

	err := machine.InitEthernet(cfg)
	if err != nil {
		println("Ethernet init failed:", err.Error())
		return err
	}

	// 3. Bring up the network interface
	// This usually triggers the DHCP client
	waitNet()

	return nil
}

func waitNet() {
	for {
		addr, _ := net.InterfaceByName("eth0")
		if addr != nil {
			addrs, _ := addr.Addrs()
			if len(addrs) > 0 {
				println("Connected! IP:", addrs[0].String())
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
}
