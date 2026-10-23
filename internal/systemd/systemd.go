// Package systemd reads unit state through `systemctl show`, which any user
// may run: it is a read-only D-Bus query.
package systemd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Properties is the list passed to `systemctl show -p`.
var Properties = []string{
	"Id", "Description", "LoadState", "ActiveState", "SubState", "Result", "UnitFileState",
	"MainPID", "NRestarts", "MemoryCurrent", "MemoryPeak", "MemoryMax", "CPUUsageNSec",
	"TasksCurrent", "ActiveEnterTimestamp", "LastTriggerUSec", "NextElapseUSecRealtime",
	"EnvironmentFiles",
}

// Unit is one parsed block, plus the raw CPU counter used for rates.
type Unit struct {
	state.Unit
	CPUNanos uint64
	cpuKnown bool
}

// Parse reads `systemctl show` output: blocks of key=value, one per unit,
// separated by blank lines, keys in systemd's own order.
func Parse(r io.Reader) ([]Unit, error) {
	var units []Unit
	cur := map[string][]string{}
	flush := func() {
		if len(cur) > 0 {
			units = append(units, fromProps(cur))
			cur = map[string][]string{}
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		cur[k] = append(cur[k], v)
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("systemd: parse: %w", err)
	}
	return units, nil
}

func fromProps(p map[string][]string) Unit {
	get := func(k string) string {
		if v := p[k]; len(v) > 0 {
			return v[len(v)-1]
		}
		return ""
	}
	u := Unit{Unit: state.Unit{
		Name:        get("Id"),
		Description: get("Description"),
		Load:        get("LoadState"),
		Active:      get("ActiveState"),
		Sub:         get("SubState"),
		Result:      get("Result"),
		Enabled:     get("UnitFileState"),
		MainPID:     atoi(get("MainPID")),
		Restarts:    atoi(get("NRestarts")),
		MemoryBytes: atou(get("MemoryCurrent")),
		MemoryPeak:  atou(get("MemoryPeak")),
		MemoryMax:   atou(get("MemoryMax")),
		Tasks:       atoi(get("TasksCurrent")),
		ActiveSince: parseTime(get("ActiveEnterTimestamp")),
		LastTrigger: parseTime(get("LastTriggerUSec")),
		NextElapse:  parseTime(get("NextElapseUSecRealtime")),
	}}
	if v := get("CPUUsageNSec"); v != "" && v != "[not set]" {
		u.CPUNanos = atou(v)
		u.cpuKnown = true
	}
	for _, line := range p["EnvironmentFiles"] {
		// "/opt/app/.env (ignore_errors=no)"; several files may share one line.
		for _, f := range strings.Fields(line) {
			if strings.HasPrefix(f, "/") {
				u.EnvFiles = append(u.EnvFiles, f)
			}
		}
	}
	return u
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// atou treats "[not set]" and "infinity" as 0, which the UI shows as "no limit".
func atou(s string) uint64 {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseTime accepts "@unix" (from --timestamp=unix) and the human format that
// timer properties keep using regardless of that flag.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "n/a" {
		return time.Time{}
	}
	if strings.HasPrefix(s, "@") {
		if n, err := strconv.ParseInt(s[1:], 10, 64); err == nil {
			return time.Unix(n, 0).UTC()
		}
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05.000000 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// Poller runs systemctl and turns CPU counters into percentages.
type Poller struct {
	Systemctl string
	run       func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu   sync.Mutex
	prev map[string]sample
}

type sample struct {
	nanos uint64
	at    time.Time
}

// NewPoller returns a poller that execs systemctl.
func NewPoller(systemctl string) *Poller {
	return &Poller{Systemctl: systemctl, run: execOutput, prev: map[string]sample{}}
}

func execOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Poll returns the state of the named units, keyed by unit name.
func (p *Poller) Poll(ctx context.Context, units []string) (map[string]state.Unit, error) {
	if len(units) == 0 {
		return map[string]state.Unit{}, nil
	}
	args := []string{"show", "--timestamp=unix", "-p", strings.Join(Properties, ","), "--"}
	args = append(args, units...)
	out, err := p.run(ctx, p.Systemctl, args...)
	if err != nil {
		return nil, fmt.Errorf("systemd: %w", err)
	}
	parsed, err := Parse(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	return p.rates(parsed, time.Now()), nil
}

func (p *Poller) rates(parsed []Unit, now time.Time) map[string]state.Unit {
	p.mu.Lock()
	defer p.mu.Unlock()
	res := make(map[string]state.Unit, len(parsed))
	for _, u := range parsed {
		if u.cpuKnown {
			if prev, ok := p.prev[u.Name]; ok && u.CPUNanos >= prev.nanos {
				if dt := now.Sub(prev.at); dt > 0 {
					u.CPUPercent = float64(u.CPUNanos-prev.nanos) / float64(dt.Nanoseconds()) * 100
				}
			}
			p.prev[u.Name] = sample{u.CPUNanos, now}
		}
		res[u.Name] = u.Unit
	}
	return res
}

// Canonical adds ".service" to bare unit names, as systemctl reports them.
func Canonical(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return name + ".service"
}
