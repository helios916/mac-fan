package main

import (
	"fmt"
	"strings"
)

const (
	// One sample per second; the curve keeps the most recent 30 seconds.
	histLen  = 30
	sparkMax = 24
)

type Group int

const (
	groupECore Group = iota
	groupPCore
	groupGPU
	groupSoC
	groupMemory
	groupBattery
	groupStorage
	groupWiFi
	groupBoard
	groupCount
)

func (g Group) Title() string {
	switch g {
	case groupECore:
		return "E-CORES"
	case groupPCore:
		return "P-CORES"
	case groupGPU:
		return "GPU"
	case groupSoC:
		return "SOC"
	case groupMemory:
		return "MEMORY"
	case groupBattery:
		return "BATTERY"
	case groupStorage:
		return "STORAGE"
	case groupWiFi:
		return "WIFI"
	case groupBoard:
		return "BOARD"
	}
	return "?"
}

type Sensor struct {
	Key   string
	Name  string
	Group Group

	Present bool
	Valid   bool
	Value   float64

	hist  [histLen]float64
	count int
	head  int
}

var sensorTable = []struct {
	key   string
	name  string
	group Group
}{
	// M1 family core map (same keys mxmon uses: 2 E-cores, 8 P-cores).
	{"Tp09", "E-Core 1", groupECore},
	{"Tp0T", "E-Core 2", groupECore},

	{"Tp01", "P-Core 1", groupPCore},
	{"Tp05", "P-Core 2", groupPCore},
	{"Tp0D", "P-Core 3", groupPCore},
	{"Tp0H", "P-Core 4", groupPCore},
	{"Tp0L", "P-Core 5", groupPCore},
	{"Tp0P", "P-Core 6", groupPCore},
	{"Tp0X", "P-Core 7", groupPCore},
	{"Tp0b", "P-Core 8", groupPCore},

	{"Tg04", "GPU 1", groupGPU},
	{"Tg05", "GPU 2", groupGPU},
	{"Tg0C", "GPU 3", groupGPU},
	{"Tg0D", "GPU 4", groupGPU},

	{"Ts00", "SoC 1", groupSoC},
	{"Ts02", "SoC 2", groupSoC},
	{"Ts04", "SoC 3", groupSoC},
	{"Ts06", "SoC 4", groupSoC},

	{"Tm00", "Memory 1", groupMemory},
	{"Tm02", "Memory 2", groupMemory},
	{"Tm04", "Memory 3", groupMemory},
	{"Tm06", "Memory 4", groupMemory},

	{"TB0T", "Battery 1", groupBattery},
	{"TB1T", "Battery 2", groupBattery},
	{"TB2T", "Battery 3", groupBattery},

	{"TH0a", "NAND 1", groupStorage},
	{"TH0b", "NAND 2", groupStorage},
	{"TH0x", "NAND 3", groupStorage},

	{"TW0P", "WiFi 1", groupWiFi},

	{"TPD0", "Board 1", groupBoard},
	{"TPD5", "Board 2", groupBoard},
}

func scanSensors(smc *SMC) []*Sensor {
	sensors := make([]*Sensor, 0, len(sensorTable))
	for _, entry := range sensorTable {
		v, _, err := smc.ReadNumber(entry.key)
		if err != nil || v <= 0 || v >= 150 {
			continue
		}
		s := &Sensor{Key: entry.key, Name: entry.name, Group: entry.group,
			Present: true, Valid: true, Value: v}
		// Start with a flat line so the sparkline has a useful shape at launch.
		for i := 0; i < histLen; i++ {
			s.hist[i] = v
		}
		s.count = histLen
		sensors = append(sensors, s)
	}
	return sensors
}

func sampleSensors(smc *SMC, sensors []*Sensor) {
	for _, s := range sensors {
		v, _, err := smc.ReadNumber(s.Key)
		if err == nil && v > 0 && v < 150 {
			s.Value = v
			s.Valid = true
		}
		if s.Valid {
			s.push(s.Value)
		} else {
			s.push(0)
		}
	}
}

func (s *Sensor) push(v float64) {
	s.hist[s.head] = v
	s.head = (s.head + 1) % histLen
	if s.count < histLen {
		s.count++
	}
}

// at returns the sample `age` seconds ago (0 = newest).
func (s *Sensor) at(age int) float64 {
	if age < 0 || age >= s.count {
		return 0
	}
	idx := s.head - 1 - age
	for idx < 0 {
		idx += histLen
	}
	return s.hist[idx%histLen]
}

func peakTemp(sensors []*Sensor) float64 {
	peak := -1.0
	for _, s := range sensors {
		if s.Valid && s.Value > peak {
			peak = s.Value
		}
	}
	return peak
}

func groupPeak(sensors []*Sensor, g Group) float64 {
	peak := -1.0
	for _, s := range sensors {
		if s.Group == g && s.Valid && s.Value > peak {
			peak = s.Value
		}
	}
	return peak
}

func groupSensors(sensors []*Sensor, g Group) []*Sensor {
	out := make([]*Sensor, 0, 8)
	for _, s := range sensors {
		if s.Group == g {
			out = append(out, s)
		}
	}
	return out
}

// heatSGRs holds one colour per temperature bucket; looking the string up
// avoids a fmt.Sprintf call for every sparkline cell on every frame.
var heatSGRs = [9]string{
	"\x1b[38;5;244m", // invalid / below ambient
	"\x1b[38;5;39m",  // <= 35
	"\x1b[38;5;45m",  // <= 45
	"\x1b[38;5;114m", // <= 55
	"\x1b[38;5;149m", // <= 62
	"\x1b[38;5;214m", // <= 70
	"\x1b[38;5;208m", // <= 78
	"\x1b[38;5;203m", // <= 86
	"\x1b[38;5;196m", // hotter
}

func heatLevel(v float64) int {
	switch {
	case v < 0:
		return 0
	case v <= 35:
		return 1
	case v <= 45:
		return 2
	case v <= 55:
		return 3
	case v <= 62:
		return 4
	case v <= 70:
		return 5
	case v <= 78:
		return 6
	case v <= 86:
		return 7
	default:
		return 8
	}
}

func heatSGR(v float64) string {
	return heatSGRs[heatLevel(v)]
}

var sparkGlyphs = [8]string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

func sparkLevel(v float64) int {
	if v <= 0 {
		return -1
	}
	t := (v - 25.0) / 70.0
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return int(t*7.0 + 0.5)
}

func sparkline(s *Sensor, width int) string {
	if width <= 0 {
		return ""
	}
	if width > sparkMax {
		width = sparkMax
	}
	n := s.count
	if n > histLen {
		n = histLen
	}
	if n == 0 {
		return strings.Repeat(" ", width)
	}

	var b strings.Builder
	if n < width {
		b.WriteString(strings.Repeat(" ", width-n))
		for i := 0; i < n; i++ {
			b.WriteString(sparkCell(s.at(n - 1 - i)))
		}
		return b.String()
	}

	// Compress the whole window into `width` buckets, oldest sample on the left.
	for i := 0; i < width; i++ {
		start := i * n / width
		end := (i + 1) * n / width
		if end <= start {
			end = start + 1
		}
		sum, cnt := 0.0, 0
		for p := start; p < end; p++ {
			if v := s.at(n - 1 - p); v > 0 {
				sum += v
				cnt++
			}
		}
		if cnt == 0 {
			b.WriteString(" ")
			continue
		}
		b.WriteString(sparkCell(sum / float64(cnt)))
	}
	return b.String()
}

func sparkCell(v float64) string {
	lvl := sparkLevel(v)
	if lvl < 0 {
		return " "
	}
	return heatSGR(v) + sparkGlyphs[lvl] + cReset
}

func degreeText(v float64) string {
	return fmt.Sprintf("%.1f°C", v)
}
