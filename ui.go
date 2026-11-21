package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	cReset  = "\x1b[0m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[38;5;244m"
	cAccent = "\x1b[38;5;45m"
	cBrand  = "\x1b[38;5;114m"
	cText   = "\x1b[38;5;252m"
	cOK     = "\x1b[38;5;114m"
	cWarn   = "\x1b[38;5;214m"
	cErr    = "\x1b[38;5;203m"
	cPurple = "\x1b[38;5;177m"
)

// Precomposed fragments keep fixed styling out of per-frame concatenations
// inside strings.Builder.WriteString calls.
const (
	selMark     = cAccent + cBold + " ▸ " + cReset
	checkedMark = cOK + "●" + cReset + " "
	emptyMark   = cDim + "○" + cReset + " "
	noKeysMsg   = "  " + cDim + "no readable SMC keys" + cReset
	targetLabel = cDim + "   target " + cReset
	barOpen     = cDim + "[" + cReset
	barMid      = cReset + cDim
	barClose    = cReset + cDim + "]" + cReset
	lineEnd     = cReset + "\x1b[K"
)

const (
	statusInfo = iota
	statusOK
	statusErr
)

type UI struct {
	sensors []*Sensor
	fans    []*Fan
	smc     *SMC

	model    string
	chip     string
	writable bool

	sel      int
	checked  []bool
	status   string
	kind     int
	expires  time.Time
	quitting bool

	scroll    int
	maxScroll int
	buttons   []uiButton
}

// uiButton describes a clickable cell range on one rendered row. A negative
// sel means the button runs a key action; otherwise it selects that fan.
type uiButton struct {
	y, x0, x1 int
	action    key
	sel       int
}

func newUI(smc *SMC, fans []*Fan, sensors []*Sensor, model, chip string, writable bool) *UI {
	return &UI{
		smc: smc, fans: fans, sensors: sensors,
		model: model, chip: chip, writable: writable,
		checked: make([]bool, len(fans)),
	}
}

/* ------------------------------------------------------------------ */
/* text helpers                                                       */
/* ------------------------------------------------------------------ */

func skipANSI(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return j
	}
	if s[j] == '[' {
		j++
		for j < len(s) {
			c := s[j]
			j++
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		return j
	}
	return j + 1
}

func visWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipANSI(s, i)
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		w++
		i += size
	}
	return w
}

func truncate(s string, max int) string {
	if max < 0 {
		max = 0
	}
	var b strings.Builder
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := skipANSI(s, i)
			b.WriteString(s[i:j])
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if w+1 > max {
			break
		}
		b.WriteString(s[i : i+size])
		w++
		i += size
	}
	return b.String()
}

func padRight(s string, width int) string {
	if visWidth(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visWidth(s))
}

func padLeft(s string, width int) string {
	if visWidth(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-visWidth(s)) + s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

/* ------------------------------------------------------------------ */
/* status                                                             */
/* ------------------------------------------------------------------ */

func (u *UI) setStatus(kind int, format string, args ...interface{}) {
	u.status = fmt.Sprintf(format, args...)
	u.kind = kind
	u.expires = time.Now().Add(4 * time.Second)
}

func (u *UI) needWrite(action string) bool {
	if u.writable {
		return true
	}
	u.setStatus(statusErr, "Read-only: run with sudo mac-fan to %s", action)
	return false
}

/* ------------------------------------------------------------------ */
/* fan actions                                                        */
/* ------------------------------------------------------------------ */

func (u *UI) checkedCount() int {
	n := 0
	for _, c := range u.checked {
		if c {
			n++
		}
	}
	return n
}

func (u *UI) targets() []*Fan {
	out := make([]*Fan, 0, len(u.fans))
	for i, f := range u.fans {
		if u.checked[i] {
			out = append(out, f)
		}
	}
	if len(out) == 0 && len(u.fans) > 0 && u.fans[u.sel].Available {
		out = append(out, u.fans[u.sel])
	}
	return out
}

func baseRPM(f *Fan) float64 {
	if f.Manual {
		return f.SetTarget
	}
	if f.Actual > 0 {
		return f.Actual
	}
	if f.HasMin {
		return f.Min
	}
	return 1500
}

func (u *UI) adjust(ratio float64) {
	idx := u.targets()
	if len(idx) == 0 {
		return
	}
	if !u.needWrite("change fan speed") {
		return
	}

	applied := 0
	var firstErr error
	var set []int

	for _, f := range idx {
		if !f.HasTarget {
			if firstErr == nil {
				firstErr = smcError(-4)
			}
			continue
		}
		span := 0.0
		if f.HasMin && f.HasMax {
			span = f.Max - f.Min
		}
		step := span * ratio
		if span <= 0 {
			step = 1000 * ratio
		}
		if step > -20 && step < 20 {
			if ratio > 0 {
				step = 20
			} else {
				step = -20
			}
		}
		next := clampRPM(f, baseRPM(f)+step)
		if err := f.SetManual(u.smc, next); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		applied++
		set = append(set, int(f.SetTarget))
	}

	if applied > 0 {
		u.setStatus(statusOK, "Set %d %s to manual: %s RPM", applied, plural(applied, "fan", "fans"), joinRPM(set))
		if firstErr != nil {
			u.setStatus(statusErr, "Some fans could not be changed: %v", firstErr)
		}
	} else {
		u.setStatus(statusErr, "Cannot change fan speed: %v", firstErr)
	}
	refreshFans(u.smc, u.fans)
}

func joinRPM(vals []int) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, fmt.Sprintf("%d", v))
	}
	return strings.Join(parts, " / ")
}

func (u *UI) manual() {
	idx := u.targets()
	if len(idx) == 0 {
		return
	}
	if !u.needWrite("switch to manual control") {
		return
	}

	applied := 0
	var firstErr error
	var set []int
	for _, f := range idx {
		rpm := clampRPM(f, baseRPM(f))
		if err := f.SetManual(u.smc, rpm); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		applied++
		set = append(set, int(f.SetTarget))
	}
	if applied > 0 {
		u.setStatus(statusOK, "%d %s switched to manual: %s RPM", applied, plural(applied, "fan", "fans"), joinRPM(set))
	} else {
		u.setStatus(statusErr, "Cannot switch to manual: %v", firstErr)
	}
	refreshFans(u.smc, u.fans)
}

func (u *UI) auto() {
	idx := u.targets()
	if len(idx) == 0 {
		return
	}
	if !u.needWrite("restore automatic control") {
		return
	}

	applied := 0
	var firstErr error
	for _, f := range idx {
		if err := f.SetAuto(u.smc); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		applied++
	}
	if applied > 0 {
		u.setStatus(statusOK, "%d %s restored to automatic control", applied, plural(applied, "fan", "fans"))
	} else {
		u.setStatus(statusErr, "Cannot restore automatic control: %v", firstErr)
	}
	refreshFans(u.smc, u.fans)
}

func (u *UI) allAuto() {
	if !u.needWrite("restore automatic control") {
		return
	}
	if err := restoreAll(u.smc, u.fans); err != nil {
		u.setStatus(statusErr, "Restore failed: %v", err)
	} else {
		u.setStatus(statusOK, "All fans restored to automatic control")
	}
	refreshFans(u.smc, u.fans)
}

func (u *UI) toggle() {
	if len(u.fans) == 0 {
		return
	}
	u.checked[u.sel] = !u.checked[u.sel]
	if u.sel+1 < len(u.fans) {
		u.sel++
	}
	n := u.checkedCount()
	if n > 0 {
		u.setStatus(statusInfo, "%d %s selected — +/− M A act on them", n, plural(n, "fan", "fans"))
	} else {
		u.setStatus(statusInfo, "Selection cleared — actions use the fan under the cursor")
	}
}

func (u *UI) move(delta int) {
	if len(u.fans) == 0 {
		return
	}
	u.sel += delta
	if u.sel < 0 {
		u.sel = 0
	}
	if u.sel >= len(u.fans) {
		u.sel = len(u.fans) - 1
	}
}

/* ------------------------------------------------------------------ */
/* rendering                                                          */
/* ------------------------------------------------------------------ */

func (u *UI) headerLine(cols int) string {
	left := " " + cBrand + cBold + "▌ mac-fan" + cReset +
		" " + cDim + version + cReset + "   " +
		cText + u.model + cReset + cDim + " · " + cReset + cText + u.chip + cReset
	badge := cWarn + "[READ-ONLY]" + cReset
	if u.writable {
		badge = cOK + "[ROOT]" + cReset
	}
	gap := cols - 1 - visWidth(left) - visWidth(badge)
	if gap < 1 {
		return truncate(left+"  "+badge, cols-1)
	}
	return left + strings.Repeat(" ", gap) + badge
}

func (u *UI) sectionTitle(title, sub string, cols int) string {
	s := " " + cBold + title + cReset
	if sub != "" {
		s += "  " + cDim + sub + cReset
	}
	return truncate(s, cols-1)
}

func (u *UI) fanSummary() string {
	n := len(u.fans)
	if n == 0 {
		return "no fans detected"
	}
	sel := u.checkedCount()
	if sel > 0 {
		return fmt.Sprintf("%d %s · %d selected", n, plural(n, "fan", "fans"), sel)
	}
	return fmt.Sprintf("%d %s", n, plural(n, "fan", "fans"))
}

func (u *UI) fanBar(f *Fan, width int) string {
	ratio := 0.0
	if f.HasMin && f.HasMax && f.Max > f.Min {
		ratio = (f.Actual - f.Min) / (f.Max - f.Min)
	} else if f.Max > 0 {
		ratio = f.Actual / f.Max
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	col := cOK
	if ratio >= 0.85 {
		col = cErr
	} else if ratio >= 0.60 {
		col = cWarn
	}
	var b strings.Builder
	b.WriteString(barOpen)
	b.WriteString(col)
	for i := 0; i < filled; i++ {
		b.WriteString("█")
	}
	b.WriteString(barMid)
	for i := filled; i < width; i++ {
		b.WriteString("░")
	}
	b.WriteString(barClose)
	return b.String()
}

func (u *UI) fanRow(i int, f *Fan, cols int) string {
	var b strings.Builder
	if i == u.sel {
		b.WriteString(selMark)
	} else {
		b.WriteString("   ")
	}
	if u.checked[i] {
		b.WriteString(checkedMark)
	} else {
		b.WriteString(emptyMark)
	}

	nameStyle := cText + cBold
	if i == u.sel {
		nameStyle = cAccent + cBold
	}
	label := fmt.Sprintf("Fan %d", f.Index)
	b.WriteString(nameStyle)
	b.WriteString(label)
	b.WriteString(cReset)
	b.WriteString(strings.Repeat(" ", 7-len(label)))

	if !f.Available {
		b.WriteString(noKeysMsg)
		return b.String()
	}

	b.WriteString("  ")
	if cols >= 82 {
		b.WriteString(u.fanBar(f, 12))
		b.WriteString("  ")
	}
	b.WriteString(cText)
	b.WriteString(padLeft(fmt.Sprintf("%.0f", f.Actual), 4))
	b.WriteString(" RPM")
	b.WriteString(cReset)

	if cols >= 78 {
		b.WriteString(targetLabel)
		b.WriteString(cText)
		b.WriteString(padLeft(fmt.Sprintf("%.0f", f.Target), 4))
		b.WriteString(cReset)
	}
	if cols >= 68 && f.HasMin && f.HasMax {
		fmt.Fprintf(&b, "%s   %.0f-%.0f%s", cDim, f.Min, f.Max, cReset)
	}
	b.WriteString("   ")
	b.WriteString(f.ModeColor())
	b.WriteString(cBold)
	b.WriteString(f.ModeLabel())
	b.WriteString(cReset)
	return b.String()
}

type groupView struct {
	g       Group
	sensors []*Sensor
}

func (u *UI) groupViews() []groupView {
	views := make([]groupView, 0, groupCount)
	total := 0
	for g := Group(0); g < groupCount; g++ {
		list := groupSensors(u.sensors, g)
		if len(list) == 0 {
			continue
		}
		views = append(views, groupView{g: g, sensors: list})
		total += len(list)
	}
	return views
}

func (u *UI) groupHeader(g Group) string {
	return " " + cBrand + cBold + "─ " + g.Title() + cReset
}

func heatRamp() string {
	var b strings.Builder
	for _, c := range []int{17, 24, 30, 35, 42, 214, 208, 196} {
		b.WriteString(fmt.Sprintf("\x1b[48;5;%dm  ", c))
	}
	b.WriteString(cReset)
	return b.String()
}

func sensorRow(s *Sensor, width int) string {
	const nameW = 9
	const valW = 7
	sparkW := width - (1 + nameW + 1 + valW + 1)
	if sparkW < 0 {
		sparkW = 0
	}
	if sparkW > sparkMax {
		sparkW = sparkMax
	}

	var b strings.Builder
	b.WriteString(" ")
	b.WriteString(cText)
	b.WriteString(padRight(s.Name, nameW))
	b.WriteString(cReset)
	b.WriteString(" ")
	if s.Valid && s.Value > 0 {
		txt := padLeft(fmt.Sprintf("%.1f°C", s.Value), valW)
		b.WriteString(cBold)
		b.WriteString(heatSGR(s.Value))
		b.WriteString(txt)
		b.WriteString(cReset)
	} else {
		b.WriteString(cDim)
		b.WriteString(padLeft("—", valW))
		b.WriteString(cReset)
	}
	if sparkW > 0 {
		b.WriteString(" ")
		b.WriteString(sparkline(s, sparkW))
	}
	return b.String()
}

func (u *UI) renderViews(views []groupView, width int, headers bool) []string {
	lines := make([]string, 0, len(views)+len(u.sensors))
	for _, v := range views {
		if headers {
			lines = append(lines, u.groupHeader(v.g))
		}
		for _, s := range v.sensors {
			lines = append(lines, sensorRow(s, width))
		}
	}
	return lines
}

type sensorLayout struct {
	lines []string
	shown int
	total int
}

func (u *UI) layoutSensors(cols, avail int) sensorLayout {
	views := u.groupViews()
	total := 0
	for _, v := range views {
		total += len(v.sensors)
	}
	if total == 0 || avail <= 0 {
		return sensorLayout{
			lines: []string{" " + cDim + "No readable thermal sensors" + cReset},
			shown: 0, total: 0,
		}
	}

	// Wide terminals get two real panels: a machine schematic on the left and
	// the sensor list (name + value + curve) on the right. The compact
	// schematic needs 17 rows; below that the panels are not split.
	if cols >= 96 && avail >= 17 {
		rightW := cols - chassisW - 2
		linesR, shown := u.renderColumn(views, rightW, avail)
		if chassis := u.chassisLines(chassisW, avail, len(linesR)); chassis != nil {
			height := len(chassis)
			if len(linesR) > height {
				height = len(linesR)
			}
			if height > avail {
				height = avail
			}
			return sensorLayout{
				lines: mergePanels(chassis, linesR, chassisW, height),
				shown: shown, total: total,
			}
		}
	}

	lines, shown := u.renderColumn(views, cols-1, avail)
	return sensorLayout{lines: lines, shown: shown, total: total}
}

// mergePanels joins the schematic and the sensor column side by side, padding
// whichever side is shorter so every row has the same shape.
func mergePanels(left, right []string, leftW, height int) []string {
	out := make([]string, 0, height)
	blank := strings.Repeat(" ", leftW)
	for i := 0; i < height; i++ {
		l := blank
		if i < len(left) {
			l = padRight(left[i], leftW)
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out = append(out, l+"  "+r)
	}
	return out
}

// renderColumn lays out one sensor column. When the sensors do not fit, the
// column becomes a scrolling window over the full list instead of dropping
// entries, so nothing is silently hidden from the user.
func (u *UI) renderColumn(views []groupView, width, avail int) ([]string, int) {
	full := u.renderViews(views, width, true)
	headers := make(map[string]struct{}, len(views))
	total := 0
	for _, v := range views {
		total += len(v.sensors)
		headers[u.groupHeader(v.g)] = struct{}{}
	}
	if len(full) <= avail {
		u.scroll = 0
		u.maxScroll = 0
		return full, total
	}
	u.maxScroll = len(full) - avail
	if u.scroll > u.maxScroll {
		u.scroll = u.maxScroll
	}
	if u.scroll < 0 {
		u.scroll = 0
	}
	window := full[u.scroll : u.scroll+avail]
	shown := 0
	for _, l := range window {
		if _, isHeader := headers[l]; !isHeader {
			shown++
		}
	}
	return window, shown
}

// scrollSensors moves the sensor window by delta rows, clamped to the range
// computed during the last render.
func (u *UI) scrollSensors(delta int) {
	u.scroll += delta
	if u.scroll < 0 {
		u.scroll = 0
	}
	if u.scroll > u.maxScroll {
		u.scroll = u.maxScroll
	}
}

func (u *UI) sensorSummary(shown, total int, cols int) string {
	sub := fmt.Sprintf("%d %s", shown, plural(shown, "sensor", "sensors"))
	if peak := peakTemp(u.sensors); peak > 0 {
		sub = fmt.Sprintf("peak %s · %s", degreeText(peak), sub)
	}
	if shown < total {
		sub = fmt.Sprintf("peak %s · %d of %d sensors", degreeText(peakTemp(u.sensors)), shown, total)
	}
	if u.maxScroll > 0 {
		sub += " · scroll ↕"
	}
	if cols >= 118 {
		sub = "25° " + heatRamp() + " 95°  ·  value = °C  ·  curve = last 30 s  ·  " + sub
	} else if cols >= 92 {
		sub += " · curve = last 30 s"
	}
	return sub
}

// controlsLine renders the bottom control bar as shaded buttons and returns
// their clickable cell ranges. A negative sel in the range means a key action.
func (u *UI) controlsLine(cols int) (string, []uiButton) {
	type ctl struct {
		glyph, label string
		action       key
	}
	all := []ctl{
		{"−", "", keyMinus}, {"+", "", keyPlus},
		{"M", "manual", keyManual}, {"A", "auto", keyAuto},
		{"R", "all auto", keyAllAuto}, {"Q", "quit", keyQuit},
	}

	labels := cols >= 96

	var b strings.Builder
	var regions []uiButton
	x := 1
	write := func(s string) {
		b.WriteString(s)
		x += visWidth(s)
	}
	for i, c := range all {
		if i > 0 {
			write("  ")
		}
		start := x
		write(" \x1b[48;5;237m" + cBold + cAccent + " " + c.glyph + " " + cReset)
		if labels && c.label != "" {
			write(" " + cDim + c.label + cReset)
		}
		regions = append(regions, uiButton{x0: start, x1: x - 1, action: c.action, sel: -1})
	}
	return truncate(b.String(), cols-1), regions
}

func (u *UI) statusLine(cols int) string {
	text := ""
	col := cDim

	switch {
	case u.status != "" && time.Now().Before(u.expires):
		text = u.status
		if u.kind == statusOK {
			col = cOK
		} else if u.kind == statusErr {
			col = cErr
		} else {
			col = cAccent
		}
	case !u.writable:
		text = "Read-only mode — run with sudo mac-fan to change fan speed"
		col = cWarn
	case u.checkedCount() > 0:
		text = fmt.Sprintf("%d %s selected · system control is restored on exit", u.checkedCount(), plural(u.checkedCount(), "fan", "fans"))
		col = cPurple
	default:
		text = "Space selects multiple fans · system control is restored on exit"
	}
	return truncate(" "+col+text+cReset, cols-1)
}

func (u *UI) render() {
	rows, cols := termSize()
	if rows < 8 {
		rows = 8
	}

	lines := make([]string, 0, rows+4)
	lines = append(lines, u.headerLine(cols))
	lines = append(lines, u.sectionTitle("FANS", u.fanSummary(), cols))
	var fanButtons []uiButton
	if len(u.fans) == 0 {
		lines = append(lines, "   "+cWarn+"No fans detected on this Mac"+cReset)
	} else {
		for i, f := range u.fans {
			lines = append(lines, u.fanRow(i, f, cols))
			fanButtons = append(fanButtons, uiButton{y: len(lines), x0: 1, x1: cols, sel: i})
		}
	}

	// One blank row keeps the fan block and the sensor grid from crowding;
	// in short terminals that row buys more useful space instead.
	if cols >= 96 && rows-len(lines)-3 >= 18 {
		lines = append(lines, "")
	}

	// Rows still needed below this point: SENSORS title, control bar, status.
	gridAvail := rows - len(lines) - 3
	if gridAvail < 1 {
		gridAvail = 1
	}
	layout := u.layoutSensors(cols, gridAvail)

	lines = append(lines, u.sectionTitle("SENSORS", u.sensorSummary(layout.shown, layout.total, cols), cols))
	lines = append(lines, layout.lines...)

	for len(lines) < rows-2 {
		lines = append(lines, "")
	}
	bar, regions := u.controlsLine(cols)
	for i := range regions {
		regions[i].y = len(lines) + 1
	}
	u.buttons = append(fanButtons, regions...)
	lines = append(lines, bar, u.statusLine(cols))
	if len(lines) > rows {
		lines = lines[:rows]
	}

	var b strings.Builder
	b.WriteString("\x1b[H")
	for i := 0; i < rows; i++ {
		s := ""
		if i < len(lines) {
			s = truncate(lines[i], cols-1)
		}
		b.WriteString(s)
		b.WriteString(lineEnd)
		if i < rows-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\x1b[J")
	_, _ = os.Stdout.WriteString(b.String())
}
