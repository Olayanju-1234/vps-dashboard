package host

import (
	"os"
	"sync"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Sampler keeps a fixed-size ring of recent samples in memory. A restart
// loses it; sysstat covers anything older than the ring.
type Sampler struct {
	proc  Proc
	disks []string

	mu       sync.Mutex
	ring     []state.Sample
	next     int
	full     bool
	prevCPU  CPUTimes
	havePrev bool
	now      state.Host
}

// NewSampler keeps size samples; at a 10 s tick, 2160 is six hours.
func NewSampler(proc Proc, disks []string, size int) *Sampler {
	return &Sampler{proc: proc, disks: disks, ring: make([]state.Sample, size)}
}

// Tick takes one reading.
func (s *Sampler) Tick(at time.Time) error {
	cpu, ncpu, err := s.proc.CPU()
	if err != nil {
		return err
	}
	mem, err := s.proc.Mem()
	if err != nil {
		return err
	}
	load, _ := s.proc.Load()
	up, _ := s.proc.Uptime()
	hostname, _ := os.Hostname()

	s.mu.Lock()
	defer s.mu.Unlock()
	h := state.Host{
		Hostname: hostname, Kernel: s.proc.Kernel(), CPUs: ncpu, UptimeSeconds: up, Load: load,
		MemTotal: mem.Total, MemAvailable: mem.Available, SwapTotal: mem.SwapTotal, SwapFree: mem.SwapFree,
	}
	for _, d := range s.disks {
		if total, free, err := Statfs(d); err == nil {
			h.Disks = append(h.Disks, state.Disk{Path: d, Total: total, Free: free})
		}
	}
	if s.havePrev {
		h.CPUPercent, h.StealPercent, h.IOWaitPercent = Usage(s.prevCPU, cpu)
		s.ring[s.next] = state.Sample{T: at.Unix(), CPU: round1(h.CPUPercent), MemUsedPct: round1(mem.UsedPercent()), SwapPct: round1(mem.SwapPercent())}
		s.next = (s.next + 1) % len(s.ring)
		if s.next == 0 {
			s.full = true
		}
	}
	s.prevCPU, s.havePrev = cpu, true
	s.now = h
	return nil
}

// Now is the latest reading.
func (s *Sampler) Now() state.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.now
	h.Disks = append([]state.Disk(nil), s.now.Disks...)
	return h
}

// Since returns samples newer than t, oldest first.
func (s *Sampler) Since(t time.Time) []state.Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []state.Sample
	add := func(x state.Sample) {
		if x.T >= t.Unix() {
			out = append(out, x)
		}
	}
	if s.full {
		for _, x := range s.ring[s.next:] {
			add(x)
		}
	}
	for _, x := range s.ring[:s.next] {
		add(x)
	}
	return out
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
