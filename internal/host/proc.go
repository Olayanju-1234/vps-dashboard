// Package host reads the machine's CPU, memory, swap and disk use from /proc
// and statfs, keeps a short in-memory history, and reads longer history from
// sysstat's daily files.
package host

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CPUTimes is the aggregate "cpu" line of /proc/stat, in clock ticks.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// Total is every tick counted.
func (c CPUTimes) Total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// Usage returns busy, steal and iowait percentages between two readings.
func Usage(prev, cur CPUTimes) (busy, steal, iowait float64) {
	dt := float64(cur.Total()) - float64(prev.Total())
	if dt <= 0 {
		return 0, 0, 0
	}
	idle := float64(cur.Idle+cur.IOWait) - float64(prev.Idle+prev.IOWait)
	busy = clamp(100 * (dt - idle) / dt)
	steal = clamp(100 * (float64(cur.Steal) - float64(prev.Steal)) / dt)
	iowait = clamp(100 * (float64(cur.IOWait) - float64(prev.IOWait)) / dt)
	return busy, steal, iowait
}

func clamp(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}

// ParseStat reads the first "cpu " line and counts the per-CPU lines.
func ParseStat(r io.Reader) (CPUTimes, int, error) {
	var c CPUTimes
	n := 0
	found := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu" && len(f) >= 9:
			v := make([]uint64, 8)
			for i := range v {
				x, err := strconv.ParseUint(f[i+1], 10, 64)
				if err != nil {
					return c, 0, fmt.Errorf("host: /proc/stat field %d: %w", i+1, err)
				}
				v[i] = x
			}
			c = CPUTimes{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}
			found = true
		case strings.HasPrefix(f[0], "cpu"):
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return c, 0, fmt.Errorf("host: read /proc/stat: %w", err)
	}
	if !found {
		return c, 0, fmt.Errorf("host: no cpu line in /proc/stat")
	}
	return c, n, nil
}

// Mem is the part of /proc/meminfo the dashboard shows, in bytes.
type Mem struct {
	Total, Available, SwapTotal, SwapFree uint64
}

// UsedPercent is memory in use, counting reclaimable cache as free.
func (m Mem) UsedPercent() float64 {
	if m.Total == 0 {
		return 0
	}
	return 100 * float64(m.Total-m.Available) / float64(m.Total)
}

// SwapPercent is swap in use.
func (m Mem) SwapPercent() float64 {
	if m.SwapTotal == 0 {
		return 0
	}
	return 100 * float64(m.SwapTotal-m.SwapFree) / float64(m.SwapTotal)
}

// ParseMeminfo reads /proc/meminfo.
func ParseMeminfo(r io.Reader) (Mem, error) {
	var m Mem
	fields := map[string]*uint64{
		"MemTotal": &m.Total, "MemAvailable": &m.Available,
		"SwapTotal": &m.SwapTotal, "SwapFree": &m.SwapFree,
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		dst, want := fields[k]
		if !want {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return m, fmt.Errorf("host: meminfo %s: %w", k, err)
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		*dst = v
	}
	if err := sc.Err(); err != nil {
		return m, fmt.Errorf("host: read meminfo: %w", err)
	}
	if m.Total == 0 {
		return m, fmt.Errorf("host: meminfo has no MemTotal")
	}
	return m, nil
}

// ParseLoadavg reads the three load averages.
func ParseLoadavg(s string) ([3]float64, error) {
	var l [3]float64
	f := strings.Fields(s)
	if len(f) < 3 {
		return l, fmt.Errorf("host: loadavg %q", s)
	}
	for i := range l {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return l, fmt.Errorf("host: loadavg: %w", err)
		}
		l[i] = v
	}
	return l, nil
}

// ParseUptime reads seconds since boot.
func ParseUptime(s string) (float64, error) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, fmt.Errorf("host: uptime %q", s)
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("host: uptime: %w", err)
	}
	return v, nil
}

// Proc reads files under a /proc root, so tests can point it at fixtures.
type Proc struct{ Root string }

func (p Proc) open(name string) (*os.File, error) {
	f, err := os.Open(filepath.Join(p.Root, name))
	if err != nil {
		return nil, fmt.Errorf("host: %w", err)
	}
	return f, nil
}

func (p Proc) read(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(p.Root, name))
	if err != nil {
		return "", fmt.Errorf("host: %w", err)
	}
	return string(b), nil
}

// CPU reads /proc/stat.
func (p Proc) CPU() (CPUTimes, int, error) {
	f, err := p.open("stat")
	if err != nil {
		return CPUTimes{}, 0, err
	}
	defer f.Close()
	return ParseStat(f)
}

// Mem reads /proc/meminfo.
func (p Proc) Mem() (Mem, error) {
	f, err := p.open("meminfo")
	if err != nil {
		return Mem{}, err
	}
	defer f.Close()
	return ParseMeminfo(f)
}

// Load reads /proc/loadavg.
func (p Proc) Load() ([3]float64, error) {
	s, err := p.read("loadavg")
	if err != nil {
		return [3]float64{}, err
	}
	return ParseLoadavg(s)
}

// Uptime reads /proc/uptime.
func (p Proc) Uptime() (float64, error) {
	s, err := p.read("uptime")
	if err != nil {
		return 0, err
	}
	return ParseUptime(s)
}

// Kernel reads the running kernel release.
func (p Proc) Kernel() string {
	s, err := p.read("sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}
