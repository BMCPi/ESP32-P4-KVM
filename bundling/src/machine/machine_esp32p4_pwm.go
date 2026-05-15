//go:build esp32p4

package machine

// PWM (LEDC) driver for the ESP32-P4 target.
// Adapted from machine_esp32s3_pwm.go + machine_esp32xx_pwm.go.
// Uses HP_SYS_CLKRST for clock/reset control (replaces SYSTEM on S3/C3).
// P4 LEDC register fields use _CH / _TIMER suffixes and lack DUTY_CYCLE/NUM/INC.

import (
	"device/esp"
	"errors"
)

// GPIO matrix output signal index for LEDC channel 0 on ESP32-P4.
const LEDC_LS_SIG_OUT0_IDX = uint32(126)

const ledcChannelsP4 = 8

const ledcApbClock = 80_000_000

const ledcDutyFracBits = 4

const ledcDividerFracBits = 8

var errPWMNoChannel = errors.New("pwm: no free channel")

type LEDCPWM struct {
	SigOutBase  uint32
	NumChannels uint8
	timerNum    uint8
	dutyRes     uint8
	configured  bool
	channelPin  [8]Pin
}

type ledcChanOp uint8

const (
	ledcChanOpInit      ledcChanOp = iota
	ledcChanOpSetDuty
	ledcChanOpSetInvert
)

var (
	PWM0 = &LEDCPWM{SigOutBase: LEDC_LS_SIG_OUT0_IDX, NumChannels: ledcChannelsP4, timerNum: 0}
	PWM1 = &LEDCPWM{SigOutBase: LEDC_LS_SIG_OUT0_IDX, NumChannels: ledcChannelsP4, timerNum: 1}
	PWM2 = &LEDCPWM{SigOutBase: LEDC_LS_SIG_OUT0_IDX, NumChannels: ledcChannelsP4, timerNum: 2}
	PWM3 = &LEDCPWM{SigOutBase: LEDC_LS_SIG_OUT0_IDX, NumChannels: ledcChannelsP4, timerNum: 3}
)

func (pwm *LEDCPWM) Configure(config PWMConfig) error {
	// Reset and enable LEDC clock via HP_SYS_CLKRST.
	esp.HP_SYS_CLKRST.SetHP_RST_EN1_REG_RST_EN_LEDC(1)
	esp.HP_SYS_CLKRST.SetSOC_CLK_CTRL3_REG_LEDC_APB_CLK_EN(1)
	esp.HP_SYS_CLKRST.SetPERI_CLK_CTRL22_REG_LEDC_CLK_EN(1)
	esp.HP_SYS_CLKRST.SetHP_RST_EN1_REG_RST_EN_LEDC(0)

	// LEDC global: APB clock source, enable internal clock.
	esp.LEDC.SetCONF_APB_CLK_SEL(1)
	esp.LEDC.SetCONF_CLK_EN(1)

	period := config.Period
	if period == 0 {
		period = 1_000_000
	}
	freq := uint64(1e9) / period
	dutyRes := uint8(10)
	switch {
	case freq < 100:
		dutyRes = 14
	case freq < 1000:
		dutyRes = 12
	case freq > 100_000:
		dutyRes = 8
	}

	divActual := ledcApbClock / (uint32(freq) * (1 << dutyRes))
	if divActual == 0 {
		divActual = 1
	}
	divReg := divActual << ledcDividerFracBits
	if divReg > 0x3ffff {
		return ErrPWMPeriodTooLong
	}

	pwm.setTimerConf(dutyRes, divReg)
	pwm.dutyRes = dutyRes
	pwm.configured = true
	for i := range pwm.channelPin {
		pwm.channelPin[i] = NoPin
	}
	return nil
}

func (pwm *LEDCPWM) setTimerConf(dutyRes uint8, divReg uint32) {
	switch pwm.timerNum {
	case 0:
		esp.LEDC.SetTIMER0_CONF_TIMER_DUTY_RES(uint32(dutyRes))
		esp.LEDC.SetTIMER0_CONF_CLK_DIV_TIMER(divReg)
		esp.LEDC.SetTIMER0_CONF_TICK_SEL_TIMER(0)
		esp.LEDC.SetTIMER0_CONF_TIMER_PAUSE(0)
		esp.LEDC.SetTIMER0_CONF_TIMER_RST(1)
		esp.LEDC.SetTIMER0_CONF_TIMER_RST(0)
		esp.LEDC.SetTIMER0_CONF_TIMER_PARA_UP(1)
	case 1:
		esp.LEDC.SetTIMER1_CONF_TIMER_DUTY_RES(uint32(dutyRes))
		esp.LEDC.SetTIMER1_CONF_CLK_DIV_TIMER(divReg)
		esp.LEDC.SetTIMER1_CONF_TICK_SEL_TIMER(0)
		esp.LEDC.SetTIMER1_CONF_TIMER_PAUSE(0)
		esp.LEDC.SetTIMER1_CONF_TIMER_RST(1)
		esp.LEDC.SetTIMER1_CONF_TIMER_RST(0)
		esp.LEDC.SetTIMER1_CONF_TIMER_PARA_UP(1)
	case 2:
		esp.LEDC.SetTIMER2_CONF_TIMER_DUTY_RES(uint32(dutyRes))
		esp.LEDC.SetTIMER2_CONF_CLK_DIV_TIMER(divReg)
		esp.LEDC.SetTIMER2_CONF_TICK_SEL_TIMER(0)
		esp.LEDC.SetTIMER2_CONF_TIMER_PAUSE(0)
		esp.LEDC.SetTIMER2_CONF_TIMER_RST(1)
		esp.LEDC.SetTIMER2_CONF_TIMER_RST(0)
		esp.LEDC.SetTIMER2_CONF_TIMER_PARA_UP(1)
	case 3:
		esp.LEDC.SetTIMER3_CONF_TIMER_DUTY_RES(uint32(dutyRes))
		esp.LEDC.SetTIMER3_CONF_CLK_DIV_TIMER(divReg)
		esp.LEDC.SetTIMER3_CONF_TICK_SEL_TIMER(0)
		esp.LEDC.SetTIMER3_CONF_TIMER_PAUSE(0)
		esp.LEDC.SetTIMER3_CONF_TIMER_RST(1)
		esp.LEDC.SetTIMER3_CONF_TIMER_RST(0)
		esp.LEDC.SetTIMER3_CONF_TIMER_PARA_UP(1)
	}
}

func (pwm *LEDCPWM) Channel(pin Pin) (uint8, error) {
	if !pwm.configured {
		return 0, errors.New("pwm: not configured")
	}
	if pin == NoPin {
		return 0, ErrInvalidOutputPin
	}
	var ch uint8
	for ch = 0; ch < pwm.NumChannels; ch++ {
		if pwm.channelPin[ch] == NoPin {
			break
		}
	}
	if ch >= pwm.NumChannels {
		return 0, errPWMNoChannel
	}
	pwm.channelPin[ch] = pin
	signal := pwm.SigOutBase + uint32(ch)
	pin.configure(PinConfig{Mode: PinOutput}, signal)
	pwm.chanOp(ch, ledcChanOpInit, 0, false)
	return ch, nil
}

func (pwm *LEDCPWM) Set(channel uint8, value uint32) {
	if channel >= pwm.NumChannels {
		return
	}
	top := uint32(1<<pwm.dutyRes) - 1
	if value > top {
		value = top
	}
	dutyVal := value << ledcDutyFracBits
	pwm.chanOp(channel, ledcChanOpSetDuty, dutyVal, false)
}

func (pwm *LEDCPWM) Top() uint32 {
	if !pwm.configured {
		return 0
	}
	return uint32(1<<pwm.dutyRes) - 1
}

func (pwm *LEDCPWM) SetInverting(channel uint8, inverting bool) {
	if channel >= pwm.NumChannels {
		return
	}
	pwm.chanOp(channel, ledcChanOpSetInvert, 0, inverting)
}

// chanOp implements LEDC low-speed channel operations for ESP32-P4 (channels 0-7).
// P4 LEDC fields use _CH / _TIMER suffixes and lack DUTY_CYCLE/NUM/INC fields.
func (pwm *LEDCPWM) chanOp(ch uint8, op ledcChanOp, duty uint32, inverting bool) {
	invVal := uint32(0)
	if inverting {
		invVal = 1
	}
	switch ch {
	case 0:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH0_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH0_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH0_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH0_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH0_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH0_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH0_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH0_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH0_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH0_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH0_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH0_CONF0_IDLE_LV_CH(invVal)
		}
	case 1:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH1_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH1_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH1_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH1_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH1_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH1_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH1_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH1_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH1_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH1_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH1_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH1_CONF0_IDLE_LV_CH(invVal)
		}
	case 2:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH2_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH2_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH2_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH2_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH2_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH2_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH2_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH2_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH2_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH2_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH2_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH2_CONF0_IDLE_LV_CH(invVal)
		}
	case 3:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH3_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH3_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH3_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH3_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH3_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH3_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH3_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH3_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH3_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH3_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH3_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH3_CONF0_IDLE_LV_CH(invVal)
		}
	case 4:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH4_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH4_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH4_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH4_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH4_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH4_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH4_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH4_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH4_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH4_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH4_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH4_CONF0_IDLE_LV_CH(invVal)
		}
	case 5:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH5_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH5_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH5_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH5_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH5_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH5_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH5_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH5_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH5_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH5_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH5_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH5_CONF0_IDLE_LV_CH(invVal)
		}
	case 6:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH6_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH6_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH6_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH6_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH6_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH6_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH6_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH6_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH6_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH6_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH6_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH6_CONF0_IDLE_LV_CH(invVal)
		}
	case 7:
		switch op {
		case ledcChanOpInit:
			esp.LEDC.SetCH7_CONF0_TIMER_SEL_CH(uint32(pwm.timerNum))
			esp.LEDC.SetCH7_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH7_CONF0_IDLE_LV_CH(0)
			esp.LEDC.SetCH7_HPOINT_HPOINT_CH(0)
			esp.LEDC.SetCH7_DUTY_DUTY_CH(0)
			esp.LEDC.SetCH7_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH7_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetDuty:
			esp.LEDC.SetCH7_DUTY_DUTY_CH(duty)
			esp.LEDC.SetCH7_CONF1_DUTY_START_CH(1)
			esp.LEDC.SetCH7_CONF0_SIG_OUT_EN_CH(1)
			esp.LEDC.SetCH7_CONF0_PARA_UP_CH(1)
		case ledcChanOpSetInvert:
			esp.LEDC.SetCH7_CONF0_IDLE_LV_CH(invVal)
		}
	}
}
