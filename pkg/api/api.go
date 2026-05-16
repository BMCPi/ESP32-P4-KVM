//go:build tinygo

// Package api implements the BMC HTTP/Redfish API on top of pkg/ethernet's
// raw TCP socket layer.  It deliberately avoids net/http (which transitively
// links ~250 KB of crypto/tls, mime, x509, and asn1 code on TinyGo) and
// encoding/json (replaced by static protobuf-go-lite generated bindings).
package api

import (
	"strconv"
	"sync"
	"time"

	"github.com/tinywasm/fmt"
	"tinygo.org/x/drivers/netlink"

	"github.com/bmcpi/esp32-p4-kvm/pkg/ethernet"
	"github.com/bmcpi/esp32-p4-kvm/pkg/power"
	"github.com/bmcpi/esp32-p4-kvm/pkg/proto"
)

const (
	resetActionTokenHeader  = "X-BMC-Reset-Token"
	resetActionMaxBodyBytes = 128

	// requestReadTimeout caps how long a single connection can stay open
	// while no full request has been read.  Keeps misbehaving clients from
	// pinning a socket forever.
	requestReadTimeout = 5 * time.Second
)

var (
	// Set at build time:
	//   -ldflags "-X github.com/bmcpi/esp32-p4-kvm/pkg/api.configuredResetAuthToken=<token>"
	configuredResetAuthToken string
	powerActionOnce          sync.Once
	powerActionQueue         = make(chan time.Duration, 1)
)

// Configure sets the reset authentication token.  Call from main (where
// the token is injected via -ldflags) before StartPowerActionWorker or
// StartAPIServer.
func Configure(token string) { configuredResetAuthToken = token }

// StartAPIServer brings the EMAC up, opens TCP port 80, and serves the
// BMC HTTP endpoints.  Blocks for the life of the program; callers
// typically run it in a goroutine.
func StartAPIServer() {
	link, _ := ethernet.Probe()
	if err := link.NetConnect(&netlink.ConnectParams{}); err != nil {
		fmt.Printf("Network connect failed: %s\n", err)
		return
	}

	ln, err := ethernet.Listen(80)
	if err != nil {
		fmt.Printf("Listen :80 failed: %s\n", err)
		return
	}
	fmt.Println("BMC API listening on :80")

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("Accept failed: %s\n", err)
			continue
		}
		go serveConn(conn)
	}
}

// ── HTTP/1.1 server ──────────────────────────────────────────────────────────
//
// One goroutine per connection; one request per connection (Connection:
// close on every response).  Keeps things simple — no chunked encoding,
// no keep-alive, no multipart, no TLS.

const (
	maxRequestLine    = 512  // request line + URI
	maxHeaderBytes    = 2048 // all headers combined
	requestReaderSize = 1024 // size of our line buffer
)

type request struct {
	method     string
	path       string
	contentLen int
	resetToken string
	body       []byte // read from socket; len == contentLen
}

type response struct {
	conn       *ethernet.Conn
	statusSent bool
	out        []byte // scratch buffer for header assembly
}

func serveConn(conn *ethernet.Conn) {
	defer conn.Close()

	req, err := readRequest(conn)
	if err != nil {
		writeStatus(conn, 400, "Bad Request", "text/plain; charset=utf-8", []byte("Bad Request"))
		return
	}

	w := &response{conn: conn}
	switch {
	case req.path == "/healthz":
		handleHealthz(w, req)
	case req.path == "/redfish/v1/Systems/1":
		handleSystemStatus(w, req)
	case req.path == "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset":
		handlePowerReset(w, req)
	default:
		writeStatus(conn, 404, "Not Found", "text/plain; charset=utf-8", []byte("Not Found"))
	}
}

// readRequest parses one HTTP/1.1 request from conn.  Returns an error if
// the request line is malformed, the headers exceed maxHeaderBytes, or
// Content-Length declares more than resetActionMaxBodyBytes.
func readRequest(conn *ethernet.Conn) (*request, error) {
	buf := make([]byte, 0, requestReaderSize)
	tmp := make([]byte, 256)
	headersDone := false
	var consumed int

	for !headersDone {
		if len(buf) > maxHeaderBytes+maxRequestLine {
			return nil, errHeaderTooLarge
		}
		n, err := conn.Read(tmp)
		if err != nil || n == 0 {
			return nil, err
		}
		buf = append(buf, tmp[:n]...)
		if i := indexCRLFCRLF(buf); i >= 0 {
			headersDone = true
			consumed = i + 4
		}
	}

	req := &request{}
	if err := parseRequestHead(buf[:consumed-4], req); err != nil {
		return nil, err
	}

	// Body (if any).  Anything beyond resetActionMaxBodyBytes is rejected.
	if req.contentLen > 0 {
		if req.contentLen > resetActionMaxBodyBytes {
			return nil, errBodyTooLarge
		}
		body := make([]byte, req.contentLen)
		// Some bytes may already be in `buf` after the headers.
		have := copy(body, buf[consumed:])
		for have < req.contentLen {
			n, err := conn.Read(body[have:])
			if err != nil || n == 0 {
				return nil, err
			}
			have += n
		}
		req.body = body
	}

	return req, nil
}

// parseRequestHead splits headBytes (up to but not including the final
// CRLFCRLF) into request-line + headers and populates req.
func parseRequestHead(head []byte, req *request) error {
	// Request line: "METHOD SP path SP HTTP/1.1"
	eol := indexCRLF(head)
	if eol < 0 {
		return errMalformed
	}
	line := head[:eol]
	sp1 := indexByte(line, ' ')
	if sp1 < 0 {
		return errMalformed
	}
	sp2 := indexByte(line[sp1+1:], ' ')
	if sp2 < 0 {
		return errMalformed
	}
	req.method = string(line[:sp1])
	req.path = string(line[sp1+1 : sp1+1+sp2])

	// Headers.
	rest := head[eol+2:]
	for len(rest) > 0 {
		eol = indexCRLF(rest)
		if eol < 0 {
			eol = len(rest)
		}
		hl := rest[:eol]
		if colon := indexByte(hl, ':'); colon > 0 {
			name := hl[:colon]
			val := trimSpace(hl[colon+1:])
			switch {
			case equalFold(name, []byte("Content-Length")):
				n, err := strconv.Atoi(string(val))
				if err == nil && n >= 0 {
					req.contentLen = n
				}
			case equalFold(name, []byte(resetActionTokenHeader)):
				req.resetToken = string(val)
			}
		}
		if eol == len(rest) {
			break
		}
		rest = rest[eol+2:]
	}
	return nil
}

// writeStatus is the one-shot helper for short responses.
func writeStatus(conn *ethernet.Conn, code int, reason, contentType string, body []byte) {
	out := make([]byte, 0, 96+len(body))
	out = append(out, "HTTP/1.1 "...)
	out = strconv.AppendInt(out, int64(code), 10)
	out = append(out, ' ')
	out = append(out, reason...)
	out = append(out, "\r\nContent-Type: "...)
	out = append(out, contentType...)
	out = append(out, "\r\nContent-Length: "...)
	out = strconv.AppendInt(out, int64(len(body)), 10)
	out = append(out, "\r\nConnection: close\r\n\r\n"...)
	out = append(out, body...)
	_, _ = conn.Write(out)
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleHealthz(w *response, r *request) {
	if r.method != "GET" {
		writeStatus(w.conn, 405, "Method Not Allowed", "text/plain", []byte("Method Not Allowed"))
		return
	}
	writeJSON(w.conn, 200, "OK", &proto.Healthz{Status: "ok"})
}

func handleSystemStatus(w *response, r *request) {
	if r.method != "GET" {
		writeStatus(w.conn, 405, "Method Not Allowed", "text/plain", []byte("Method Not Allowed"))
		return
	}
	powerState := "Off"
	if !power.Sense.Get() {
		powerState = "On"
	}
	writeJSON(w.conn, 200, "OK", &proto.SystemStatus{
		Id:         "1",
		Name:       "Managed Host",
		PowerState: powerState,
	})
}

func handlePowerReset(w *response, r *request) {
	if r.method != "POST" {
		writeStatus(w.conn, 405, "Method Not Allowed", "text/plain", []byte("Method Not Allowed"))
		return
	}
	if !authorizePowerReset(w.conn, r) {
		return
	}

	var req proto.ResetRequest
	if err := req.UnmarshalJSON(r.body); err != nil {
		writeJSON(w.conn, 400, "Bad Request", &proto.ErrorResponse{Error: "Bad Request"})
		return
	}

	var duration time.Duration
	switch req.ResetType {
	case "On", "GracefulShutdown":
		duration = 500 * time.Millisecond
	case "ForceOff":
		duration = 6 * time.Second
	default:
		writeJSON(w.conn, 400, "Bad Request", &proto.ErrorResponse{Error: "Invalid ResetType"})
		return
	}

	if !enqueuePowerAction(duration) {
		writeJSON(w.conn, 429, "Too Many Requests", &proto.ErrorResponse{Error: "Power action busy"})
		return
	}

	writeJSON(w.conn, 202, "Accepted", &proto.ResetResponse{Status: "Accepted"})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// jsonMarshaler is implemented by every protobuf-go-lite generated message
// when the `json` feature is enabled.
type jsonMarshaler interface {
	MarshalJSON() ([]byte, error)
}

func writeJSON(conn *ethernet.Conn, code int, reason string, msg jsonMarshaler) {
	body, err := msg.MarshalJSON()
	if err != nil {
		fmt.Printf("api: marshal failed: %s\n", err)
		writeStatus(conn, 500, "Internal Server Error", "text/plain", []byte("Internal Server Error"))
		return
	}
	writeStatus(conn, code, reason, "application/json", body)
}

func authorizePowerReset(conn *ethernet.Conn, r *request) bool {
	if configuredResetAuthToken == "" {
		writeJSON(conn, 503, "Service Unavailable", &proto.ErrorResponse{Error: "Reset action disabled"})
		return false
	}
	if !constantTimeEqual([]byte(r.resetToken), []byte(configuredResetAuthToken)) {
		writeJSON(conn, 401, "Unauthorized", &proto.ErrorResponse{Error: "Unauthorized"})
		return false
	}
	return true
}

// constantTimeEqual: see comment on use site.  Reimplemented here to avoid
// pulling in crypto/subtle (which transitively links ~30 KB of
// crypto/internal/fips140 self-test code).
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// StartPowerActionWorker spawns the goroutine that drains the power action
// queue and presses the physical power button for the requested duration.
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

// ── byte-slice helpers (no bytes/strings imports) ───────────────────────────

var (
	errHeaderTooLarge = &apiErr{"header too large"}
	errBodyTooLarge   = &apiErr{"body too large"}
	errMalformed      = &apiErr{"malformed request"}
)

type apiErr struct{ s string }

func (e *apiErr) Error() string { return e.s }

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func indexCRLF(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func indexCRLFCRLF(b []byte) int {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' && b[i+2] == '\r' && b[i+3] == '\n' {
			return i
		}
	}
	return -1
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

// equalFold compares two ASCII byte slices case-insensitively.  Header
// names are always ASCII, so we don't need unicode folding.
func equalFold(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
