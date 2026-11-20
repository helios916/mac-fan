package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// chassisW is the fixed width of the machine schematic panel.
const chassisW = 54

// stretchMark is replaced by blank chassis rows so the panel can match the
// height of the sensor list.
const stretchMark = "\x00"

var tempPattern = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?°`)

// spinner glyphs rotate to suggest a turning fan (compact layout).
var spinner = [4]string{"◐", "◓", "◑", "◒"}

type chassisKind int

const (
	chassisDual   chassisKind = iota // 14"/16" Pro: one blower at each rear corner
	chassisSingle                    // 13" Pro: a single blower at the rear-left
	chassisAir                       // MacBook Air: fanless
)

// chassisKindFor maps the model identifier to the real internal arrangement.
func chassisKindFor(model string) chassisKind {
	if strings.HasPrefix(model, "MacBookAir") {
		return chassisAir
	}
	switch model {
	case "MacBookPro17,1", "Mac14,7", // 13" M1 / M2
		"MacBookPro16,3", "MacBookPro15,4", "MacBookPro14,1", "MacBookPro13,1":
		return chassisSingle
	}
	return chassisDual
}

func (u *UI) tempOf(key string) *Sensor {
	for _, s := range u.sensors {
		if s.Key == key {
			return s
		}
	}
	return nil
}

// tempSlot renders a fixed-width integer temperature token, e.g. " 62°".
func (u *UI) tempSlot(key string) string {
	s := u.tempOf(key)
	if s == nil || !s.Valid || s.Value <= 0 {
		return "   —"
	}
	return padLeft(fmt.Sprintf("%.0f°", s.Value), 4)
}

// colorTemps turns every "<number>°" token into a heat-coloured value.
func colorTemps(line string) string {
	return tempPattern.ReplaceAllStringFunc(line, func(m string) string {
		v, err := strconv.ParseFloat(strings.TrimSuffix(m, "°"), 64)
		if err != nil {
			return m
		}
		return cBold + heatSGR(v) + m + cReset
	})
}

func boxTop(title string, width int) string {
	inner := width - 2
	if title == "" {
		return "╭" + strings.Repeat("─", inner) + "╮"
	}
	t := " " + title + " "
	left := (inner - visWidth(t)) / 2
	if left < 0 {
		left = 0
	}
	right := inner - left - visWidth(t)
	if right < 0 {
		right = 0
	}
	return "╭" + strings.Repeat("─", left) + t + strings.Repeat("─", right) + "╮"
}

func boxBottom(width int) string {
	return "╰" + strings.Repeat("─", width-2) + "╯"
}

func boxLine(content string, width int) string {
	inner := width - 2
	content = padRight(content, inner-2)
	return "│" + " " + content + " " + "│"
}

/* ------------------------------------------------------------------ */
/* fan rotor                                                          */
/* ------------------------------------------------------------------ */

const (
	rotorW    = 7
	rotorHub  = 3
	fanBlockW = 16
)

// rotorBase draws an eight-blade blower; the highlighted blade rotates
// clockwise one step per frame. The hub turns hollow when the fan is idle.
var rotorBase = [3][]rune{
	[]rune("╲  │  ╱"),
	[]rune("─  ◉  ─"),
	[]rune("╱  │  ╲"),
}

type armPos struct{ row, col int }

var rotorArms = [8]armPos{{0, 3}, {0, 6}, {1, 6}, {2, 6}, {2, 3}, {2, 0}, {1, 0}, {0, 0}}

// hubSpin cycles the filled quadrant so the hub itself visibly turns.
var hubSpin = [4]rune{'◴', '◷', '◶', '◵'}

func rotor(frame int, spinning bool) [3]string {
	active := frame % len(rotorArms)
	// Two opposite blades glow each frame; stepping them around the hub makes
	// the rotor read as spinning even at low frame rates.
	b1 := rotorArms[active]
	b2 := rotorArms[(active+len(rotorArms)/2)%len(rotorArms)]
	var out [3]string
	for r := 0; r < 3; r++ {
		var b strings.Builder
		for c := 0; c < rotorW; c++ {
			ch := rotorBase[r][c]
			style := cDim
			switch {
			case r == 1 && c == rotorHub:
				if spinning {
					ch = hubSpin[frame%len(hubSpin)]
					style = cBold + cAccent
				} else {
					ch = '○'
				}
			case spinning && r == b1.row && c == b1.col,
				spinning && r == b2.row && c == b2.col:
				style = cBold + cAccent
			}
			b.WriteString(style)
			b.WriteString(string(ch))
			b.WriteString(cReset)
		}
		out[r] = b.String()
	}
	return out
}

// fanBlockLines lays out a rotor with its label and speed. The right-hand
// fan is mirrored so both blowers face into the middle of the machine.
func fanBlockLines(r [3]string, label, rpm string, mirrored bool) [3]string {
	var out [3]string
	if !mirrored {
		out[0] = padRight(r[0]+" "+label, fanBlockW)
		out[1] = padRight(r[1]+" "+rpm, fanBlockW)
		out[2] = padRight(r[2], fanBlockW)
	} else {
		out[0] = padLeft(label+" "+r[0], fanBlockW)
		out[1] = padLeft(rpm+" "+r[1], fanBlockW)
		out[2] = padLeft(r[2], fanBlockW)
	}
	return out
}

func (u *UI) fanRPM(i int) int {
	if i >= len(u.fans) || !u.fans[i].Available {
		return 0
	}
	return int(u.fans[i].Actual)
}

func (u *UI) fanGlyph(i int, frame int) string {
	if u.fanRPM(i) <= 0 {
		return "◌"
	}
	return spinner[frame%len(spinner)]
}

func (u *UI) rotorFor(i, frame int) [3]string {
	return rotor(frame, u.fanRPM(i) > 0)
}

/* ------------------------------------------------------------------ */
/* chassis                                                            */
/* ------------------------------------------------------------------ */

func centerIn(s string, width int) string {
	if visWidth(s) >= width {
		return s
	}
	return strings.Repeat(" ", (width-visWidth(s))/2) + s
}

func (u *UI) socBox(spread bool) []string {
	const socW = 46
	slot := func(label, key string) string { return label + " " + u.tempSlot(key) }
	pRow := func(labels [4]string, keys [4]string) string {
		return "       " + slot(labels[0], keys[0]) + "  " + slot(labels[1], keys[1]) +
			"  " + slot(labels[2], keys[2]) + "  " + slot(labels[3], keys[3])
	}

	rows := []string{
		"▣ CPU  " + slot("E1", "Tp09") + "    " + slot("E2", "Tp0T"),
		pRow([4]string{"P1", "P2", "P3", "P4"}, [4]string{"Tp01", "Tp05", "Tp0D", "Tp0H"}),
		pRow([4]string{"P5", "P6", "P7", "P8"}, [4]string{"Tp0L", "Tp0P", "Tp0X", "Tp0b"}),
		"▦ GPU  " + slot("G1", "Tg04") + "  " + slot("G2", "Tg05") + "  " + slot("G3", "Tg0C") + "  " + slot("G4", "Tg0D"),
		"◧ SOC  " + slot("S1", "Ts00") + "  " + slot("S2", "Ts02") + "  " + slot("S3", "Ts04") + "  " + slot("S4", "Ts06"),
		"▤ MEM  " + slot("M1", "Tm00") + "  " + slot("M2", "Tm02") + "  " + slot("M3", "Tm04") + "  " + slot("M4", "Tm06"),
	}

	// Roomy layout: blank rows between functional blocks. The dense variant
	// keeps the same alignment when vertical space is tight.
	if spread {
		rows = []string{
			rows[0], rows[1], rows[2],
			"",
			rows[3],
			"",
			rows[4],
			"",
			rows[5],
		}
	}

	lines := []string{boxTop("SOC PACKAGE", socW)}
	for _, r := range rows {
		lines = append(lines, boxLine(r, socW))
	}
	lines = append(lines, boxBottom(socW))
	return lines
}

func (u *UI) batteryBox() []string {
	const batW = 50
	b1 := "B1 " + u.tempSlot("TB0T")
	b2 := "B2 " + u.tempSlot("TB1T")
	b3 := "B3 " + u.tempSlot("TB2T")
	gap := (batW - 4 - visWidth(b1) - visWidth(b2) - visWidth(b3)) / 2
	if gap < 1 {
		gap = 1
	}
	return []string{
		boxTop("▰ BATTERY", batW),
		boxLine(b1+strings.Repeat(" ", gap)+b2+strings.Repeat(" ", gap)+b3, batW),
		boxBottom(batW),
	}
}

func (u *UI) ssdLine() string {
	return "▥ SSD  " + "N1 " + u.tempSlot("TH0a") + " N2 " + u.tempSlot("TH0b") + " N3 " + u.tempSlot("TH0x")
}

func (u *UI) boardLine() string {
	return "◈ BD1 " + u.tempSlot("TPD0") + "   ◈ BD2 " + u.tempSlot("TPD5")
}

func (u *UI) ventLine(inner int) string {
	wf := "((•)) WF1 " + u.tempSlot("TW0P")
	bar := inner - 3 - visWidth(wf)
	if bar < 0 {
		bar = 0
	}
	return " " + cDim + strings.Repeat("▚", bar) + cReset + " " + wf + " "
}

func (u *UI) speakerRow(inner int) string {
	return " ◁ SPK" + strings.Repeat(" ", inner-12) + "SPK ▷ "
}

func (u *UI) fanRowDualCompact(inner int, frame int) string {
	g0, g1 := u.fanGlyph(0, frame), u.fanGlyph(1, frame)
	rpm0 := fmt.Sprintf("%d RPM", u.fanRPM(0))
	rpm1 := fmt.Sprintf("%d RPM", u.fanRPM(1))
	left := " " + g0 + " FAN 0 " + rpm0
	right := " FAN 1 " + rpm1 + " " + g1 + " "
	gap := inner - visWidth(left) - visWidth(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (u *UI) chassisBody(kind chassisKind, full, spread bool, frame int) []string {
	inner := chassisW - 2
	wrap := func(s string) string { return "│" + padRight(s, inner) + "│" }
	blank := wrap("")

	lines := make([]string, 0, 24)
	lines = append(lines, "╭"+strings.Repeat("─", inner)+"╮")
	lines = append(lines, wrap(u.ventLine(inner)))

	single := kind == chassisSingle
	air := kind == chassisAir

	if kind == chassisDual {
		rpm0 := fmt.Sprintf("%d RPM", u.fanRPM(0))
		rpm1 := fmt.Sprintf("%d RPM", u.fanRPM(1))
		if full {
			rl := fanBlockLines(u.rotorFor(0, frame), "FAN 0", rpm0, false)
			rr := fanBlockLines(u.rotorFor(1, frame), "FAN 1", rpm1, true)
			gap := inner - 12 - 2*fanBlockW
			if gap < 1 {
				gap = 1
			}
			for i := 0; i < 3; i++ {
				lines = append(lines, wrap("      "+rl[i]+strings.Repeat(" ", gap)+rr[i]+"      "))
			}
			lines = append(lines, blank)
		} else {
			lines = append(lines, wrap(u.fanRowDualCompact(inner, frame)))
		}
	}

	if single {
		rpm0 := fmt.Sprintf("%d RPM", u.fanRPM(0))
		if full {
			rl := fanBlockLines(u.rotorFor(0, frame), "FAN 0", rpm0, false)
			for i := 0; i < 3; i++ {
				lines = append(lines, wrap("      "+rl[i]))
			}
			lines = append(lines, blank)
		} else {
			lines = append(lines, wrap(" "+u.fanGlyph(0, frame)+" FAN 0 "+rpm0))
		}
	}

	if air && !full {
		lines = append(lines, wrap(" "+cDim+"fanless · passive cooling"+cReset))
	}

	// Vertical stretch points: the chassis frame is padded here so its bottom
	// edge lines up with the sensor list on the right.
	lines = append(lines, stretchMark)

	// The M1 package sits in the middle of the board between the two blowers;
	// on the 13" and the Air the board lives in the rear-right corner.
	soc := u.socBox(spread)
	for _, s := range soc {
		var p int
		if kind == chassisDual {
			p = (inner - 46) / 2
		} else {
			p = inner - 46 - 1
		}
		if p < 0 {
			p = 0
		}
		lines = append(lines, wrap(strings.Repeat(" ", p)+s+" "))
	}

	lines = append(lines, wrap(centerIn(u.ssdLine(), inner)))
	lines = append(lines, wrap(centerIn(u.boardLine(), inner)))
	lines = append(lines, stretchMark)
	if full {
		lines = append(lines, wrap(u.speakerRow(inner)))
	}

	for _, s := range u.batteryBox() {
		lines = append(lines, wrap(" "+s+" "))
	}
	lines = append(lines, "╰"+strings.Repeat("─", inner)+"╯")
	return lines
}

// chassisLines draws a top-down schematic that follows the real hardware
// layout of the detected series: rear vents and antenna, blower positions,
// the SoC package, NAND, board sensors and the battery across the bottom.
func (u *UI) chassisLines(width, avail, minHeight int) []string {
	if width < chassisW {
		return nil
	}
	kind := chassisKindFor(u.model)
	frame := int(time.Now().UnixMilli() / 125)
	lines := u.chassisBody(kind, true, true, frame)
	if len(lines) > avail {
		lines = u.chassisBody(kind, true, false, frame)
	}
	if len(lines) > avail {
		lines = u.chassisBody(kind, false, false, frame)
	}

	// Grow the frame at its stretch points until it reaches the height of the
	// right-hand column, so both panels end on the same row. Stretch points
	// are always consumed — even when no growth is needed — so the sentinel
	// byte never leaks into the rendered frame (an unconsumed "\x00" renders
	// invisibly and would erase the left border on that row).
	if minHeight > avail {
		minHeight = avail
	}
	marks := 0
	for _, l := range lines {
		if l == stretchMark {
			marks++
		}
	}
	if marks > 0 {
		base := len(lines) - marks
		extra := minHeight - base
		if extra < 0 {
			extra = 0
		}
		per, rem := extra/marks, extra%marks
		out := make([]string, 0, base+extra)
		blank := "│" + strings.Repeat(" ", chassisW-2) + "│"
		for _, l := range lines {
			if l != stretchMark {
				out = append(out, l)
				continue
			}
			n := per
			if rem > 0 {
				n++
				rem--
			}
			for i := 0; i < n; i++ {
				out = append(out, blank)
			}
		}
		lines = out
	}

	for i, l := range lines {
		lines[i] = colorTemps(l)
	}
	if len(lines) > avail {
		lines = lines[:avail]
	}
	return lines
}
