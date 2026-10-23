package collect

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/netstat"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

func testCollector(t *testing.T, cfgJSON string) *Collector {
	cfg, err := config.Parse([]byte(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, "t", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func listener(ip string, port int) netstat.Listener {
	return netstat.Listener{IP: net.ParseIP(ip), Port: port, UID: 0}
}

func TestPortsMap(t *testing.T) {
	c := testCollector(t, `{"ports":[{"port":22,"project":"shared","label":"sshd","public":true}],
		"projects":[{"id":"shop","ports":[{"port":3000,"label":"API"},{"port":3001,"label":"web"},{"port":3002,"label":"admin"}]}]}`)
	c.listeners = []netstat.Listener{
		listener("0.0.0.0", 22), listener("::", 22),
		listener("127.0.0.1", 3000),
		listener("0.0.0.0", 3001),
		listener("127.0.0.1", 8400),
		listener("127.0.0.1", 5555), listener("0.0.0.0", 6666),
	}
	got := map[int]state.Port{}
	for _, p := range c.portsLocked() {
		got[p.Port] = p
	}
	want := map[int]string{22: "ok", 3000: "ok", 3001: "exposed", 3002: "down", 8400: "ok", 5555: "unlisted", 6666: "unexpected"}
	for port, status := range want {
		if got[port].Status != status {
			t.Errorf("port %d: %q, want %q (%+v)", port, got[port].Status, status, got[port])
		}
	}
	if got[8400].Project != "vps-dashboard" || len(got[22].Listening) != 2 {
		t.Fatalf("self/dual-stack: %+v %+v", got[8400], got[22])
	}
}

func TestAlertsAndStatus(t *testing.T) {
	c := testCollector(t, `{"projects":[{"id":"shop","units":["api"],"checks":[{"url":"https://x/health"}]},{"id":"calm","units":["calm"]}]}`)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.unitState = map[string]state.Unit{
		"api.service":  {Name: "api.service", Load: "loaded", Active: "failed", Result: "exit-code"},
		"calm.service": {Name: "calm.service", Load: "loaded", Active: "active", Sub: "running", Restarts: 1},
	}
	c.checks = map[string]state.Check{"https://x/health": {URL: "https://x/health", Status: 502, Expect: 200, CheckedAt: now}}
	c.snap = state.Snapshot{GeneratedAt: now, Backups: []state.Backup{{Project: "shop", Label: "db", Status: "stale", NewestTime: now.Add(-50 * time.Hour)}}}
	s := c.State()
	if len(s.Alerts) == 0 || s.Alerts[0].Level != "critical" {
		t.Fatalf("critical alerts must sort first: %+v", s.Alerts)
	}
	joined := ""
	for _, a := range s.Alerts {
		joined += a.Level + ":" + a.Message + "\n"
	}
	for _, want := range []string{"critical:api.service has failed", "critical:https://x/health: got 502", "warning:db: newest backup is 2d old", "warning:calm.service was restarted 1 time"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing alert %q in\n%s", want, joined)
		}
	}
	statuses := map[string]string{}
	for _, p := range s.Projects {
		statuses[p.ID] = p.Status
	}
	if statuses["shop"] != "critical" || statuses["calm"] != "warn" {
		t.Fatalf("statuses: %v", statuses)
	}
	if s.Probe.Stale {
		t.Fatalf("fresh snapshot should not be stale: %+v", s.Probe)
	}
}

func TestDownsample(t *testing.T) {
	in := []state.Sample{{T: 0, CPU: 10}, {T: 30, CPU: 20}, {T: 60, CPU: 40}, {T: 200, CPU: 1}}
	out := Downsample(in, 60)
	if len(out) != 3 || out[0].CPU != 15 || out[1].CPU != 40 || out[2].T != 180 {
		t.Fatalf("downsample: %+v", out)
	}
}

func TestLatestStart(t *testing.T) {
	now := time.Now()
	units := []state.Unit{
		{Sub: "running", ActiveSince: now.Add(-30 * 24 * time.Hour)}, // a broker that never restarts
		{Sub: "running", ActiveSince: now.Add(-time.Hour)},
		{Sub: "waiting", ActiveSince: now}, // timers don't count
	}
	if got := latestStart(units); !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("latestStart = %v", got)
	}
}
