package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "1.0.0"

func main() {
	os.Exit(run())
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func hwInfo() (model, chip string) {
	if v, err := syscall.Sysctl("hw.model"); err == nil {
		model = strings.TrimSpace(v)
	}
	if model == "" {
		model = "Mac"
	}
	if v, err := syscall.Sysctl("machdep.cpu.brand_string"); err == nil {
		chip = strings.TrimSpace(v)
	}
	if chip == "" {
		chip = "Apple Silicon"
	}
	return model, chip
}

func openHardware() (*SMC, []*Fan, []*Sensor, error) {
	smc, err := OpenSMC()
	if err != nil {
		return nil, nil, nil, err
	}
	fans := scanFans(smc)
	refreshFans(smc, fans)
	sensors := scanSensors(smc)
	if len(sensors) > 0 {
		sampleSensors(smc, sensors)
	}
	return smc, fans, sensors, nil
}

func runOnce(model, chip string, writable, jsonOut bool) int {
	smc, fans, sensors, err := openHardware()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot connect to SMC: %v\n", err)
		return 1
	}
	defer smc.Close()

	if jsonOut {
		type oncePayload struct {
			Version  string       `json:"version"`
			Model    string       `json:"model"`
			Chip     string       `json:"chip"`
			ReadOnly bool         `json:"read_only"`
			Fans     []fanInfo    `json:"fans"`
			Sensors  []sensorInfo `json:"sensors"`
		}
		sensorsOut := make([]sensorInfo, 0, len(sensors))
		for _, s := range sensors {
			sensorsOut = append(sensorsOut, sensorInfo{s.Key, s.Name, s.Group.Title(), s.Value})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(oncePayload{version, model, chip, !writable, fanInfos(fans), sensorsOut}); err != nil {
			return cliError(err.Error())
		}
		return 0
	}

	badge := ""
	if !writable {
		badge = "  [read-only]"
	}
	fmt.Printf("mac-fan %s: %d %s · %s · %s%s\n", version, len(fans), plural(len(fans), "fan", "fans"), model, chip, badge)
	printFanTable(fans)

	var parts []string
	for g := Group(0); g < groupCount; g++ {
		peak := groupPeak(sensors, g)
		if peak > 0 {
			parts = append(parts, fmt.Sprintf("%s %.1f°C", g.Title(), peak))
		}
	}
	if len(parts) > 0 {
		fmt.Printf("  temps  %s\n", strings.Join(parts, " · "))
	}
	if !writable {
		fmt.Println("  hint: run with sudo mac-fan to change fan speed")
	}
	return 0
}

func maybeElevate() {
	fmt.Print("\n mac-fan needs root privileges to change fan speed.\n Relaunch with sudo? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return
	}
	line = strings.TrimSpace(line)
	if len(line) == 0 || line[0] != 'y' && line[0] != 'Y' {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot locate executable: %v\n", err)
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: sudo not found: %v\n", err)
		return
	}

	args := []string{"sudo", exe}
	args = append(args, os.Args[1:]...)
	if err := syscall.Exec(sudo, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot start sudo: %v\n", err)
	}
}

func runTUI(model, chip string, writable bool) int {
	smc, fans, sensors, err := openHardware()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot connect to SMC: %v\n", err)
		return 1
	}
	defer smc.Close()

	old, err := makeRaw(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mac-fan: cannot set raw terminal mode: %v\n", err)
		return 1
	}
	defer func() {
		restoreTermios(0, old)
		fmt.Print(cReset + "\x1b[?25h\x1b[?1049l")
	}()

	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H")

	ui := newUI(smc, fans, sensors, model, chip, writable)
	if !writable {
		ui.setStatus(statusInfo, "Read-only mode — run with sudo mac-fan to change fan speed")
	}

	keys := make(chan key, 32)
	go inputLoop(keys)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sig)

	ui.render()
	// Redraw quickly while a fan is turning so the rotor animation is smooth;
	// otherwise fall back to a calm twice-per-second refresh.
	ticker := time.NewTicker(125 * time.Millisecond)
	defer ticker.Stop()
	lastSample := time.Now()
	lastFans := time.Now()
	lastDraw := time.Now()

	for {
		select {
		case <-sig:
			quitTUI(ui, writable)
			return 0
		case k, ok := <-keys:
			if !ok {
				quitTUI(ui, writable)
				return 0
			}
			if handleKey(ui, k) {
				quitTUI(ui, writable)
				return 0
			}
			ui.render()
			lastDraw = time.Now()
		case <-ticker.C:
			// Fan state is polled at 500 ms: the rotor animation runs off the
			// wall clock, so re-reading SMC every 125 ms frame only adds IOKit
			// calls. Sensor sampling stays at 1 Hz.
			if time.Since(lastFans) >= 500*time.Millisecond {
				refreshFans(smc, fans)
				if writable {
					reassertFans(smc, fans)
				}
				lastFans = time.Now()
			}
			sampled := false
			if time.Since(lastSample) >= time.Second {
				sampleSensors(smc, sensors)
				lastSample = time.Now()
				sampled = true
			}
			if sampled || anyFanSpinning(fans) || time.Since(lastDraw) >= 500*time.Millisecond {
				ui.render()
				lastDraw = time.Now()
			}
		}
	}
}

func handleKey(ui *UI, k key) bool {
	switch k {
	case keyUp:
		ui.move(-1)
	case keyDown:
		ui.move(1)
	case keyPgUp:
		ui.adjust(0.25)
	case keyPgDn:
		ui.adjust(-0.25)
	case keyPlus:
		ui.adjust(0.10)
	case keyMinus:
		ui.adjust(-0.10)
	case keyManual:
		ui.manual()
	case keyAuto:
		ui.auto()
	case keyAllAuto:
		ui.allAuto()
	case keyToggle:
		ui.toggle()
	case keyQuit:
		return true
	}
	return false
}

func quitTUI(ui *UI, writable bool) {
	if writable {
		_ = restoreAll(ui.smc, ui.fans)
	}
	refreshFans(ui.smc, ui.fans)
	ui.smc.Close()
}

func printHelp() {
	fmt.Printf(`mac-fan %s — fan control and thermal monitor for Apple Silicon Macs

Usage:
  mac-fan [command]

Commands:
  (none) | tui         interactive TUI (writing fans needs sudo)
  list [--json]        list fans: index, RPM, target, range, mode
  status [--json]      one snapshot of fans and all sensors
  set <fan> <target>   set and hold a fan speed                 [sudo]
  auto [fan]           restore automatic control                [sudo]
  help [command]       show this help, or help for one command
  version              print the version

Options:
  --read-only          TUI only: never write to SMC
  -h, --help           same as 'mac-fan help'

Quick start:
  mac-fan list
  mac-fan set 0 40%% --dry-run
  sudo mac-fan set 0 40%% --hold 60

Run 'mac-fan help <command>' for arguments, options and examples.
`, version)
}
