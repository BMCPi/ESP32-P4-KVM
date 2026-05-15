//go:build tinygo

// Package ethernet implements an EMAC-backed netdev/netlink for the ESP32-P4.
//
// The ESP32-P4 EMAC driver provides raw Ethernet frame TX/RX only; there is no
// TCP/IP offload and no built-in netlink/probe implementation for Ethernet in
// tinygo.org/x/drivers. This package implements the full stack needed to use
// the standard net/http server on top of the raw EMAC:
//
//	netdev.Netdever  (BSD socket API — L3/L4)
//	netlink.Netlinker (L2 connect / MAC — L2)
//
// Static IP only; DHCP support can be added later.
//
// Usage (follows the webserver example pattern):
//
//	link, _ := ethernet.Probe()
//	link.NetConnect(&netlink.ConnectParams{})
//	http.HandleFunc("/", handler)
//	http.ListenAndServe(":80", nil)
//
// Note: increase stack size when using net/http: -stack-size=8KB
package ethernet

import (
	"machine"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/tinywasm/fmt"
	"tinygo.org/x/drivers/netdev"
	"tinygo.org/x/drivers/netlink"
)

// ── Static network configuration ─────────────────────────────────────────────
// Adjust these to match your network before flashing.

var (
	ethernetMAC = [6]byte{0x02, 0xDE, 0xAD, 0xBE, 0xEF, 0x01} // locally administered
	ethernetIP  = [4]byte{192, 168, 1, 200}
)

// ── Ethernet / IP / TCP constants ────────────────────────────────────────────

const (
	ethTypeARP  = 0x0806
	ethTypeIPv4 = 0x0800

	tcpFlagFIN = 0x01
	tcpFlagSYN = 0x02
	tcpFlagRST = 0x04
	tcpFlagACK = 0x10

	ipHdrLen      = 20
	tcpHdrLen     = 20
	maxSockets    = 8
	rxChannelSize = 8 // buffered data chunks per connection
)

// ── Socket ────────────────────────────────────────────────────────────────────

type sockState int

const (
	sockClosed    sockState = 0
	sockListening sockState = 1
	sockSynRcvd   sockState = 2
	sockEstab     sockState = 3
	sockCloseWait sockState = 4
)

// emacSocket represents one TCP socket (listening or connection).
type emacSocket struct {
	state      sockState
	listenPort uint16 // our local port (also the src port in outbound frames)

	// remote peer (zero for listening sockets)
	srcIP   [4]byte
	srcPort uint16
	srcMAC  [6]byte

	// TCP sequence tracking
	seqNum uint32 // next byte we will send
	ackNum uint32 // next byte we expect from remote

	// Receive data queue. Dispatch goroutine sends []byte slices here;
	// nil signals EOF (connection closed by remote or by us).
	rxCh      chan []byte
	rxLeftover []byte // partial data from a previous Recv call

	// For listening sockets: newly accepted connection fds are sent here.
	newConnCh chan int
}

// ── EMACNetdev ────────────────────────────────────────────────────────────────

// EMACNetdev implements netdev.Netdever and netlink.Netlinker on top of the
// ESP32-P4 raw Ethernet MAC driver (machine.DefaultEMAC).
type EMACNetdev struct {
	mac    [6]byte
	ip     [4]byte
	ipAddr netip.Addr

	mu     sync.Mutex
	socks  [maxSockets + 1]*emacSocket // index 1..maxSockets; 0 unused

	sendMu sync.Mutex // serialise EMAC TX across goroutines

	notify func(netlink.Event)
}

var emacNetdev = &EMACNetdev{mac: ethernetMAC}

// Probe creates and registers the EMAC-based netdev, following the same pattern
// as tinygo.org/x/drivers/netlink/probe. Call once before using net/http.
func Probe() (netlink.Netlinker, netdev.Netdever) {
	netdev.UseNetdev(emacNetdev)
	return emacNetdev, emacNetdev
}

// ── netlink.Netlinker ─────────────────────────────────────────────────────────

// NetConnect initialises the EMAC hardware and starts the frame-dispatch
// goroutine. ConnectParams fields (SSID, passphrase, etc.) are unused for
// Ethernet; pass an empty &netlink.ConnectParams{}.
func (d *EMACNetdev) NetConnect(params *netlink.ConnectParams) error {
	copy(d.ip[:], ethernetIP[:])
	d.ipAddr = netip.AddrFrom4(ethernetIP)

	linkUp := machine.DefaultEMAC.Init(d.mac)
	if linkUp {
		fmt.Println("EMAC: link UP")
	} else {
		fmt.Println("EMAC: link DOWN (no PHY or cable?)")
	}

	go d.dispatchLoop()

	if d.notify != nil {
		d.notify(netlink.EventNetUp)
	}
	return nil
}

func (d *EMACNetdev) NetDisconnect() {
	if d.notify != nil {
		d.notify(netlink.EventNetDown)
	}
}

func (d *EMACNetdev) NetNotify(cb func(netlink.Event)) { d.notify = cb }

func (d *EMACNetdev) GetHardwareAddr() (net.HardwareAddr, error) {
	return net.HardwareAddr(d.mac[:]), nil
}

// ── netdev.Netdever ───────────────────────────────────────────────────────────

// GetHostByName resolves a literal IPv4 address string. DNS is not supported.
func (d *EMACNetdev) GetHostByName(name string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(name)
	if err != nil {
		return netip.Addr{}, netdev.ErrHostUnknown
	}
	return addr, nil
}

func (d *EMACNetdev) Addr() (netip.Addr, error) { return d.ipAddr, nil }

func (d *EMACNetdev) Socket(domain, stype, proto int) (int, error) {
	if domain != netdev.AF_INET || stype != netdev.SOCK_STREAM || proto != netdev.IPPROTO_TCP {
		return -1, netdev.ErrProtocolNotSupported
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := 1; i <= maxSockets; i++ {
		if d.socks[i] == nil {
			d.socks[i] = &emacSocket{
				state: sockClosed,
				rxCh:  make(chan []byte, rxChannelSize),
			}
			return i, nil
		}
	}
	return -1, netdev.ErrNoMoreSockets
}

func (d *EMACNetdev) Bind(fd int, ip netip.AddrPort) error {
	s := d.sockByFD(fd)
	if s == nil {
		return netdev.ErrInvalidSocketFd
	}
	s.listenPort = ip.Port()
	return nil
}

// Connect is not supported; this implementation is server-only.
func (d *EMACNetdev) Connect(fd int, host string, ip netip.AddrPort) error {
	return netdev.ErrNotSupported
}

func (d *EMACNetdev) Listen(fd, backlog int) error {
	s := d.sockByFD(fd)
	if s == nil {
		return netdev.ErrInvalidSocketFd
	}
	if backlog < 1 {
		backlog = 1
	}
	s.state = sockListening
	s.newConnCh = make(chan int, backlog)
	return nil
}

// Accept blocks until a new TCP connection is established on the listening
// socket, then returns the new connection's fd and remote address.
func (d *EMACNetdev) Accept(fd int) (int, netip.AddrPort, error) {
	s := d.sockByFD(fd)
	if s == nil {
		return -1, netip.AddrPort{}, netdev.ErrInvalidSocketFd
	}
	newFD, ok := <-s.newConnCh
	if !ok || newFD < 0 {
		return -1, netip.AddrPort{}, netdev.ErrClosingSocket
	}
	conn := d.sockByFD(newFD)
	if conn == nil {
		return -1, netip.AddrPort{}, netdev.ErrClosingSocket
	}
	addr := netip.AddrPortFrom(netip.AddrFrom4(conn.srcIP), conn.srcPort)
	return newFD, addr, nil
}

// Send transmits buf over the established TCP connection in MTU-sized chunks.
func (d *EMACNetdev) Send(fd int, buf []byte, flags int, deadline time.Time) (int, error) {
	s := d.sockByFD(fd)
	if s == nil {
		return 0, netdev.ErrInvalidSocketFd
	}
	const mtu = 1400
	sent := 0
	for len(buf) > 0 {
		n := len(buf)
		if n > mtu {
			n = mtu
		}
		frame := d.buildTCPFrame(s, tcpFlagACK, buf[:n])
		s.seqNum += uint32(n)
		d.txFrame(frame)
		buf = buf[n:]
		sent += n
	}
	return sent, nil
}

// Recv blocks until data is available on the connection, a deadline fires, or
// the connection is closed (returns 0, netdev.ErrClosingSocket).
func (d *EMACNetdev) Recv(fd int, buf []byte, flags int, deadline time.Time) (int, error) {
	s := d.sockByFD(fd)
	if s == nil {
		return 0, netdev.ErrInvalidSocketFd
	}

	// Return any leftover bytes from a previous larger chunk first.
	if len(s.rxLeftover) > 0 {
		n := copy(buf, s.rxLeftover)
		s.rxLeftover = s.rxLeftover[n:]
		return n, nil
	}

	var data []byte
	if deadline.IsZero() {
		data = <-s.rxCh
	} else {
		select {
		case data = <-s.rxCh:
		case <-time.After(time.Until(deadline)):
			return 0, netdev.ErrTimeout
		}
	}

	if data == nil {
		return 0, netdev.ErrClosingSocket
	}
	n := copy(buf, data)
	if n < len(data) {
		s.rxLeftover = data[n:]
	}
	return n, nil
}

// Close sends a TCP FIN and releases the socket.
func (d *EMACNetdev) Close(fd int) error {
	s := d.sockByFD(fd)
	if s == nil {
		return netdev.ErrInvalidSocketFd
	}

	if s.state == sockEstab || s.state == sockCloseWait {
		frame := d.buildTCPFrame(s, tcpFlagFIN|tcpFlagACK, nil)
		s.seqNum++
		d.txFrame(frame)
	}

	// Remove from socket table and signal any blocked Recv.
	d.mu.Lock()
	for i := 1; i <= maxSockets; i++ {
		if d.socks[i] == s {
			d.socks[i] = nil
			break
		}
	}
	d.mu.Unlock()

	select {
	case s.rxCh <- nil: // EOF signal
	default:
	}
	return nil
}

func (d *EMACNetdev) SetSockOpt(fd, level, opt int, value interface{}) error {
	return nil // no-op; keepalive and linger are ignored
}

// ── Frame dispatch loop ───────────────────────────────────────────────────────

func (d *EMACNetdev) dispatchLoop() {
	for {
		frame := machine.DefaultEMAC.Recv()
		if frame == nil {
			time.Sleep(time.Millisecond)
			continue
		}
		if len(frame) < 14 {
			continue
		}
		etype := uint16(frame[12])<<8 | uint16(frame[13])
		var reply []byte
		switch etype {
		case ethTypeARP:
			reply = d.handleARP(frame)
		case ethTypeIPv4:
			reply = d.handleIPv4(frame)
		}
		if reply != nil {
			d.txFrame(reply)
		}
	}
}

// handleARP replies to ARP requests targeting our IP.
func (d *EMACNetdev) handleARP(frame []byte) []byte {
	if len(frame) < 42 {
		return nil
	}
	arp := frame[14:]
	// Ethernet HW=1, IPv4=0x0800, HW len=6, proto len=4, op=request(1)
	if u16(arp[0:]) != 1 || u16(arp[2:]) != 0x0800 ||
		arp[4] != 6 || arp[5] != 4 || u16(arp[6:]) != 1 {
		return nil
	}
	if !ip4eq(arp[24:28], d.ip) {
		return nil
	}
	reply := make([]byte, 42)
	copy(reply[0:6], arp[8:14])    // dst = sender MAC
	copy(reply[6:12], d.mac[:])    // src = our MAC
	pu16(reply[12:14], ethTypeARP)
	pu16(reply[14:16], 1)          // HW type = Ethernet
	pu16(reply[16:18], 0x0800)     // proto = IPv4
	reply[18], reply[19] = 6, 4
	pu16(reply[20:22], 2)          // op = reply
	copy(reply[22:28], d.mac[:])   // sender MAC = ours
	copy(reply[28:32], d.ip[:])    // sender IP  = ours
	copy(reply[32:38], arp[8:14])  // target MAC = requester
	copy(reply[38:42], arp[14:18]) // target IP  = requester
	return reply
}

// handleIPv4 dispatches TCP segments to the appropriate socket.
func (d *EMACNetdev) handleIPv4(frame []byte) []byte {
	if len(frame) < 14+ipHdrLen+tcpHdrLen {
		return nil
	}
	ip := frame[14:]
	if ip[0]>>4 != 4 || ip[9] != 6 { // must be IPv4/TCP
		return nil
	}
	if !ip4eq(ip[16:20], d.ip) { // must be addressed to us
		return nil
	}
	ihl := int(ip[0]&0x0F) * 4
	tcp := ip[ihl:]
	srcPort := u16(tcp[0:2])
	dstPort := u16(tcp[2:4])
	seqNum := u32(tcp[4:8])
	dataOff := int(tcp[12]>>4) * 4
	flags := tcp[13]
	data := tcp[dataOff:]

	var srcIP [4]byte
	copy(srcIP[:], ip[12:16])
	var srcMAC [6]byte
	copy(srcMAC[:], frame[6:12])

	// RST — forcibly close matching connection.
	if flags&tcpFlagRST != 0 {
		d.closeConn(srcIP, srcPort)
		return nil
	}

	// SYN — new incoming connection.
	if flags&tcpFlagSYN != 0 && flags&tcpFlagACK == 0 {
		ls := d.findListening(dstPort)
		if ls == nil {
			return d.buildRST(srcMAC, srcIP, srcPort, dstPort, seqNum+1)
		}
		ns := &emacSocket{
			state:      sockSynRcvd,
			listenPort: dstPort,
			srcIP:      srcIP,
			srcPort:    srcPort,
			srcMAC:     srcMAC,
			seqNum:     0x12345678,
			ackNum:     seqNum + 1,
			rxCh:       make(chan []byte, rxChannelSize),
		}
		newFD := d.allocSocket(ns)
		if newFD < 0 {
			return d.buildRST(srcMAC, srcIP, srcPort, dstPort, seqNum+1)
		}
		reply := d.buildTCPFrame(ns, tcpFlagSYN|tcpFlagACK, nil)
		ns.seqNum++ // SYN consumes one sequence number
		return reply
	}

	conn := d.findConn(srcIP, srcPort)
	if conn == nil {
		return nil
	}

	// ACK completing the three-way handshake.
	if conn.state == sockSynRcvd && flags&tcpFlagACK != 0 {
		conn.state = sockEstab
		conn.ackNum = seqNum
		ls := d.findListening(conn.listenPort)
		if ls != nil {
			fd := d.fdOf(conn)
			select {
			case ls.newConnCh <- fd:
			default: // backlog full — drop
			}
		}
	}

	// Data payload.
	if conn.state == sockEstab && len(data) > 0 {
		conn.ackNum = seqNum + uint32(len(data))
		chunk := make([]byte, len(data))
		copy(chunk, data)
		select {
		case conn.rxCh <- chunk:
		default: // receive buffer full — drop (sender will retransmit)
		}
		return d.buildTCPFrame(conn, tcpFlagACK, nil)
	}

	// FIN — remote is closing.
	if flags&tcpFlagFIN != 0 {
		conn.ackNum = seqNum + 1
		conn.state = sockCloseWait
		select {
		case conn.rxCh <- nil: // signal EOF to Recv
		default:
		}
		return d.buildTCPFrame(conn, tcpFlagACK, nil)
	}

	return nil
}

// ── Socket table helpers ──────────────────────────────────────────────────────

func (d *EMACNetdev) sockByFD(fd int) *emacSocket {
	if fd < 1 || fd > maxSockets {
		return nil
	}
	d.mu.Lock()
	s := d.socks[fd]
	d.mu.Unlock()
	return s
}

func (d *EMACNetdev) allocSocket(s *emacSocket) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := 1; i <= maxSockets; i++ {
		if d.socks[i] == nil {
			d.socks[i] = s
			return i
		}
	}
	return -1
}

func (d *EMACNetdev) findListening(port uint16) *emacSocket {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := 1; i <= maxSockets; i++ {
		s := d.socks[i]
		if s != nil && s.state == sockListening && s.listenPort == port {
			return s
		}
	}
	return nil
}

func (d *EMACNetdev) findConn(srcIP [4]byte, srcPort uint16) *emacSocket {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := 1; i <= maxSockets; i++ {
		s := d.socks[i]
		if s != nil && s.srcIP == srcIP && s.srcPort == srcPort &&
			s.state != sockListening && s.state != sockClosed {
			return s
		}
	}
	return nil
}

func (d *EMACNetdev) closeConn(srcIP [4]byte, srcPort uint16) {
	conn := d.findConn(srcIP, srcPort)
	if conn == nil {
		return
	}
	d.mu.Lock()
	for i := 1; i <= maxSockets; i++ {
		if d.socks[i] == conn {
			d.socks[i] = nil
			break
		}
	}
	d.mu.Unlock()
	select {
	case conn.rxCh <- nil:
	default:
	}
}

func (d *EMACNetdev) fdOf(s *emacSocket) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := 1; i <= maxSockets; i++ {
		if d.socks[i] == s {
			return i
		}
	}
	return -1
}

// ── Frame builder ─────────────────────────────────────────────────────────────

func (d *EMACNetdev) buildTCPFrame(s *emacSocket, flags uint8, payload []byte) []byte {
	frame := make([]byte, 14+ipHdrLen+tcpHdrLen+len(payload))

	// Ethernet header
	copy(frame[0:6], s.srcMAC[:])
	copy(frame[6:12], d.mac[:])
	pu16(frame[12:14], ethTypeIPv4)

	// IPv4 header
	ip := frame[14:]
	ip[0] = 0x45 // version=4, IHL=5
	pu16(ip[2:4], uint16(ipHdrLen+tcpHdrLen+len(payload)))
	pu16(ip[6:8], 0x4000) // DF bit
	ip[8] = 64            // TTL
	ip[9] = 6             // TCP
	copy(ip[12:16], d.ip[:])
	copy(ip[16:20], s.srcIP[:])
	pu16(ip[10:12], ipCksum(ip[:ipHdrLen]))

	// TCP header
	tcp := ip[ipHdrLen:]
	pu16(tcp[0:2], s.listenPort) // src port = our listening port
	pu16(tcp[2:4], s.srcPort)    // dst port = remote's port
	pu32(tcp[4:8], s.seqNum)
	pu32(tcp[8:12], s.ackNum)
	tcp[12] = 0x50 // data offset = 5 (20 bytes)
	tcp[13] = flags
	pu16(tcp[14:16], 4096) // window size
	copy(tcp[tcpHdrLen:], payload)

	var myIP4 [4]byte
	copy(myIP4[:], d.ip[:])
	pu16(tcp[16:18], tcpCksum(myIP4, s.srcIP, tcp[:tcpHdrLen+len(payload)]))

	return frame
}

func (d *EMACNetdev) buildRST(srcMAC [6]byte, srcIP [4]byte, srcPort, listenPort uint16, ackNum uint32) []byte {
	s := &emacSocket{
		srcMAC:     srcMAC,
		srcIP:      srcIP,
		srcPort:    srcPort,
		listenPort: listenPort,
		seqNum:     0,
		ackNum:     ackNum,
	}
	return d.buildTCPFrame(s, tcpFlagRST|tcpFlagACK, nil)
}

// txFrame transmits a frame, spinning briefly if the TX ring is full.
func (d *EMACNetdev) txFrame(frame []byte) {
	d.sendMu.Lock()
	for !machine.DefaultEMAC.Send(frame) {
		d.sendMu.Unlock()
		time.Sleep(time.Millisecond)
		d.sendMu.Lock()
	}
	d.sendMu.Unlock()
}

// ── Wire encoding helpers ─────────────────────────────────────────────────────

func pu16(b []byte, v uint16) { b[0] = byte(v >> 8); b[1] = byte(v) }
func pu32(b []byte, v uint32) {
	b[0] = byte(v >> 24); b[1] = byte(v >> 16); b[2] = byte(v >> 8); b[3] = byte(v)
}
func u16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func u32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func ip4eq(a []byte, b [4]byte) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] && a[3] == b[3]
}

// ipCksum computes the RFC 791 one's-complement checksum.
func ipCksum(b []byte) uint16 {
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

// tcpCksum computes the TCP checksum using the IPv4 pseudo-header.
func tcpCksum(srcIP, dstIP [4]byte, tcpSeg []byte) uint16 {
	pseudo := make([]byte, 12+len(tcpSeg))
	copy(pseudo[0:4], srcIP[:])
	copy(pseudo[4:8], dstIP[:])
	pseudo[9] = 6 // TCP
	pu16(pseudo[10:12], uint16(len(tcpSeg)))
	copy(pseudo[12:], tcpSeg)
	return ipCksum(pseudo)
}
