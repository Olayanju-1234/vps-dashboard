package systemd

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestParseRealOutput(t *testing.T) {
	f, err := os.Open("testdata/show.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	units, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 6 {
		t.Fatalf("got %d units", len(units))
	}
	wf := units[0]
	if wf.Name != "engine.service" || wf.Active != "active" || wf.Sub != "running" || wf.MemoryMax != 268435456 || wf.MemoryBytes == 0 || wf.ActiveSince.IsZero() || !wf.cpuKnown {
		t.Fatalf("engine: %+v", wf)
	}
	if len(units[1].EnvFiles) != 1 || units[1].EnvFiles[0] != "/opt/shop/.env" {
		t.Fatalf("env files: %v", units[1].EnvFiles)
	}
	timer := units[2]
	if timer.LastTrigger.IsZero() || timer.NextElapse.IsZero() || timer.Sub != "waiting" {
		t.Fatalf("timer: %+v", timer)
	}
	if units[3].MemoryMax != 0 || units[3].MemoryBytes != 0 {
		t.Fatalf("infinity/[not set] should be 0: %+v", units[3])
	}
	if units[4].Load != "not-found" {
		t.Fatalf("missing unit: %+v", units[4])
	}
}

func TestParseTime(t *testing.T) {
	if got := parseTime("@1792608395"); got.Unix() != 1792608395 {
		t.Fatal(got)
	}
	if got := parseTime("Sat 2026-10-24 03:31:40 UTC"); got.Format(time.RFC3339) != "2026-10-24T03:31:40Z" {
		t.Fatal(got)
	}
	if !parseTime("").IsZero() || !parseTime("n/a").IsZero() {
		t.Fatal("empty should be zero")
	}
}

func TestPollRates(t *testing.T) {
	calls := 0
	p := NewPoller("systemctl")
	p.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		nanos := "1000000000"
		if calls > 1 {
			nanos = "1500000000"
		}
		return []byte("Id=a.service\nActiveState=active\nCPUUsageNSec=" + nanos + "\n"), nil
	}
	if _, err := p.Poll(context.Background(), []string{"a.service"}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	s := p.prev["a.service"]
	s.at = s.at.Add(-10 * time.Second) // pretend the last poll was 10 s ago
	p.prev["a.service"] = s
	p.mu.Unlock()
	got, err := p.Poll(context.Background(), []string{"a.service"})
	if err != nil {
		t.Fatal(err)
	}
	if pct := got["a.service"].CPUPercent; pct < 4.9 || pct > 5.1 {
		t.Fatalf("0.5 s of CPU over 10 s should be ~5%%, got %v", pct)
	}
}

func TestCanonical(t *testing.T) {
	if Canonical("nginx") != "nginx.service" || Canonical("x.timer") != "x.timer" {
		t.Fatal("canonical")
	}
}
