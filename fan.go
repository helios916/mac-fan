package main

import (
	"fmt"
	"math"
	"time"
)

const (
	maxFans    = 8
	modeSystem = 3
	modeManual = 1
)

type Fan struct {
	Index int

	Available bool
	HasActual bool
	HasMin    bool
	HasMax    bool
	HasTarget bool
	HasMode   bool

	Actual float64
	Min    float64
	Max    float64
	Target float64
	Mode   uint8

	TypeTarget uint32
	TypeMode   uint32

	TargetKey string
	ModeKey   string

	OriginalMode uint8
	Manual       bool
	SetTarget    float64
	lastApply    time.Time
}

var (
	ftstPresent  bool
	ftstOriginal uint8
	ftstUnlocked bool
)

func readNumber(smc *SMC, key string) (float64, uint32, bool) {
	v, typ, err := smc.ReadNumber(key)
	if err != nil {
		return 0, 0, false
	}
	return v, typ, true
}

func readModeKey(smc *SMC, index int) (key string, typ uint32, value uint8, ok bool) {
	for _, suffix := range []string{"Md", "md"} {
		k := fmt.Sprintf("F%d%s", index, suffix)
		_, data, err := smc.ReadKey(k)
		if err == nil && len(data) >= 1 {
			typ = fourCC("ui8 ")
			return k, typ, data[0], true
		}
	}
	return "", 0, 0, false
}

func scanFans(smc *SMC) []*Fan {
	ftstPresent = false
	ftstUnlocked = false
	ftstOriginal = 0

	count := 0
	if _, data, err := smc.ReadKey("FNum"); err == nil && len(data) >= 1 {
		count = int(data[0])
		if count > maxFans {
			count = maxFans
		}
	} else {
		for i := 0; i < maxFans; i++ {
			if _, _, err := smc.ReadKey(fmt.Sprintf("F%dAc", i)); err == nil {
				count = i + 1
			}
		}
	}

	if v, err := smc.ReadU8("Ftst"); err == nil {
		ftstPresent = true
		ftstOriginal = v
	}

	fans := make([]*Fan, 0, count)
	for i := 0; i < count; i++ {
		f := &Fan{Index: i, TargetKey: fmt.Sprintf("F%dTg", i)}

		if v, _, ok := readNumber(smc, fmt.Sprintf("F%dAc", i)); ok {
			f.Actual, f.HasActual = v, true
		}
		if v, _, ok := readNumber(smc, fmt.Sprintf("F%dMn", i)); ok {
			f.Min, f.HasMin = v, true
		}
		if v, _, ok := readNumber(smc, fmt.Sprintf("F%dMx", i)); ok {
			f.Max, f.HasMax = v, true
		}
		if v, typ, ok := readNumber(smc, f.TargetKey); ok {
			f.Target, f.TypeTarget, f.HasTarget = v, typ, true
		}
		if key, typ, mode, ok := readModeKey(smc, i); ok {
			f.ModeKey, f.TypeMode = key, typ
			f.Mode, f.OriginalMode, f.HasMode = mode, mode, true
		}
		f.Available = f.HasActual || f.HasMax
		f.SetTarget = f.Target
		if !f.HasTarget || f.SetTarget <= 0 {
			f.SetTarget = f.Min
		}
		fans = append(fans, f)
	}
	return fans
}

func anyFanSpinning(fans []*Fan) bool {
	for _, f := range fans {
		if f.Available && f.Actual > 0 {
			return true
		}
	}
	return false
}

func refreshFans(smc *SMC, fans []*Fan) {
	for _, f := range fans {
		if v, _, ok := readNumber(smc, fmt.Sprintf("F%dAc", f.Index)); ok {
			f.Actual, f.HasActual = v, true
		}
		if v, typ, ok := readNumber(smc, f.TargetKey); ok {
			f.Target, f.TypeTarget, f.HasTarget = v, typ, true
		}
		if key, typ, mode, ok := readModeKey(smc, f.Index); ok {
			f.ModeKey, f.TypeMode, f.Mode, f.HasMode = key, typ, mode, true
		}
	}
}

func clampRPM(f *Fan, rpm float64) float64 {
	if f.HasMin && rpm < f.Min {
		rpm = f.Min
	}
	if f.HasMax && rpm > f.Max {
		rpm = f.Max
	}
	if rpm < 0 {
		rpm = 0
	}
	return rpm
}

func (f *Fan) autoModeValue() uint8 {
	if f.OriginalMode == 0 || f.OriginalMode == modeSystem {
		return f.OriginalMode
	}
	return modeSystem
}

func applyManual(smc *SMC, f *Fan) error {
	if ftstPresent && !ftstUnlocked {
		if err := smc.WriteU8("Ftst", 1); err != nil {
			return err
		}
		ftstUnlocked = true
	}
	if err := smc.WriteKey(f.ModeKey, f.TypeMode, []byte{modeManual}); err != nil {
		return err
	}
	if !f.HasTarget || f.TypeTarget == 0 {
		return smcError(-4)
	}
	return smc.WriteNumber(f.TargetKey, f.TypeTarget, f.SetTarget)
}

func (f *Fan) SetManual(smc *SMC, rpm float64) error {
	if !f.Available || !f.HasTarget || !f.HasMode {
		return smcError(-4)
	}
	f.SetTarget = math.Round(clampRPM(f, rpm))
	f.Manual = true
	f.lastApply = time.Now()
	if err := applyManual(smc, f); err != nil {
		f.Manual = false
		return err
	}
	return nil
}

func (f *Fan) SetAuto(smc *SMC) error {
	f.Manual = false
	if !f.HasMode {
		return smcError(-4)
	}
	return smc.WriteKey(f.ModeKey, f.TypeMode, []byte{f.autoModeValue()})
}

func reassertFans(smc *SMC, fans []*Fan) {
	now := time.Now()
	for _, f := range fans {
		if !f.Manual || !f.HasMode {
			continue
		}
		if now.Sub(f.lastApply) < time.Second {
			continue
		}
		if f.Mode != modeManual {
			if applyManual(smc, f) == nil {
				f.lastApply = now
			}
		} else if math.Abs(f.Target-f.SetTarget) > 6.0 {
			if f.HasTarget && f.TypeTarget != 0 {
				if smc.WriteNumber(f.TargetKey, f.TypeTarget, f.SetTarget) == nil {
					f.lastApply = now
				}
			}
		}
	}
}

func restoreAll(smc *SMC, fans []*Fan) error {
	var firstErr error
	for _, f := range fans {
		if !f.HasMode {
			continue
		}
		if err := smc.WriteKey(f.ModeKey, f.TypeMode, []byte{f.autoModeValue()}); err != nil && firstErr == nil {
			firstErr = err
		}
		f.Manual = false
	}
	if ftstPresent && ftstUnlocked {
		if err := smc.WriteU8("Ftst", ftstOriginal); err != nil && firstErr == nil {
			firstErr = err
		}
		ftstUnlocked = false
	}
	return firstErr
}

func (f *Fan) ModeLabel() string {
	if !f.HasMode {
		return "UNKNOWN"
	}
	switch f.Mode {
	case modeManual:
		return "MANUAL"
	case modeSystem:
		return "AUTO"
	case 0:
		return "AUTO"
	default:
		return fmt.Sprintf("MODE %d", f.Mode)
	}
}

func (f *Fan) ModeColor() string {
	if !f.HasMode {
		return cErr
	}
	switch f.Mode {
	case modeManual:
		return cWarn
	case modeSystem, 0:
		return cOK
	default:
		return cErr
	}
}
