//go:build tinygo

package main

import (
	"machine"
)

// ─────────────────────────────────────────────────────────────
// Static network configuration — change to match your LAN.
// ─────────────────────────────────────────────────────────────

var myMAC = [6]byte{0x02, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE} // locally administered
var myIP = [4]byte{192, 168, 1, 100}
var myGW = [4]byte{192, 168, 1, 1}

// ─────────────────────────────────────────────────────────────
// Ethernet frame helpers
// ─────────────────────────────────────────────────────────────

const (
	etherTypeARP  = 0x0806
	etherTypeIPv4 = 0x0800
)

func etherDst(f []byte) []byte  { return f[0:6] }
func etherSrc(f []byte) []byte  { return f[6:12] }
func etherType(f []byte) uint16 { return uint16(f[12])<<8 | uint16(f[13]) }

func putMAC(dst []byte, src [6]byte) {
	dst[0] = src[0]; dst[1] = src[1]; dst[2] = src[2]
	dst[3] = src[3]; dst[4] = src[4]; dst[5] = src[5]
}

func putU16(b []byte, v uint16) { b[0] = byte(v >> 8); b[1] = byte(v) }
func getU16(b []byte) uint16    { return uint16(b[0])<<8 | uint16(b[1]) }
func putU32(b []byte, v uint32) {
	b[0] = byte(v >> 24); b[1] = byte(v >> 16); b[2] = byte(v >> 8); b[3] = byte(v)
}
func getU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func macEqual(a, b []byte) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] &&
		a[3] == b[3] && a[4] == b[4] && a[5] == b[5]
}

func ipEqual(a []byte, b [4]byte) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] && a[3] == b[3]
}

// ─────────────────────────────────────────────────────────────
// ARP
// ─────────────────────────────────────────────────────────────

// handleARP processes an incoming ARP frame (Ethernet payload starting at f[14]).
// Returns a reply frame to transmit, or nil if nothing to send.
func handleARP(f []byte) []byte {
	if len(f) < 14+28 {
		return nil
	}
	arp := f[14:] // ARP payload
	// Check: Ethernet HW type=1, IPv4=0x0800, HW len=6, proto len=4, op=1 (request)
	if getU16(arp[0:]) != 1 || getU16(arp[2:]) != 0x0800 ||
		arp[4] != 6 || arp[5] != 4 || getU16(arp[6:]) != 1 {
		return nil
	}
	targetIP := arp[24:28]
	if !ipEqual(targetIP, myIP) {
		return nil
	}

	// Build ARP reply (42 bytes total).
	var reply [42]byte
	// Ethernet header
	copy(reply[0:6], arp[8:14])  // dst = sender MAC
	putMAC(reply[6:12], myMAC)   // src = my MAC
	putU16(reply[12:14], etherTypeARP)
	// ARP payload
	putU16(reply[14:16], 1)        // HW type = Ethernet
	putU16(reply[16:18], 0x0800)   // proto = IPv4
	reply[18] = 6                  // HW len
	reply[19] = 4                  // proto len
	putU16(reply[20:22], 2)        // op = reply
	putMAC(reply[22:28], myMAC)    // sender MAC = me
	copy(reply[28:32], myIP[:])    // sender IP  = me
	copy(reply[32:38], arp[8:14])  // target MAC = requester
	copy(reply[38:42], arp[14:18]) // target IP  = requester's IP

	out := make([]byte, 42)
	copy(out, reply[:])
	return out
}

// ─────────────────────────────────────────────────────────────
// IPv4 / TCP helpers
// ─────────────────────────────────────────────────────────────

const (
	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpACK = 0x10
)

// ipChecksum computes a 16-bit one's complement checksum over b.
func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)&1 != 0 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// tcpPseudoChecksum computes TCP checksum using the IPv4 pseudo-header.
func tcpChecksum(srcIP, dstIP [4]byte, tcpSeg []byte) uint16 {
	pseudo := make([]byte, 12+len(tcpSeg))
	copy(pseudo[0:4], srcIP[:])
	copy(pseudo[4:8], dstIP[:])
	pseudo[8] = 0
	pseudo[9] = 6 // TCP
	putU16(pseudo[10:12], uint16(len(tcpSeg)))
	copy(pseudo[12:], tcpSeg)
	return ipChecksum(pseudo)
}

// ─────────────────────────────────────────────────────────────
// Minimal HTTP-over-TCP state machine
// ─────────────────────────────────────────────────────────────

// tcpConn tracks a single TCP connection (we handle one at a time).
type tcpConn struct {
	active  bool
	srcIP   [4]byte
	srcPort uint16
	dstPort uint16 // our listening port
	seqNum  uint32
	ackNum  uint32
	state   uint8 // 0=closed, 1=synRcvd, 2=established, 3=closeWait
}

const (
	tcpStateClosed    = 0
	tcpStateSynRcvd   = 1
	tcpStateEstab     = 2
	tcpStateCloseWait = 3
)

const httpListenPort = 80

var conn tcpConn

// buildIPTCP builds a full Ethernet+IP+TCP frame.
// payload is the TCP data; flags controls TCP control bits.
func buildIPTCP(dstMAC []byte, dstIP [4]byte, dstPort uint16,
	flags uint8, seq, ack uint32, payload []byte) []byte {

	const ipHdrLen = 20
	const tcpHdrLen = 20
	totalLen := 14 + ipHdrLen + tcpHdrLen + len(payload)
	frame := make([]byte, totalLen)

	// ── Ethernet header ──
	copy(frame[0:6], dstMAC)
	putMAC(frame[6:12], myMAC)
	putU16(frame[12:14], etherTypeIPv4)

	// ── IPv4 header ──
	ip := frame[14:]
	ip[0] = 0x45               // version=4, IHL=5
	ip[1] = 0                  // DSCP/ECN
	putU16(ip[2:4], uint16(ipHdrLen+tcpHdrLen+len(payload)))
	putU16(ip[4:6], 0)         // ID=0
	putU16(ip[6:8], 0x4000)    // DF, no fragment
	ip[8] = 64                 // TTL
	ip[9] = 6                  // protocol = TCP
	putU16(ip[10:12], 0)       // checksum placeholder
	copy(ip[12:16], myIP[:])
	copy(ip[16:20], dstIP[:])
	cs := ipChecksum(ip[:ipHdrLen])
	putU16(ip[10:12], cs)

	// ── TCP header ──
	tcp := ip[ipHdrLen:]
	putU16(tcp[0:2], httpListenPort) // src port
	putU16(tcp[2:4], dstPort)        // dst port
	putU32(tcp[4:8], seq)
	putU32(tcp[8:12], ack)
	tcp[12] = 0x50            // data offset = 5 (20 bytes), reserved=0
	tcp[13] = flags
	putU16(tcp[14:16], 4096) // window size
	putU16(tcp[16:18], 0)    // checksum placeholder
	putU16(tcp[18:20], 0)    // urgent pointer
	copy(tcp[tcpHdrLen:], payload)

	var srcIP [4]byte; copy(srcIP[:], myIP[:])
	tcpCS := tcpChecksum(srcIP, dstIP, tcp[:tcpHdrLen+len(payload)])
	putU16(tcp[16:18], tcpCS)

	return frame
}

// handleIPv4 processes an IPv4 frame and may return a reply.
func handleIPv4(ethFrame []byte) []byte {
	if len(ethFrame) < 14+20 {
		return nil
	}
	ip := ethFrame[14:]
	if ip[0]>>4 != 4 { // not IPv4
		return nil
	}
	ipHdrLen := int(ip[0]&0x0F) * 4
	if len(ip) < ipHdrLen+20 { // must have at least a TCP header
		return nil
	}
	if ip[9] != 6 { // not TCP
		return nil
	}
	// Destination IP must be ours
	if !ipEqual(ip[16:20], myIP) {
		return nil
	}

	var srcIP [4]byte; copy(srcIP[:], ip[12:16])
	tcp := ip[ipHdrLen:]
	srcPort := getU16(tcp[0:2])
	dstPort := getU16(tcp[2:4])
	tcpSeq := getU32(tcp[4:8])
	tcpAck := getU32(tcp[8:12])
	tcpDataOff := int(tcp[12]>>4) * 4
	flags := tcp[13]
	_ = tcpAck

	srcMAC := ethFrame[6:12]

	// ── TCP RST ── close our connection if needed
	if flags&tcpRST != 0 {
		if conn.active && conn.srcPort == srcPort && conn.srcIP == srcIP {
			conn = tcpConn{}
		}
		return nil
	}

	// ── SYN (new connection) ──
	if flags&tcpSYN != 0 && flags&tcpACK == 0 {
		if dstPort != httpListenPort {
			// RST for non-listening port
			return buildIPTCP(srcMAC, srcIP, srcPort,
				tcpRST|tcpACK, 0, tcpSeq+1, nil)
		}
		// Accept — send SYN-ACK
		conn = tcpConn{
			active:  true,
			srcIP:   srcIP,
			srcPort: srcPort,
			dstPort: dstPort,
			seqNum:  0x12345678, // our ISN
			ackNum:  tcpSeq + 1,
			state:   tcpStateSynRcvd,
		}
		reply := buildIPTCP(srcMAC, srcIP, srcPort,
			tcpSYN|tcpACK, conn.seqNum, conn.ackNum, nil)
		conn.seqNum++ // SYN consumes one seq byte
		return reply
	}

	if !conn.active || conn.srcIP != srcIP || conn.srcPort != srcPort {
		return nil
	}

	// ── ACK completing the handshake ──
	if conn.state == tcpStateSynRcvd && flags&tcpACK != 0 {
		conn.state = tcpStateEstab
		conn.ackNum = tcpSeq
	}

	// ── Data (HTTP request) ──
	if conn.state == tcpStateEstab {
		data := tcp[tcpDataOff:]
		if len(data) > 0 {
			conn.ackNum = tcpSeq + uint32(len(data))
			// Any data on port 80 → send HTTP 200 Hello World + FIN
			resp := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 13\r\nConnection: close\r\n\r\nHello, World!")
			conn.state = tcpStateCloseWait
			reply := buildIPTCP(srcMAC, srcIP, srcPort,
				tcpACK|tcpFIN, conn.seqNum, conn.ackNum, resp)
			conn.seqNum += uint32(len(resp)) + 1 // data + FIN
			return reply
		}
	}

	// ── FIN ──
	if flags&tcpFIN != 0 {
		conn.ackNum = tcpSeq + 1
		reply := buildIPTCP(srcMAC, srcIP, srcPort,
			tcpACK, conn.seqNum, conn.ackNum, nil)
		conn = tcpConn{} // done
		return reply
	}

	return nil
}

// ─────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────

func main() {
	println("=== ESP32-P4-ETH Demo — HTTP Hello World ===")
	println("UART0 serial via USB-C at 115200 baud")

	println("Initialising EMAC...")
	linkUp := machine.DefaultEMAC.Init(myMAC)
	if linkUp {
		println("EMAC: link UP")
	} else {
		println("EMAC: link DOWN (no PHY or cable?); continuing anyway")
	}
	println("HTTP server on 192.168.1.100:80")
	println("Connect an Ethernet cable and browse to http://192.168.1.100/")

	for {
		frame := machine.DefaultEMAC.Recv()
		if frame == nil {
			continue
		}

		var reply []byte
		switch etherType(frame) {
		case etherTypeARP:
			reply = handleARP(frame)
		case etherTypeIPv4:
			reply = handleIPv4(frame)
		}

		if reply != nil {
			for !machine.DefaultEMAC.Send(reply) {
				// TX ring full — spin briefly until it drains
			}
		}
	}
}

