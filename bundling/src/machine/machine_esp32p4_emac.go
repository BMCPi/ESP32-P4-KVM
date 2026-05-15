//go:build esp32p4

// ESP32-P4 EMAC (Ethernet MAC) driver — raw Ethernet frame TX/RX.
//
// Hardware: Synopsys DesignWare GMAC (same IP as DW7GMAC in ESP32).
// Register map (from ESP-IDF soc/esp32p4/ld/esp32p4.peripherals.ld):
//   EMAC_MAC  = 0x50098000   — MAC configuration + MDIO management
//   EMAC_DMA  = 0x50099000   — DMA bus-mode, descriptor lists, operation
//   HP_SYS    = 0x500E5000   — GMAC_CTRL0 (PHY interface select)
//   HP_SYS_CLKRST = 0x500E6000 — EMAC system + RMII clocks
//   LP_CLKRST = 0x50111000   — EMAC peripheral reset
//
// GPIO pins — Waveshare ESP32-P4-ETH default RMII assignment
// (matches ESP-IDF Kconfig defaults for ESP32-P4; update if schematic differs):
//   GPIO50  RMII_CLK   — 50 MHz reference clock input from PHY
//   GPIO28  CRS_DV     — carrier sense / receive data valid (RX input)
//   GPIO29  RXD0       — receive data bit 0 (RX input)
//   GPIO30  RXD1       — receive data bit 1 (RX input)
//   GPIO49  TX_EN      — transmit enable (TX output)
//   GPIO34  TXD0       — transmit data bit 0 (TX output)
//   GPIO35  TXD1       — transmit data bit 1 (TX output)
//   GPIO31  MDC        — management data clock (output)
//   GPIO52  MDIO       — management data I/O (bidirectional)
//   GPIO51  PHY_RST    — PHY hardware reset, active-low (output)
//
// The driver uses polling (no interrupts) and chained descriptors.

package machine

import (
	"device/esp"
	"device/riscv"
	"runtime/volatile"
	"unsafe"
)

// ─────────────────────────────────────────────────────────────
// Peripheral base addresses (raw, from ESP32-P4 peripheral map)
// ─────────────────────────────────────────────────────────────

const (
	emacMACBase    = uintptr(0x50098000)
	emacDMABase    = uintptr(0x50099000)
	hpSysBase      = uintptr(0x500E5000)
	hpSysClkrstBase = uintptr(0x500E6000)
	lpClkrstBase   = uintptr(0x50111000)
)

// emacReg returns a *volatile.Register32 for an EMAC MAC register at offset.
func emacMACReg(offset uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(emacMACBase + offset))
}

// emacDMAReg returns a *volatile.Register32 for an EMAC DMA register at offset.
func emacDMAReg(offset uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(emacDMABase + offset))
}

func hpSysReg(offset uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(hpSysBase + offset))
}

func hpSysClkrstReg(offset uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(hpSysClkrstBase + offset))
}

func lpClkrstReg(offset uintptr) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Pointer(lpClkrstBase + offset))
}

// ─────────────────────────────────────────────────────────────
// EMAC MAC register offsets (from emac_reg.h hw_ver1)
// ─────────────────────────────────────────────────────────────
const (
	emacMACConfig     = uintptr(0x000) // MACCONFIGURATION
	emacMACFrameFilter = uintptr(0x004) // frame filter
	emacGMIIAddress   = uintptr(0x010) // MDIO management address
	emacGMIIData      = uintptr(0x014) // MDIO management data
	emacAddr0High     = uintptr(0x044) // MAC address 0 high (AE|[47:32])
	emacAddr0Low      = uintptr(0x048) // MAC address 0 low  [31:0]
)

// MACCONFIGURATION bits
const (
	emacMACConfig_RE  = uint32(1 << 2)  // Receiver Enable
	emacMACConfig_TE  = uint32(1 << 3)  // Transmitter Enable
	emacMACConfig_DM  = uint32(1 << 11) // Duplex Mode (1=full)
	emacMACConfig_FES = uint32(1 << 14) // Fast Ethernet Speed (1=100Mbps)
	emacMACConfig_PS  = uint32(1 << 15) // Port Select (1=MII, which covers RMII)

	// Combined value for 100 Mbps RMII full-duplex
	emacMACConfig_100FD = emacMACConfig_PS | emacMACConfig_FES | emacMACConfig_DM | emacMACConfig_TE | emacMACConfig_RE
)

// GMIIADDRESS (MDIO) bits
const (
	emacGMIIAddr_GB  = uint32(1 << 0)  // GMII busy
	emacGMIIAddr_GW  = uint32(1 << 1)  // GMII write (1=write, 0=read)
	emacGMIIAddr_CR  = uint32(4 << 2)  // CSR clock range: 0x4 → HCLK/102 (100–150 MHz)
	emacGMIIAddr_GR  = 6               // bit position of register address
	emacGMIIAddr_PA  = 11              // bit position of PHY address
)

// ─────────────────────────────────────────────────────────────
// EMAC DMA register offsets
// ─────────────────────────────────────────────────────────────
const (
	emacDMABusMode       = uintptr(0x000)
	emacDMATxPollDemand  = uintptr(0x004)
	emacDMARxPollDemand  = uintptr(0x008)
	emacDMARxDescListAddr = uintptr(0x00C)
	emacDMATxDescListAddr = uintptr(0x010)
	emacDMAStatus        = uintptr(0x014)
	emacDMAOpMode        = uintptr(0x018)
	emacDMAIntEnable     = uintptr(0x01C)
	emacDMAMissedFrames  = uintptr(0x020)
)

// DMA BUSMODE bits
const (
	emacDMABusMode_SWR = uint32(1 << 0) // Software Reset
	emacDMABusMode_PBL = uint32(32 << 8) // Programmable Burst Length = 32
	emacDMABusMode_FIX = uint32(1 << 16) // Fixed Burst
	emacDMABusMode_USP = uint32(1 << 23) // Use Separate PBL
	emacDMABusMode_AAL = uint32(1 << 25) // Address-Aligned Beats
)

// DMA OPERATIONMODE bits
const (
	emacDMAOpMode_SR  = uint32(1 << 1)  // Start Receive
	emacDMAOpMode_ST  = uint32(1 << 13) // Start Transmit
	emacDMAOpMode_RSF = uint32(1 << 25) // Receive Store and Forward
	emacDMAOpMode_TSF = uint32(1 << 21) // Transmit Store and Forward
)

// DMA STATUS bits
const (
	emacDMAStatus_RI  = uint32(1 << 6)  // Receive Interrupt
	emacDMAStatus_TI  = uint32(1 << 0)  // Transmit Interrupt
	emacDMAStatus_RU  = uint32(1 << 7)  // Receive Buffer Unavailable
	emacDMAStatus_NIS = uint32(1 << 16) // Normal Interrupt Summary
)

// ─────────────────────────────────────────────────────────────
// Clock / reset register offsets
// ─────────────────────────────────────────────────────────────
const (
	// HP_SYS_CLKRST
	hpSysClkrst_SocClkCtrl1   = uintptr(0x18) // SOC_CLK_CTRL1
	hpSysClkrst_PeriClkCtrl00 = uintptr(0x30) // PERI_CLK_CTRL00
	hpSysClkrst_PeriClkCtrl01 = uintptr(0x34) // PERI_CLK_CTRL01

	// Bits in SOC_CLK_CTRL1
	emacSysClkEn = uint32(1 << 13)

	// Bits in PERI_CLK_CTRL00
	emacRmiiClkEn    = uint32(1 << 27) // reg_emac_rmii_clk_en
	emacRxClkEn      = uint32(1 << 29) // reg_emac_rx_clk_en
	padEmacRefClkEn  = uint32(1 << 24) // reg_pad_emac_ref_clk_en (keep 0 for input CLK)

	// Bits in PERI_CLK_CTRL01
	emacTxClkEn = uint32(1 << 9) // reg_emac_tx_clk_en

	// HP_SYSTEM GMAC_CTRL0 at offset 0x14C
	hpSys_GmacCtrl0 = uintptr(0x14C)
	gmacPhyIntfRmii = uint32(0x4 << 2) // PHY_INTF_SEL[4:2] = 0x4 → RMII

	// LP_CLKRST HP_SDMMC_EMAC_RST_CTRL at offset 0x4C
	lpClkrst_HpSdmmcEmacRstCtrl = uintptr(0x4C)
	emacRstEn                   = uint32(1 << 30) // rst_en_emac

	// LP_CLKRST HP_CLK_CTRL at offset (to be determined; pad clocks are enabled by default)
)

// ─────────────────────────────────────────────────────────────
// GPIO matrix signal IDs for EMAC (from gpio_sig_map.h)
// ─────────────────────────────────────────────────────────────
const (
	sigMiiMDI_In        = 107 // MDIO data input
	sigMiiMDC_Out       = 108 // MDC clock output
	sigMiiMDO_Out       = 109 // MDIO data output
	sigEmacRxDV_In      = 178 // CRS_DV / RXDV input (RMII)
	sigEmacTxEn_Out     = 178 // TX_EN output (RMII) — same signal ID
	sigEmacRxD0_In      = 179 // RXD0 input
	sigEmacTxD0_Out     = 179 // TXD0 output
	sigEmacRxD1_In      = 180 // RXD1 input
	sigEmacTxD1_Out     = 180 // TXD1 output
	sigEmacRxClk_In     = 184 // RX_CLK / RMII_REF_CLK input
)

// ─────────────────────────────────────────────────────────────
// GPIO pin assignments for Waveshare ESP32-P4-ETH (RMII)
// Matches ESP-IDF Kconfig defaults for ESP32-P4.
// Adjust constants below if your board schematic differs.
// ─────────────────────────────────────────────────────────────
const (
	pinRmiiClk  Pin = 50 // GPIO50 — RMII 50 MHz CLK input from PHY
	pinCrsDv    Pin = 28 // GPIO28 — CRS_DV  (RX input)
	pinRxD0     Pin = 29 // GPIO29 — RXD0    (RX input)
	pinRxD1     Pin = 30 // GPIO30 — RXD1    (RX input)
	pinTxEn     Pin = 49 // GPIO49 — TX_EN   (TX output)
	pinTxD0     Pin = 34 // GPIO34 — TXD0    (TX output)
	pinTxD1     Pin = 35 // GPIO35 — TXD1    (TX output)
	pinMDC      Pin = 31 // GPIO31 — MDC     (output)
	pinMDIO     Pin = 52 // GPIO52 — MDIO    (bidirectional)
	pinPhyRst   Pin = 51 // GPIO51 — PHY RST (output, active-low)
)

// PHY I²C/SMI address (most single-PHY boards use address 1).
const phyAddr = 1

// ─────────────────────────────────────────────────────────────
// DMA descriptor ring
// ─────────────────────────────────────────────────────────────

const (
	emacRxDescCount = 4
	emacTxDescCount = 4
	emacFrameSize   = 1536 // max Ethernet frame (1500 + 14 hdr + 4 FCS + 18 VLAN headroom)
)

// emacDesc is a DWC GMAC normal (non-enhanced) DMA descriptor — 4 × 32-bit words.
type emacDesc struct {
	status uint32 // RDES0 / TDES0: ownership + status
	ctrl   uint32 // RDES1 / TDES1: buffer sizes + control flags
	buf1   uint32 // RDES2 / TDES2: buffer 1 physical address
	next   uint32 // RDES3 / TDES3: next-descriptor physical address (chained mode)
}

// Descriptor bit fields
const (
	// RX RDES0
	rdes0OWN      = uint32(1 << 31) // 1=DMA owns, 0=CPU owns
	rdes0FS       = uint32(1 << 9)  // first descriptor
	rdes0LS       = uint32(1 << 8)  // last descriptor
	rdes0FLShift  = 16              // frame length field start bit
	rdes0FLMask   = uint32(0x3FFF << 16) // bits [29:16]

	// RX RDES1 control
	rdes1RCH  = uint32(1 << 14) // second address chained (next desc in RDES3)
	rdes1Size = uint32(emacFrameSize & 0x1FFF) // buffer 1 size

	// TX TDES0 control (CPU sets before handing to DMA)
	tdes0OWN = uint32(1 << 31) // DMA owns
	tdes0IC  = uint32(1 << 30) // interrupt on completion
	tdes0LS  = uint32(1 << 29) // last segment
	tdes0FS  = uint32(1 << 28) // first segment
	tdes0TCH = uint32(1 << 20) // transmit chained (next desc in TDES3)

	// TX TDES0 ready mask: single-frame = OWN|IC|LS|FS|TCH
	tdes0SingleFrame = tdes0OWN | tdes0IC | tdes0LS | tdes0FS | tdes0TCH
)

// Static descriptor rings and frame buffers (placed in SRAM by the linker).
var (
	emacRxDescs [emacRxDescCount]emacDesc
	emacTxDescs [emacTxDescCount]emacDesc
	emacRxBufs  [emacRxDescCount][emacFrameSize]byte
	emacTxBufs  [emacTxDescCount][emacFrameSize]byte
)

// EMAC is the singleton Ethernet MAC driver.
type EMAC struct {
	rxIdx uint32 // next RX descriptor to check
	txIdx uint32 // next TX descriptor to use
}

// DefaultEMAC is the singleton EMAC instance.
var DefaultEMAC EMAC

// ─────────────────────────────────────────────────────────────
// EMAC public API
// ─────────────────────────────────────────────────────────────

// Init initialises the Ethernet MAC, PHY, and DMA.
// Returns true if a PHY was found and the link came up.
// macAddr is the 6-byte hardware address to assign.
func (e *EMAC) Init(macAddr [6]byte) bool {
	// ── 1. Enable EMAC system clock ──
	hpSysClkrstReg(hpSysClkrst_SocClkCtrl1).SetBits(emacSysClkEn)

	// ── 2. Reset EMAC peripheral (pulse rst_en_emac) ──
	lpClkrstReg(lpClkrst_HpSdmmcEmacRstCtrl).SetBits(emacRstEn)
	{
		start := uint(riscv.CYCLE.Get())
		for uint(riscv.CYCLE.Get())-start < 2*cyclesPerMs {
		}
	}
	lpClkrstReg(lpClkrst_HpSdmmcEmacRstCtrl).ClearBits(emacRstEn)

	// ── 3. Configure RMII clocks ──
	// PHY interface = RMII (bits [4:2] of GMAC_CTRL0 = 0x4)
	ctrl0 := hpSysReg(hpSys_GmacCtrl0)
	ctrl0.Set((ctrl0.Get() &^ uint32(0x1C)) | gmacPhyIntfRmii)

	// Enable RMII CLK (from external PHY pad), RX CLK, TX CLK.
	// PERI_CLK_CTRL00: pad_emac_ref_clk_en=0 (CLK is input, not output),
	//                  emac_rmii_clk_src_sel=0 (pad_emac_txrx_clk),
	//                  emac_rmii_clk_en=1, emac_rx_clk_en=1.
	peri00 := hpSysClkrstReg(hpSysClkrst_PeriClkCtrl00)
	peri00.SetBits(emacRmiiClkEn | emacRxClkEn)
	peri00.ClearBits(padEmacRefClkEn) // CLK is input from PHY

	// PERI_CLK_CTRL01: emac_tx_clk_en=1.
	hpSysClkrstReg(hpSysClkrst_PeriClkCtrl01).SetBits(emacTxClkEn)

	// ── 4. Reset PHY and configure GPIO pins ──
	pinPhyRst.Configure(PinConfig{Mode: PinOutput})
	pinPhyRst.Set(false) // assert reset (active-low)
	{
		start := uint(riscv.CYCLE.Get())
		for uint(riscv.CYCLE.Get())-start < 10*cyclesPerMs {
		}
	}
	pinPhyRst.Set(true) // deassert reset

	// Allow PHY to come out of reset (100 ms minimum for most PHYs).
	{
		start := uint(riscv.CYCLE.Get())
		for uint(riscv.CYCLE.Get())-start < 150*cyclesPerMs {
		}
	}

	// ── 5. Configure GPIO matrix for EMAC RMII signals ──
	// Inputs: CLK, CRS_DV, RXD0, RXD1
	pinRmiiClk.configure(PinConfig{Mode: PinInput}, sigEmacRxClk_In)
	pinCrsDv.configure(PinConfig{Mode: PinInput}, sigEmacRxDV_In)
	pinRxD0.configure(PinConfig{Mode: PinInput}, sigEmacRxD0_In)
	pinRxD1.configure(PinConfig{Mode: PinInput}, sigEmacRxD1_In)

	// Outputs: TX_EN, TXD0, TXD1, MDC
	pinTxEn.configure(PinConfig{Mode: PinOutput}, sigEmacTxEn_Out)
	pinTxD0.configure(PinConfig{Mode: PinOutput}, sigEmacTxD0_Out)
	pinTxD1.configure(PinConfig{Mode: PinOutput}, sigEmacTxD1_Out)
	pinMDC.configure(PinConfig{Mode: PinOutput}, sigMiiMDC_Out)

	// MDIO is bidirectional — route both input and output signals to the same GPIO.
	// The MAC hardware controls the direction; we configure the pad as output-capable.
	pinMDIO.Configure(PinConfig{Mode: PinOutput})
	pinMDIO.outFunc().Set(sigMiiMDO_Out)
	inFunc(sigMiiMDI_In).Set(esp.GPIO_FUNC_IN_SEL_CFG_SEL |
		uint32(pinMDIO)<<esp.GPIO_FUNC_IN_SEL_CFG_IN_SEL_Pos)

	// ── 6. DMA software reset ──
	emacDMAReg(emacDMABusMode).SetBits(emacDMABusMode_SWR)
	waitUntilOrTimeout(200, func() bool {
		return emacDMAReg(emacDMABusMode).Get()&emacDMABusMode_SWR == 0
	})

	// ── 7. Set MAC address ──
	emacMACReg(emacAddr0High).Set(0x80000000 | // AE=1 (address enable)
		uint32(macAddr[5])<<8 | uint32(macAddr[4]))
	emacMACReg(emacAddr0Low).Set(
		uint32(macAddr[3])<<24 | uint32(macAddr[2])<<16 |
			uint32(macAddr[1])<<8 | uint32(macAddr[0]))

	// ── 8. MAC frame filter: pass all frames (promiscuous for simplicity) ──
	emacMACReg(emacMACFrameFilter).Set(0x80000000) // RA=1 receive all

	// ── 9. Set up RX descriptor ring ──
	for i := range emacRxDescs {
		next := (i + 1) % emacRxDescCount
		emacRxDescs[i] = emacDesc{
			status: rdes0OWN,                 // DMA owns
			ctrl:   rdes1RCH | rdes1Size,     // chained, buffer size
			buf1:   uint32(uintptr(unsafe.Pointer(&emacRxBufs[i][0]))),
			next:   uint32(uintptr(unsafe.Pointer(&emacRxDescs[next]))),
		}
	}
	e.rxIdx = 0

	// ── 10. Set up TX descriptor ring (CPU owns initially) ──
	for i := range emacTxDescs {
		next := (i + 1) % emacTxDescCount
		emacTxDescs[i] = emacDesc{
			status: 0, // CPU owns
			ctrl:   0,
			buf1:   uint32(uintptr(unsafe.Pointer(&emacTxBufs[i][0]))),
			next:   uint32(uintptr(unsafe.Pointer(&emacTxDescs[next]))),
		}
	}
	e.txIdx = 0

	// ── 11. Give DMA the descriptor list base addresses ──
	emacDMAReg(emacDMARxDescListAddr).Set(uint32(uintptr(unsafe.Pointer(&emacRxDescs[0]))))
	emacDMAReg(emacDMATxDescListAddr).Set(uint32(uintptr(unsafe.Pointer(&emacTxDescs[0]))))

	// ── 12. DMA bus mode: fixed-burst, PBL=32 ──
	emacDMAReg(emacDMABusMode).Set(emacDMABusMode_PBL | emacDMABusMode_FIX | emacDMABusMode_AAL)

	// ── 13. Configure MAC for 100 Mbps RMII full-duplex ──
	emacMACReg(emacMACConfig).Set(emacMACConfig_100FD)

	// ── 14. Start DMA TX + RX ──
	emacDMAReg(emacDMAOpMode).Set(
		emacDMAOpMode_SR | emacDMAOpMode_ST |
			emacDMAOpMode_RSF | emacDMAOpMode_TSF)

	// ── 15. Check PHY link (BMSR register 1, bit 2 = link status) ──
	bmsr := e.mdioRead(phyAddr, 1)
	return bmsr&(1<<2) != 0
}

// Send transmits an Ethernet frame. frame must include the 14-byte Ethernet header.
// Returns false if the TX ring is full (caller should retry).
func (e *EMAC) Send(frame []byte) bool {
	if len(frame) == 0 || len(frame) > emacFrameSize {
		return false
	}
	d := &emacTxDescs[e.txIdx]
	if volatile.LoadUint32(&d.status)&tdes0OWN != 0 {
		return false // DMA still owns this descriptor
	}

	// Copy frame into the static TX buffer.
	n := copy(emacTxBufs[e.txIdx][:], frame)

	// Set TDES1 buffer length, hand descriptor to DMA.
	volatile.StoreUint32(&d.ctrl, uint32(n)&0x1FFF)
	volatile.StoreUint32(&d.status, tdes0SingleFrame) // OWN → DMA

	// Advance TX ring index.
	e.txIdx = (e.txIdx + 1) % emacTxDescCount

	// Kick TX poll demand so DMA processes the descriptor immediately.
	emacDMAReg(emacDMATxPollDemand).Set(1)
	return true
}

// Recv returns the next received Ethernet frame (including 14-byte header),
// or nil if no frame is available.  The returned slice is valid until the
// next call to Recv (it aliases the internal RX buffer).
func (e *EMAC) Recv() []byte {
	d := &emacRxDescs[e.rxIdx]
	status := volatile.LoadUint32(&d.status)
	if status&rdes0OWN != 0 {
		return nil // DMA still owns
	}
	// Frame length is in RDES0[29:16]; subtract 4 for FCS.
	fl := (status & rdes0FLMask) >> rdes0FLShift
	if fl < 4 {
		// Malformed — return descriptor to DMA.
		volatile.StoreUint32(&d.status, rdes0OWN)
		e.rxIdx = (e.rxIdx + 1) % emacRxDescCount
		emacDMAReg(emacDMARxPollDemand).Set(1)
		return nil
	}
	n := fl - 4 // strip FCS

	// Guard against DMA writing a garbage frame length (e.g. spurious RMII
	// noise when the PHY is not yet linked).  Discard and recycle the descriptor.
	if n > emacFrameSize {
		volatile.StoreUint32(&d.status, rdes0OWN)
		e.rxIdx = (e.rxIdx + 1) % emacRxDescCount
		emacDMAReg(emacDMARxPollDemand).Set(1)
		return nil
	}

	buf := emacRxBufs[e.rxIdx][:n]

	// Return descriptor to DMA immediately.
	volatile.StoreUint32(&d.status, rdes0OWN)
	e.rxIdx = (e.rxIdx + 1) % emacRxDescCount
	emacDMAReg(emacDMARxPollDemand).Set(1)
	return buf
}

// ─────────────────────────────────────────────────────────────
// MDIO / SMI (PHY management interface)
// ─────────────────────────────────────────────────────────────

// mdioRead reads a PHY register (addr) from the PHY at phyAddress.
func (e *EMAC) mdioRead(phyAddress, reg uint32) uint16 {
	// CR=4 → HCLK/102; fits a 100–150 MHz AHB clock.
	addr := emacGMIIAddr_CR |
		(reg << emacGMIIAddr_GR) |
		(phyAddress << emacGMIIAddr_PA) |
		emacGMIIAddr_GB
	emacMACReg(emacGMIIAddress).Set(addr)
	waitUntilOrTimeout(10, func() bool {
		return emacMACReg(emacGMIIAddress).Get()&emacGMIIAddr_GB == 0
	})
	return uint16(emacMACReg(emacGMIIData).Get())
}

// mdioWrite writes val to PHY register reg at phyAddress.
func (e *EMAC) mdioWrite(phyAddress, reg uint32, val uint16) {
	emacMACReg(emacGMIIData).Set(uint32(val))
	addr := emacGMIIAddr_CR |
		(reg << emacGMIIAddr_GR) |
		(phyAddress << emacGMIIAddr_PA) |
		emacGMIIAddr_GW |
		emacGMIIAddr_GB
	emacMACReg(emacGMIIAddress).Set(addr)
	waitUntilOrTimeout(10, func() bool {
		return emacMACReg(emacGMIIAddress).Get()&emacGMIIAddr_GB == 0
	})
}

// LinkUp returns true if the PHY reports link status.
func (e *EMAC) LinkUp() bool {
	return e.mdioRead(phyAddr, 1)&(1<<2) != 0
}
