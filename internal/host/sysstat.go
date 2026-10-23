package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

type sadfDoc struct {
	Sysstat struct {
		Hosts []struct {
			Statistics []struct {
				Timestamp struct {
					Date string `json:"date"`
					Time string `json:"time"`
					UTC  int    `json:"utc"`
				} `json:"timestamp"`
				CPULoad []struct {
					CPU  string  `json:"cpu"`
					Idle float64 `json:"idle"`
				} `json:"cpu-load"`
				Memory *struct {
					MemUsedPercent float64 `json:"memused-percent"`
					Avail          float64 `json:"avail"`
					MemUsed        float64 `json:"memused"`
					SwpUsedPercent float64 `json:"swpused-percent"`
				} `json:"memory"`
			} `json:"statistics"`
		} `json:"hosts"`
	} `json:"sysstat"`
}

// ParseSadf reads `sadf -j -- -u -r -S` output. Memory is computed from
// "avail" so it matches the live gauge, not sysstat's cache-inclusive memused.
func ParseSadf(r io.Reader) ([]state.Sample, error) {
	var doc sadfDoc
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("host: sadf json: %w", err)
	}
	var out []state.Sample
	for _, h := range doc.Sysstat.Hosts {
		for _, st := range h.Statistics {
			ts := st.Timestamp
			if ts.UTC != 1 {
				continue
			}
			t, err := time.Parse("2006-01-02 15:04:05", ts.Date+" "+ts.Time)
			if err != nil {
				continue
			}
			s := state.Sample{T: t.Unix()}
			for _, c := range st.CPULoad {
				if c.CPU == "all" {
					s.CPU = round1(100 - c.Idle)
				}
			}
			if m := st.Memory; m != nil {
				// sar's memused excludes cache, so memused/percent recovers total RAM.
				if m.MemUsedPercent > 0 && m.Avail > 0 {
					total := m.MemUsed * 100 / m.MemUsedPercent
					s.MemUsedPct = round1(100 * (1 - m.Avail/total))
				} else {
					s.MemUsedPct = m.MemUsedPercent
				}
				s.SwapPct = m.SwpUsedPercent
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// Sysstat reads daily sa files with sadf, caching every day but today.
type Sysstat struct {
	Dir  string
	Sadf string
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	mod     time.Time
	samples []state.Sample
}

// NewSysstat reads files from dir with the given sadf binary.
func NewSysstat(dir, sadf string) *Sysstat {
	return &Sysstat{Dir: dir, Sadf: sadf, run: run, cache: map[string]cached{}}
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Since returns sysstat samples newer than t, oldest first.
func (s *Sysstat) Since(ctx context.Context, t time.Time) ([]state.Sample, error) {
	var all []state.Sample
	var firstErr error
	for day := t.UTC().Truncate(24 * time.Hour); !day.After(time.Now().UTC()); day = day.Add(24 * time.Hour) {
		path := filepath.Join(s.Dir, fmt.Sprintf("sa%02d", day.Day()))
		samples, err := s.day(ctx, path, day)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, x := range samples {
			if x.T >= t.Unix() {
				all = append(all, x)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].T < all[j].T })
	if len(all) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
}

func (s *Sysstat) day(ctx context.Context, path string, day time.Time) ([]state.Sample, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("host: sysstat: %w", err)
	}
	s.mu.Lock()
	c, ok := s.cache[path]
	s.mu.Unlock()
	if ok && c.mod.Equal(fi.ModTime()) {
		return c.samples, nil
	}
	out, err := s.run(ctx, s.Sadf, "-j", path, "--", "-u", "-r", "-S")
	if err != nil {
		return nil, fmt.Errorf("host: sysstat: %w", err)
	}
	samples, err := ParseSadf(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	// saDD files are reused monthly; drop samples from an older month's file.
	kept := samples[:0]
	for _, x := range samples {
		if time.Unix(x.T, 0).UTC().Truncate(24 * time.Hour).Equal(day) {
			kept = append(kept, x)
		}
	}
	s.mu.Lock()
	s.cache[path] = cached{mod: fi.ModTime(), samples: kept}
	s.mu.Unlock()
	return kept, nil
}
