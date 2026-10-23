package host

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProcFixtures(t *testing.T) {
	p := Proc{Root: "testdata/proc"}
	cpu, n, err := p.CPU()
	if err != nil || n != 1 || cpu.Idle != 475148977 || cpu.Steal != 1338089 {
		t.Fatalf("cpu %+v n=%d err=%v", cpu, n, err)
	}
	mem, err := p.Mem()
	if err != nil || mem.Total != 6045272*1024 || mem.Available != 4476760*1024 {
		t.Fatalf("mem %+v %v", mem, err)
	}
	if pct := mem.UsedPercent(); pct < 25.9 || pct > 26.0 {
		t.Fatalf("used%% %v", pct)
	}
	l, err := p.Load()
	if err != nil || l[0] != 1.32 {
		t.Fatalf("load %v %v", l, err)
	}
	up, err := p.Uptime()
	if err != nil || up < 4829290 {
		t.Fatalf("uptime %v %v", up, err)
	}
	if p.Kernel() != "6.17.0-test" {
		t.Fatal(p.Kernel())
	}
}

func TestUsage(t *testing.T) {
	a := CPUTimes{User: 100, Idle: 900}
	b := CPUTimes{User: 150, Idle: 940, Steal: 10}
	busy, steal, _ := Usage(a, b)
	// 100 ticks elapsed, 40 idle: 60% busy, of which 10 were steal.
	if busy != 60 || steal != 10 {
		t.Fatalf("busy %v steal %v", busy, steal)
	}
	if busy, _, _ := Usage(b, b); busy != 0 {
		t.Fatal("no time elapsed must be 0")
	}
}

func TestSamplerRing(t *testing.T) {
	s := NewSampler(Proc{Root: "testdata/proc"}, []string{os.TempDir()}, 3)
	base := time.Unix(1000, 0)
	for i := 0; i < 6; i++ {
		if err := s.Tick(base.Add(time.Duration(i) * 10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Since(time.Unix(0, 0))
	// The first tick only primes the CPU counter, so 5 samples went into a ring of 3.
	if len(got) != 3 || got[0].T != 1030 || got[2].T != 1050 {
		t.Fatalf("ring: %+v", got)
	}
	if h := s.Now(); h.MemTotal == 0 || len(h.Disks) != 1 || h.Disks[0].Total == 0 {
		t.Fatalf("now: %+v", h)
	}
}

func TestParseSadf(t *testing.T) {
	f, err := os.Open("testdata/sadf.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := ParseSadf(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d samples", len(got))
	}
	first := got[0]
	if first.T != time.Date(2026, 10, 22, 0, 10, 15, 0, time.UTC).Unix() || first.CPU < 1 || first.CPU > 3 || first.MemUsedPct < 26 || first.MemUsedPct > 29 {
		t.Fatalf("first: %+v", first)
	}
}

func TestSysstatCachesPastDays(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().UTC().Truncate(24 * time.Hour)
	path := dir + "/sa" + day.Format("02")
	if err := os.WriteFile(path, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := NewSysstat(dir, "sadf")
	s.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		ts := day.Add(time.Hour)
		return []byte(`{"sysstat":{"hosts":[{"statistics":[{"timestamp":{"date":"` + ts.Format("2006-01-02") + `","time":"01:00:00","utc":1},"cpu-load":[{"cpu":"all","idle":90}],"memory":{"memused-percent":20,"avail":500,"memused":200,"swpused-percent":1}}]}]}}`), nil
	}
	for i := 0; i < 2; i++ {
		got, err := s.Since(context.Background(), day)
		if err != nil || len(got) != 1 || got[0].CPU != 10 || got[0].MemUsedPct != 50 {
			t.Fatalf("got %+v %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged file should be read once, got %d", calls)
	}
	if _, err := s.Since(context.Background(), day.Add(-48*time.Hour)); err != nil && !strings.Contains(err.Error(), "sysstat") {
		t.Fatal(err)
	}
}
