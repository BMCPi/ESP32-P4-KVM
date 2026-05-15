//go:build tinygo

package serial

import (
	"io"
	"machine"
	"net"

	"github.com/tinywasm/fmt"
)

const (
	serialBaudRate uint32 = 115200
	serialTCPPort         = ":2222"

	// UART1 pins — adjust to match the PCB schematic.
	// These must not conflict with the SD-card SPI (GPIO10–13) or the
	// power-control pins (GPIO16/17) or the UART0 debug serial (GPIO43/44).
	serialTX machine.Pin = machine.GPIO4
	serialRX machine.Pin = machine.GPIO5
)

// serialUART is the UART peripheral wired to the remote machine's serial port.
var serialUART = machine.UART1

// initSerial configures UART1 at 115200 8N1. Call this once from main() before
// the network is started; the TCP listener is started separately by
// startSerialTerminal() once ethernet is up.
//
// Note: machine.UART.Configure has no error return on some TinyGo targets
// (e.g. ESP32), so errors are not propagated here.
func initSerial() error {
	serialUART.Configure(machine.UARTConfig{
		BaudRate: serialBaudRate,
		TX:       serialTX,
		RX:       serialRX,
	})
	return nil
}

// startSerialTerminal listens on serialTCPPort and bridges each incoming TCP
// connection to the remote machine's serial console over UART1. Clients
// connect with any raw-TCP terminal:
//
//	nc <bmc-ip> 2222
//	socat - TCP:<bmc-ip>:2222
func startSerialTerminal() {
	ln, err := net.Listen("tcp", serialTCPPort)
	if err != nil {
		fmt.Printf("Serial terminal: listen %s: %s\n", serialTCPPort, err)
		return
	}
	fmt.Printf("Serial terminal listening on %s (%d baud 8N1)\n", serialTCPPort, serialBaudRate)
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("Serial terminal: accept: %s\n", err)
			continue
		}
		go bridgeSerialConn(conn)
	}
}

// bridgeSerialConn relays data in both directions between conn and the remote
// machine's UART until the TCP client disconnects.
func bridgeSerialConn(conn net.Conn) {
	defer conn.Close()
	// TCP client → remote machine serial port.
	go io.Copy(serialUART, conn)
	// Remote machine serial port → TCP client.
	io.Copy(conn, serialUART)
}
