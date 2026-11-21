package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type fanInfo struct {
	Index     int     `json:"index"`
	Available bool    `json:"available"`
	RPM       float64 `json:"rpm"`
	Target    float64 `json:"target"`
	Min       float64 `json:"min,omitempty"`
	Max       float64 `json:"max,omitempty"`
	Mode      string  `json:"mode"`
}

type sensorInfo struct {
	Key   string  `json:"key"`
	Name  string  `json:"name"`
	Group string  `json:"group"`
	Temp  float64 `json:"temp_c"`
}

type setResult struct {
	Index int     `json:"index"`
	RPM   float64 `json:"rpm"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Pct   float64 `json:"percent_of_max,omitempty"`
}

func run() int {
	if len(os.Args) < 2 {
		return runTUIEntry(false)
	}

	switch os.Args[1] {
	case "help", "-h", "--help":
		if len(os.Args) == 2 {
			printHelp()
			return 0
		}
		return printCommandHelp(os.Args[2])
	case "version", "-V", "--version":
		fmt.Printf("mac-fan %s\n", version)
		return 0
	case "list":
		return cmdList(os.Args[2:])
	case "status":
		return cmdStatus(os.Args[2:])
	case "set":
		return cmdSet(os.Args[2:])
	case "auto":
		return cmdAuto(os.Args[2:])
	case "tui":
		return cmdTUI(os.Args[2:])
	case "--read-only":
		if len(os.Args) > 2 {
			fmt.Fprintln(os.Stderr, "mac-fan: --read-only is only valid for the TUI")
			return 2
		}
		return runTUIEntry(true)
	}

	fmt.Fprintf(os.Stderr, "mac-fan: unknown command %q\nRun 'mac-fan help' for usage.\n", os.Args[1])
	return 2
}

func runTUIEntry(readOnly bool) int {
	model, chip := hwInfo()
	writable := os.Geteuid() == 0 && !readOnly

	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		return runOnce(model, chip, writable, false)
	}
	if !writable && !readOnly {
		maybeElevate()
		writable = os.Geteuid() == 0 && !readOnly
	}
	return runTUI(model, chip, writable)
}

/* ------------------------------------------------------------------ */
/* flag plumbing                                                      */
/* ------------------------------------------------------------------ */

func newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("mac-fan", flag.ContinueOnError)
	// The flag package prints its own errors and default usage; we print a
	// consistent message and the command-specific help instead.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseInterspersed collects positionals that are mixed with options. The
// standard flag package stops at the first non-flag argument, so calling
// fs.Parse again on the remainder lets "set 0 40% --dry-run" and
// "set --dry-run 0 40%" both work without a hand-written preprocessor.
// An explicit "--" terminates option parsing for the rest of the line.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}

	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return pos, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return append(pos, tail...), nil
}

// parseWithHelp parses args and returns the positional arguments plus a status
// code: -1 to continue, 0 when help was shown, or 2 on a bad option.
func parseWithHelp(fs *flag.FlagSet, args []string, cmd string) ([]string, int) {
	pos, err := parseInterspersed(fs, args)
	if err == nil {
		return pos, -1
	}
	if errors.Is(err, flag.ErrHelp) {
		printCommandHelp(cmd)
		return nil, 0
	}
	msg := err.Error()
	msg = strings.Replace(msg, "flag provided but not defined:", "unknown option", 1)
	msg = strings.Replace(msg, "flag needs an argument:", "missing value for", 1)
	fmt.Fprintf(os.Stderr, "mac-fan: %s\nRun 'mac-fan help %s' for usage.\n", msg, cmd)
	return nil, 2
}

func cmdTUI(args []string) int {
	fs := newFlagSet()
	readOnly := fs.Bool("read-only", false, "never write to SMC")
	pos, code := parseWithHelp(fs, args, "tui")
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageError("tui", fmt.Sprintf("unexpected argument %q", pos[0]))
	}
	return runTUIEntry(*readOnly)
}

/* ------------------------------------------------------------------ */
/* errors                                                             */
/* ------------------------------------------------------------------ */

func usageError(cmd, msg string) int {
	fmt.Fprintf(os.Stderr, "mac-fan: %s\nRun 'mac-fan help %s' for usage.\n", msg, cmd)
	return 2
}

func cliError(msg string) int {
	fmt.Fprintf(os.Stderr, "mac-fan: %s\n", msg)
	return 1
}

func notRoot() int {
	fmt.Fprintf(os.Stderr, "mac-fan: changing fan settings requires root\n")
	fmt.Fprintf(os.Stderr, "  sudo %s %s\n", exeName(), strings.Join(os.Args[1:], " "))
	return 1
}

func exeName() string {
	if len(os.Args) > 0 {
		base := os.Args[0]
		if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
			base = base[i+1:]
		}
		if base != "" {
			return base
		}
	}
	return "mac-fan"
}

/* ------------------------------------------------------------------ */
/* shared helpers                                                     */
/* ------------------------------------------------------------------ */

func resolveFans(fans []*Fan, sel string) ([]*Fan, error) {
	if strings.EqualFold(sel, "all") {
		out := make([]*Fan, 0, len(fans))
		for _, f := range fans {
			if f.Available {
				out = append(out, f)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no usable fans detected")
		}
		return out, nil
	}
	idx, err := strconv.Atoi(sel)
	if err != nil {
		return nil, fmt.Errorf("invalid fan selector %q, use an index from 'mac-fan list' or \"all\"", sel)
	}
	for _, f := range fans {
		if f.Index == idx {
			if !f.Available {
				return nil, fmt.Errorf("fan %d has no readable SMC keys", idx)
			}
			return []*Fan{f}, nil
		}
	}
	return nil, fmt.Errorf("fan %d not found, use an index from 'mac-fan list'", idx)
}

func fanMode(f *Fan) string {
	if !f.Available {
		return "UNKNOWN"
	}
	return f.ModeLabel()
}

func fanInfos(fans []*Fan) []fanInfo {
	out := make([]fanInfo, 0, len(fans))
	for _, f := range fans {
		info := fanInfo{
			Index:     f.Index,
			Available: f.Available,
			RPM:       f.Actual,
			Target:    f.Target,
			Mode:      fanMode(f),
		}
		if f.HasMin {
			info.Min = f.Min
		}
		if f.HasMax {
			info.Max = f.Max
		}
		out = append(out, info)
	}
	return out
}

func printFanTable(fans []*Fan) {
	for _, f := range fans {
		if !f.Available {
			fmt.Printf("  #%d  no readable SMC keys\n", f.Index)
			continue
		}
		rangeText := "-"
		if f.HasMin && f.HasMax {
			rangeText = fmt.Sprintf("%.0f-%.0f", f.Min, f.Max)
		}
		fmt.Printf("  #%d  %4.0f RPM   target %4.0f   range %s RPM   %s\n",
			f.Index, f.Actual, f.Target, rangeText, f.ModeLabel())
	}
}

/* ------------------------------------------------------------------ */
/* list                                                               */
/* ------------------------------------------------------------------ */

func cmdList(args []string) int {
	fs := newFlagSet()
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, code := parseWithHelp(fs, args, "list")
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageError("list", fmt.Sprintf("unexpected argument %q", pos[0]))
	}
	model, chip := hwInfo()
	return runListFans(model, chip, *jsonOut)
}

func runListFans(model, chip string, jsonOut bool) int {
	smc, fans, _, err := openHardware()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot connect to SMC: %v\n", err)
		return 1
	}
	defer smc.Close()

	if jsonOut {
		payload := struct {
			Version string    `json:"version"`
			Model   string    `json:"model"`
			Chip    string    `json:"chip"`
			Fans    []fanInfo `json:"fans"`
		}{version, model, chip, fanInfos(fans)}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return cliError(err.Error())
		}
		return 0
	}

	fmt.Printf("mac-fan %s: %d %s · %s · %s\n", version, len(fans), plural(len(fans), "fan", "fans"), model, chip)
	printFanTable(fans)
	return 0
}

/* ------------------------------------------------------------------ */
/* status                                                             */
/* ------------------------------------------------------------------ */

func cmdStatus(args []string) int {
	fs := newFlagSet()
	jsonOut := fs.Bool("json", false, "JSON output")
	pos, code := parseWithHelp(fs, args, "status")
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return usageError("status", fmt.Sprintf("unexpected argument %q", pos[0]))
	}
	model, chip := hwInfo()
	return runOnce(model, chip, false, *jsonOut)
}

/* ------------------------------------------------------------------ */
/* set                                                                */
/* ------------------------------------------------------------------ */

func cmdSet(args []string) int {
	fs := newFlagSet()
	jsonOut := fs.Bool("json", false, "JSON output")
	dryRun := fs.Bool("dry-run", false, "resolve and print the target without writing")
	fs.BoolVar(dryRun, "n", false, "")
	hold := fs.Int("hold", 0, "seconds to hold; 0 holds until interrupted")
	pos, code := parseWithHelp(fs, args, "set")
	if code >= 0 {
		return code
	}
	if len(pos) != 2 {
		return usageError("set", "set needs a fan (index or \"all\") and a target (RPM or percent)")
	}
	if *hold < 0 {
		return usageError("set", "--hold must be zero or a positive number of seconds")
	}

	sel, val := pos[0], pos[1]
	rpm, pct, isPct, err := parseTarget(val)
	if err != nil {
		return usageError("set", err.Error())
	}

	smc, fans, _, err := openHardware()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot connect to SMC: %v\n", err)
		return 1
	}
	defer smc.Close()

	targets, err := resolveFans(fans, sel)
	if err != nil {
		return cliError(err.Error())
	}

	type plan struct {
		fan *Fan
		rpm float64
	}
	plans := make([]plan, 0, len(targets))
	results := make([]setResult, 0, len(targets))
	var firstErr error
	for _, fan := range targets {
		var target float64
		if isPct {
			if !fan.HasMax || fan.Max <= 0 {
				if firstErr == nil {
					firstErr = fmt.Errorf("fan %d reports no maximum RPM, use an RPM value instead", fan.Index)
				}
				continue
			}
			target = fan.Max * pct / 100
		} else {
			// A bare number is an absolute RPM, not a percentage. Reject values
			// below the fan's minimum instead of silently clamping to it.
			if fan.HasMin && rpm < fan.Min {
				hint := ""
				if rpm > 0 && rpm <= 100 {
					hint = fmt.Sprintf(" — did you mean %d%%?", int(rpm))
				}
				return usageError("set", fmt.Sprintf("fan %d: %.0f RPM is below the minimum %.0f%s",
					fan.Index, rpm, fan.Min, hint))
			}
			target = rpm
		}
		target = math.Round(clampRPM(fan, target))

		r := setResult{Index: fan.Index, RPM: target}
		if fan.HasMin {
			r.Min = fan.Min
		}
		if fan.HasMax {
			r.Max = fan.Max
			r.Pct = math.Round(target/fan.Max*1000) / 10
		}
		results = append(results, r)
		plans = append(plans, plan{fan, target})
	}

	if len(plans) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no fan matched %q", sel)
		}
		return cliError(firstErr.Error())
	}

	// The fan index was valid: only now require root for the actual write.
	if !*dryRun && os.Geteuid() != 0 {
		return notRoot()
	}

	applied := make([]*Fan, 0, len(plans))
	if !*dryRun {
		for _, p := range plans {
			if err := p.fan.SetManual(smc, p.rpm); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("fan %d: %v", p.fan.Index, err)
				}
				continue
			}
			applied = append(applied, p.fan)
		}
	}

	if *dryRun {
		if *jsonOut {
			payload := struct {
				DryRun bool        `json:"dry_run"`
				Fans   []setResult `json:"fans"`
			}{true, results}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(payload)
		} else {
			fmt.Println("dry run — no SMC writes")
			describeResults(results)
		}
		if firstErr != nil {
			fmt.Fprintf(os.Stderr, "mac-fan: %v\n", firstErr)
			return 1
		}
		return 0
	}

	if len(applied) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no fan could be set")
		}
		return cliError(firstErr.Error())
	}

	if *jsonOut {
		payload := struct {
			Holding bool        `json:"holding_manual"`
			Hold    int         `json:"hold_seconds"`
			Fans    []setResult `json:"fans"`
		}{true, *hold, results}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(payload)
	} else {
		describeResults(results)
		if *hold > 0 {
			fmt.Printf("holding manual control for %d s — Ctrl-C restores automatic\n", *hold)
		} else {
			fmt.Println("holding manual control — Ctrl-C restores automatic")
		}
	}

	holdManual(smc, fans, *hold)

	restored := 0
	for _, f := range applied {
		if err := f.SetAuto(smc); err == nil {
			restored++
		}
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Restored int `json:"restored"`
		}{restored})
	} else {
		fmt.Printf("restored %d %s to automatic control\n", restored, plural(restored, "fan", "fans"))
	}
	if firstErr != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: %v\n", firstErr)
		return 1
	}
	return 0
}

func describeResults(results []setResult) {
	for _, r := range results {
		if r.Max > 0 {
			fmt.Printf("fan %d: %.0f RPM (%.1f%% of max, range %.0f-%.0f)\n", r.Index, r.RPM, r.Pct, r.Min, r.Max)
		} else {
			fmt.Printf("fan %d: %.0f RPM\n", r.Index, r.RPM)
		}
	}
}

func parseTarget(s string) (rpm, pct float64, isPct bool, err error) {
	if strings.HasSuffix(s, "%") {
		v, e := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if e != nil || v < 0 || v > 100 {
			return 0, 0, true, fmt.Errorf("invalid percent %q, use 0-100%%", s)
		}
		return 0, v, true, nil
	}
	v, e := strconv.ParseFloat(s, 64)
	if e != nil || v < 0 {
		return 0, 0, false, fmt.Errorf("invalid target %q, use RPM or a percent like 40%%", s)
	}
	return v, 0, false, nil
}

func holdManual(smc *SMC, fans []*Fan, secs int) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	var expire <-chan time.Time
	if secs > 0 {
		timer := time.NewTimer(time.Duration(secs) * time.Second)
		defer timer.Stop()
		expire = timer.C
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-stop:
			return
		case <-expire:
			return
		case <-tick.C:
			reassertFans(smc, fans)
		}
	}
}

/* ------------------------------------------------------------------ */
/* auto                                                               */
/* ------------------------------------------------------------------ */

func cmdAuto(args []string) int {
	fs := newFlagSet()
	jsonOut := fs.Bool("json", false, "JSON output")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	fs.BoolVar(dryRun, "n", false, "")
	pos, code := parseWithHelp(fs, args, "auto")
	if code >= 0 {
		return code
	}
	if len(pos) > 1 {
		return usageError("auto", "auto takes at most one fan selector")
	}
	sel := "all"
	if len(pos) == 1 {
		sel = pos[0]
	}

	smc, fans, _, err := openHardware()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot connect to SMC: %v\n", err)
		return 1
	}
	defer smc.Close()

	targets, err := resolveFans(fans, sel)
	if err != nil {
		return cliError(err.Error())
	}

	if !*dryRun && os.Geteuid() != 0 {
		return notRoot()
	}

	if *dryRun {
		idx := make([]int, 0, len(targets))
		for _, fan := range targets {
			idx = append(idx, fan.Index)
		}
		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(struct {
				DryRun   bool  `json:"dry_run"`
				Restored []int `json:"would_restore"`
			}{true, idx})
		} else {
			fmt.Printf("dry run — would restore %d %s to automatic control: %s\n",
				len(idx), plural(len(idx), "fan", "fans"), joinInts(idx))
		}
		return 0
	}

	restored := 0
	var firstErr error
	for _, fan := range targets {
		if err := fan.SetAuto(smc); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("fan %d: %v", fan.Index, err)
			}
			continue
		}
		restored++
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Restored int `json:"restored"`
		}{restored})
	} else {
		fmt.Printf("restored %d %s to automatic control\n", restored, plural(restored, "fan", "fans"))
	}
	if firstErr != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: %v\n", firstErr)
		return 1
	}
	return 0
}

func joinInts(vals []int) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ", ")
}

/* ------------------------------------------------------------------ */
/* help                                                               */
/* ------------------------------------------------------------------ */

func printCommandHelp(cmd string) int {
	switch cmd {
	case "list":
		fmt.Printf(`Usage: mac-fan list [--json]

List fans with their index, current RPM, target, min/max range and mode.
The index (#N, or fans[].index in JSON) is what 'set' and 'auto' accept.

Options:
  --json        machine-readable output

Examples:
  mac-fan list
  mac-fan list --json
`)
	case "status":
		fmt.Printf(`Usage: mac-fan status [--json]

Print one snapshot of every fan and all readable SMC sensors, then exit.

Options:
  --json        machine-readable output

Examples:
  mac-fan status
  mac-fan status --json
`)
	case "set":
		fmt.Printf(`Usage: mac-fan set <fan|all> <rpm|percent%%> [options]

Set a fan target, hold manual control, and restore automatic control on exit.
Writing requires root.

Arguments:
  fan          index from 'mac-fan list' (0, 1, ...) or "all"
  target       absolute RPM such as 3000, or a share of max such as 40%%

Options:
  --hold N     stop holding after N seconds (0 = until interrupted)
  --dry-run,-n resolve and print the clamped target; no writes, no sudo
  --json       JSON output (applied fans, then {"restored":N})

Notes:
  Percent targets are clamped to the fan's min/max range.
  An absolute RPM below the minimum is rejected (80 means 80 RPM, not 80%%).
  thermalmonitord reverts manual mode, so the target is re-asserted every second.

Examples:
  mac-fan set 0 40%% --dry-run
  sudo mac-fan set 0 3000 --hold 60
  sudo mac-fan set all 80%% --hold 30
`)
	case "auto":
		fmt.Printf(`Usage: mac-fan auto [fan|all] [options]

Restore automatic fan control and exit. Writing requires root.

Arguments:
  fan          index from 'mac-fan list' or "all" (default)

Options:
  --dry-run,-n show which fans would be restored, without writing
  --json       JSON output

Examples:
  sudo mac-fan auto all
  sudo mac-fan auto 0
  mac-fan auto --dry-run
`)
	case "tui":
		fmt.Printf(`Usage: mac-fan [--read-only]   or   mac-fan tui [--read-only]

Interactive TUI: live thermal map on the left, 30-second sensor curves on the
right, fan selection and control in the middle. Terminal is restored on exit.

Options:
  --read-only  never write to SMC; useful without root

Keys:
  Up/Down, j/k  move the cursor
  Space         toggle selection and move to the next fan
  + / -         raise or lower the selected fans (manual mode)
  PgUp/PgDn     raise or lower speed by 25%% of the fan range
  M             switch selected fans to manual control
  A             restore selected fans to automatic control
  R             restore all fans to automatic control
  Q, Ctrl-C     quit and restore automatic control
`)
	case "help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "mac-fan: unknown command %q\nRun 'mac-fan help' for usage.\n", cmd)
		return 2
	}
	return 0
}
