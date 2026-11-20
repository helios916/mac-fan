---
name: mac-fan
description: Operate the mac-fan CLI and TUI for Apple Silicon fan control and SMC temperature reads. Use when asked to list fans, set a fan RPM or percent of max, restore automatic fan control, or build and troubleshoot mac-fan.
---

# mac-fan

Single Go binary in this repository; talks to AppleSMC through cgo/IOKit.

Interface shape: **subcommands for actions, flags for options**.
Writing fan settings needs root; reading does not.

## 1. Discover fans (no root)

```sh
mac-fan list
mac-fan list --json
```

```text
mac-fan 1.0: 2 fans · MacBookPro18,1 · Apple M1 Pro
  #0     0 RPM   target    0   range 1499-4296 RPM   AUTO
  #1     0 RPM   target    0   range 1499-4744 RPM   AUTO
```

The `#N` column is the fan identifier used by `set` and `auto`. JSON exposes it
as `fans[].index` together with `rpm`, `min`, `max`, `mode`, `available`.

## 2. Resolve and inspect a target (no root)

`--dry-run` resolves the fan, applies clamping and prints the result without
writing SMC:

```sh
mac-fan set 0 3000 --dry-run
mac-fan set 1 40% --dry-run
mac-fan set all 80% --dry-run
```

```text
dry run — no SMC writes
fan 0: 3000 RPM (69.8% of max, range 1499-4296)
```

Argument rules:

- fan selector: index from `mac-fan list`, or `all`; unknown index exits 1.
- value: integer/decimal RPM (`3000`) or a percent with `%` (`40%`) computed
  from that fan's max from `list`; clamped to `min`-`max`.
- invalid value or missing arguments exit 2.

## 3. Apply (root)

```sh
sudo mac-fan set 0 3000 --hold 60
sudo mac-fan set 0 40% --hold 60
sudo mac-fan set all 80% --hold 60
```

Manual control stays in the foreground and re-asserts the target every second
because `thermalmonitord` reverts it. `Ctrl-C`, `SIGTERM`, or `--hold N`
ends the run and restores automatic mode for the changed fans.

JSON output is NDJSON: first the applied fans, then `{"restored":N}`.

## 4. Restore automatic control

```sh
sudo mac-fan auto all
sudo mac-fan auto 0
mac-fan auto all --dry-run     # no root, shows which fans would change
```

Use `auto all` if a previous `set` was killed with SIGKILL.

## 5. Snapshot and JSON

```sh
mac-fan status
mac-fan status --json
```

`status --json` returns `version`, `model`, `chip`, `read_only`, `fans[]`, and
`sensors[]` (`key`, `name`, `group`, `temp_c`).

## 6. Recommended agent flow

1. `mac-fan list --json` → pick `fans[].index`, require `available: true`.
2. `mac-fan set <index> <target> --dry-run --json` → confirm resolved RPM.
3. `sudo mac-fan set <index> <target> --hold 60` → verify `fan N: ... RPM`.
4. Confirm the final `restored N fan(s) to automatic control` line.
5. If a run may have been force-killed, finish with `sudo mac-fan auto all`.

Never run an unbounded `set` unattended; always bound `--hold` or follow up
with `auto all`.

## 7. Build and verify

```sh
go vet ./... && make
./mac-fan version
./mac-fan list --json
./mac-fan set 0 40% --dry-run
```

TUI checks (interactive): at 120×34 both panels end on the same row; at 96×24
the fan row collapses to one line; below 96 columns only the sensor list shows.

## 8. Guardrails

- Never leave a fan manual without restoring it.
- Never write from `status`, `list`, or `--dry-run` paths.
- Do not invent fan indices; only use indices reported by `list`.
- Keep the schematic truthful per series (two rear-corner blowers on 14"/16″
  Pro, one rear-left on 13″ Pro, none on Air).
- Missing sensors are expected; report `N of 31` instead of fabricating rows.

## 9. Code map

- `cli.go` — subcommands, option parsing (`parseInterspersed`), dry-run, hold loop, JSON.
- `main.go` — entry point, TUI loop, snapshot output, elevation prompt.
- `fan.go` — SMC fan keys, manual targets, re-assert, restore.
- `sensors.go` — sensor table, sampling, heat colours, sparklines.
- `chassis.go` — per-series top-down schematic and rotor animation.
- `ui.go` — panels, selection, status line.
