//go:build esp32p4

package machine

// I2C driver for the ESP32-P4 target.
// Adapted from machine_esp32s3_i2c.go + machine_esp32xx_i2c.go.
// Uses HP_SYS_CLKRST for clock/reset control (replaces SYSTEM on S3/C3).

import (
	"device/esp"
	"errors"
	"runtime/volatile"
	"unsafe"
)

// I2C GPIO matrix signal indices for ESP32-P4 (from soc/gpio_sig_map.h).
const (
	I2C0SCLInIDX  = uint32(68)
	I2C0SCLOutIDX = uint32(68)
	I2C0SDAInIDX  = uint32(69)
	I2C0SDAOutIDX = uint32(69)
	I2C1SCLInIDX  = uint32(70)
	I2C1SCLOutIDX = uint32(70)
	I2C1SDAInIDX  = uint32(71)
	I2C1SDAOutIDX = uint32(71)
)

// Default I2C pins for generic ESP32-P4 target.
const (
	SCL_PIN = NoPin
	SDA_PIN = NoPin
)

// I2C clock source: XTAL at 40 MHz.
const (
	clkXTAL               = 0
	clkXTALFrequency      = uint32(40e6)
	i2cClkSourceFrequency = clkXTALFrequency
	i2cClkSource          = clkXTAL
)

// I2C represents an I2C bus.
type I2C struct {
	Bus              *esp.I2C_Type
	funcSCL, funcSDA uint32
	useExt1          bool
	txCmdBuf         [8]i2cCommand
}

// I2CConfig holds I2C bus configuration.
type I2CConfig struct {
	Frequency uint32
	SCL       Pin
	SDA       Pin
}

var (
	I2C0 = &I2C{
		Bus:     esp.I2C0,
		funcSCL: I2C0SCLOutIDX,
		funcSDA: I2C0SDAOutIDX,
		useExt1: false,
	}
	I2C1 = &I2C{
		Bus:     esp.I2C1,
		funcSCL: I2C1SCLOutIDX,
		funcSDA: I2C1SDAOutIDX,
		useExt1: true,
	}
)

// initI2CExt1Clock enables the I2C1 clock via HP_SYS_CLKRST.
func initI2CExt1Clock() {
	esp.HP_SYS_CLKRST.SetHP_RST_EN1_REG_RST_EN_I2C1(1)
	esp.HP_SYS_CLKRST.SetSOC_CLK_CTRL2_REG_I2C1_APB_CLK_EN(1)
	esp.HP_SYS_CLKRST.SetHP_RST_EN1_REG_RST_EN_I2C1(0)
}

func (i2c *I2C) Configure(config I2CConfig) error {
	if config.Frequency == 0 {
		config.Frequency = 400 * KHz
	}
	if config.SCL == 0 {
		config.SCL = SCL_PIN
	}
	if config.SDA == 0 {
		config.SDA = SDA_PIN
	}
	i2c.initClock(config)
	i2c.initNoiseFilter()
	i2c.initPins(config)
	i2c.initFrequency(config)
	i2c.startMaster()
	return nil
}

//go:inline
func (i2c *I2C) initClock(config I2CConfig) {
	if !i2c.useExt1 {
		esp.HP_SYS_CLKRST.SetHP_RST_EN1_REG_RST_EN_I2C0(1)
		esp.HP_SYS_CLKRST.SetSOC_CLK_CTRL2_REG_I2C0_APB_CLK_EN(1)
		esp.HP_SYS_CLKRST.SetHP_RST_EN1_REG_RST_EN_I2C0(0)
	} else {
		initI2CExt1Clock()
	}
	i2c.Bus.INT_CLR.Set(0x3fff)
	i2c.Bus.INT_ENA.ClearBits(0x3fff)
	i2c.Bus.SetCLK_CONF_SCLK_SEL(i2cClkSource)
	i2c.Bus.SetCLK_CONF_SCLK_ACTIVE(1)
	i2c.Bus.SetCLK_CONF_SCLK_DIV_NUM(i2cClkSourceFrequency / (config.Frequency * 1024))
	i2c.Bus.SetCTR_CLK_EN(1)
}

//go:inline
func (i2c *I2C) initNoiseFilter() {
	i2c.Bus.FILTER_CFG.Set(0x377)
}

//go:inline
func (i2c *I2C) initPins(config I2CConfig) {
	config.SDA.configure(PinConfig{Mode: PinOutput}, i2c.funcSDA)
	inFunc(i2c.funcSDA).Set(esp.GPIO_FUNC_IN_SEL_CFG_SEL | uint32(config.SDA)<<esp.GPIO_FUNC_IN_SEL_CFG_IN_SEL_Pos)
	config.SDA.Set(true)
	config.SDA.pinReg().SetBits(esp.GPIO_PIN_PAD_DRIVER)
	i2c.Bus.SetCTR_SDA_FORCE_OUT(1)

	config.SCL.configure(PinConfig{Mode: PinOutput}, i2c.funcSCL)
	inFunc(i2c.funcSCL).Set(esp.GPIO_FUNC_IN_SEL_CFG_SEL | uint32(config.SCL)<<esp.GPIO_FUNC_IN_SEL_CFG_IN_SEL_Pos)
	config.SCL.Set(true)
	config.SCL.pinReg().SetBits(esp.GPIO_PIN_PAD_DRIVER)
	i2c.Bus.SetCTR_SCL_FORCE_OUT(1)
}

//go:inline
func (i2c *I2C) initFrequency(config I2CConfig) {
	clkmDiv := i2cClkSourceFrequency/(config.Frequency*1024) + 1
	sclkFreq := i2cClkSourceFrequency / clkmDiv
	halfCycle := sclkFreq / config.Frequency / 2
	sclLow := halfCycle
	sclWaitHigh := uint32(0)
	if config.Frequency > 50000 {
		sclWaitHigh = halfCycle / 8
	}
	sclHigh := halfCycle - sclWaitHigh
	sdaHold := halfCycle / 4
	sdaSample := halfCycle / 2
	setup := halfCycle
	hold := halfCycle

	i2c.Bus.SetSCL_LOW_PERIOD(sclLow - 1)
	i2c.Bus.SetSCL_HIGH_PERIOD(sclHigh)
	i2c.Bus.SetSCL_HIGH_PERIOD_SCL_WAIT_HIGH_PERIOD(25)
	i2c.Bus.SetSCL_RSTART_SETUP_TIME(setup)
	i2c.Bus.SetSCL_STOP_SETUP_TIME(setup)
	i2c.Bus.SetSCL_START_HOLD_TIME(hold - 1)
	i2c.Bus.SetSCL_STOP_HOLD_TIME(hold - 1)
	i2c.Bus.SetSDA_SAMPLE_TIME(sdaSample)
	i2c.Bus.SetSDA_HOLD_TIME(sdaHold)
}

//go:inline
func (i2c *I2C) startMaster() {
	i2c.Bus.SetFIFO_CONF_NONFIFO_EN(0)
	i2c.Bus.SetFIFO_CONF_RX_FIFO_RST(1)
	i2c.Bus.SetFIFO_CONF_RX_FIFO_RST(0)
	i2c.Bus.SetFIFO_CONF_TX_FIFO_RST(1)
	i2c.Bus.SetFIFO_CONF_TX_FIFO_RST(0)
	i2c.Bus.TO.Set(0x10)
	i2c.Bus.CTR.Set(0x113)
	i2c.Bus.SetCTR_CONF_UPGATE(1)
	i2c.resetMaster()
}

//go:inline
func (i2c *I2C) resetMaster() {
	i2c.Bus.SetCTR_FSM_RST(1)
	i2c.Bus.SetSCL_SP_CONF_SCL_RST_SLV_NUM(9)
	i2c.Bus.SetSCL_SP_CONF_SCL_RST_SLV_EN(1)
	i2c.Bus.SetSCL_STRETCH_CONF_SLAVE_SCL_STRETCH_EN(1)
	i2c.Bus.SetCTR_CONF_UPGATE(1)
	i2c.Bus.FILTER_CFG.Set(0x377)
	for i2c.Bus.GetSCL_SP_CONF_SCL_RST_SLV_EN() != 0 {
	}
	i2c.Bus.SetSCL_SP_CONF_SCL_RST_SLV_NUM(0)
}

type i2cCommandType = uint32

const (
	i2cCMD_RSTART   i2cCommandType = 6 << 11
	i2cCMD_WRITE    i2cCommandType = 1<<11 | 1<<8
	i2cCMD_READ     i2cCommandType = 3<<11 | 1<<8
	i2cCMD_READLAST i2cCommandType = 3<<11 | 5<<8
	i2cCMD_STOP     i2cCommandType = 2 << 11
	i2cCMD_END      i2cCommandType = 4 << 11
)

type i2cCommand struct {
	cmd  i2cCommandType
	data []byte
	head int
}

var (
	errI2CWriteTimeout = errors.New("i2c: timeout during write")
	errI2CReadTimeout  = errors.New("i2c: timeout during read")
	errI2CAckExpected  = errors.New("i2c: error: expected ACK not NACK")
)

//go:linkname nanotime runtime.nanotime
func nanotime() int64

func (i2c *I2C) transmit(addr uint16, cmd []i2cCommand, timeoutMS int) error {
	const intMask = esp.I2C_INT_STATUS_END_DETECT_INT_ST_Msk |
		esp.I2C_INT_STATUS_TRANS_COMPLETE_INT_ST_Msk |
		esp.I2C_INT_STATUS_TIME_OUT_INT_ST_Msk |
		esp.I2C_INT_STATUS_NACK_INT_ST_Msk
	i2c.Bus.INT_CLR.SetBits(intMask)
	i2c.Bus.INT_ENA.SetBits(intMask)
	i2c.Bus.SetCTR_CONF_UPGATE(1)

	defer func() {
		i2c.Bus.INT_CLR.SetBits(intMask)
		i2c.Bus.INT_ENA.ClearBits(intMask)
	}()

	timeoutNS := int64(timeoutMS) * 1000000
	needAddress := true
	needRestart := false
	readLast := false
	var readTo []byte
	for cmdIdx, reg := 0, &i2c.Bus.COMD0; cmdIdx < len(cmd); {
		c := &cmd[cmdIdx]

		switch c.cmd {
		case i2cCMD_RSTART:
			reg.Set(i2cCMD_RSTART)
			reg = nextI2CAddress(reg)
			cmdIdx++

		case i2cCMD_WRITE:
			count := 32
			if needAddress {
				needAddress = false
				i2c.Bus.SetDATA_FIFO_RDATA((uint32(addr) & 0x7f) << 1)
				count--
				i2c.Bus.SLAVE_ADDR.Set(uint32(addr))
				i2c.Bus.SetCTR_CONF_UPGATE(1)
			}
			for ; count > 0 && c.head < len(c.data); count, c.head = count-1, c.head+1 {
				i2c.Bus.SetDATA_FIFO_RDATA(uint32(c.data[c.head]))
			}
			reg.Set(i2cCMD_WRITE | uint32(32-count))
			reg = nextI2CAddress(reg)
			if c.head < len(c.data) {
				reg.Set(i2cCMD_END)
				reg = nil
			} else {
				cmdIdx++
			}
			needRestart = true

		case i2cCMD_READ:
			if needAddress {
				needAddress = false
				i2c.Bus.SetDATA_FIFO_RDATA((uint32(addr)&0x7f)<<1 | 1)
				i2c.Bus.SLAVE_ADDR.Set(uint32(addr))
				reg.Set(i2cCMD_WRITE | 1)
				reg = nextI2CAddress(reg)
			}
			if needRestart {
				reg.Set(i2cCMD_RSTART)
				reg = nextI2CAddress(reg)
				reg.Set(i2cCMD_WRITE | 1)
				reg = nextI2CAddress(reg)
				i2c.Bus.SetDATA_FIFO_RDATA((uint32(addr)&0x7f)<<1 | 1)
				needRestart = false
			}
			count := 32
			bytes := len(c.data) - c.head
			split := bytes <= count
			if split {
				bytes--
			}
			if bytes > 32 {
				bytes = 32
			}
			if bytes > 0 {
				reg.Set(i2cCMD_READ | uint32(bytes))
				reg = nextI2CAddress(reg)
			}
			if split {
				readLast = true
				reg.Set(i2cCMD_READLAST | 1)
				reg = nextI2CAddress(reg)
				readTo = c.data[c.head : c.head+bytes+1]
				cmdIdx++
			} else {
				reg.Set(i2cCMD_END)
				readTo = c.data[c.head : c.head+bytes]
				reg = nil
			}

		case i2cCMD_STOP:
			reg.Set(i2cCMD_STOP)
			reg = nil
			cmdIdx++
		}

		if reg == nil {
			i2c.Bus.SetCTR_CONF_UPGATE(1)
			i2c.Bus.SetCTR_TRANS_START(1)
			end := nanotime() + timeoutNS
			var mask uint32
			for mask = i2c.Bus.INT_STATUS.Get(); mask&intMask == 0; mask = i2c.Bus.INT_STATUS.Get() {
				if nanotime() > end {
					if readTo != nil {
						return errI2CReadTimeout
					}
					return errI2CWriteTimeout
				}
			}
			switch {
			case mask&esp.I2C_INT_STATUS_NACK_INT_ST_Msk != 0 && !readLast:
				return errI2CAckExpected
			case mask&esp.I2C_INT_STATUS_TIME_OUT_INT_ST_Msk != 0:
				if readTo != nil {
					return errI2CReadTimeout
				}
				return errI2CWriteTimeout
			}
			i2c.Bus.INT_CLR.SetBits(intMask)
			for i := 0; i < len(readTo); i++ {
				readTo[i] = byte(i2c.Bus.GetDATA_FIFO_RDATA() & 0xff)
				c.head++
			}
			readTo = nil
			reg = &i2c.Bus.COMD0
		}
	}
	return nil
}

// Tx performs a single I2C transaction: write w then read r.
func (i2c *I2C) Tx(addr uint16, w, r []byte) (err error) {
	const timeout = 40
	cmd := i2c.txCmdBuf[:0]
	cmd = append(cmd, i2cCommand{cmd: i2cCMD_RSTART})
	if len(w) > 0 {
		cmd = append(cmd, i2cCommand{cmd: i2cCMD_WRITE, data: w})
	}
	if len(r) > 0 {
		cmd = append(cmd, i2cCommand{cmd: i2cCMD_READ, data: r})
	}
	cmd = append(cmd, i2cCommand{cmd: i2cCMD_STOP})
	return i2c.transmit(addr, cmd, timeout)
}

func (i2c *I2C) SetBaudRate(br uint32) error {
	return nil
}

func nextI2CAddress(reg *volatile.Register32) *volatile.Register32 {
	return (*volatile.Register32)(unsafe.Add(unsafe.Pointer(reg), 4))
}
