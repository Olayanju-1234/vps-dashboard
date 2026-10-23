package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/envfile"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func entryJSON(t time.Time, unit, sysUnit, prio string, msg any) string {
	m := map[string]any{
		"__CURSOR":             fmt.Sprintf("c-%d", t.UnixMicro()),
		"__REALTIME_TIMESTAMP": fmt.Sprint(t.UnixMicro()),
		"MESSAGE":              msg,
		"PRIORITY":             prio,
	}
	if unit != "" {
		m["_SYSTEMD_UNIT"] = unit
	}
	if sysUnit != "" {
		m["UNIT"] = sysUnit
		m["_PID"] = "1"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestClassify(t *testing.T) {
	cases := []struct {
		prio  int
		msg   string
		level string
	}{
		{6, `{"level":50,"msg":"db down"}`, "error"},
		{6, `level=ERROR msg="append failed"`, "error"},
		{6, `[Nest] 12  - 10/23/2026   ERROR [TenantService] boom`, "error"},
		{6, `Error: connect ECONNREFUSED 127.0.0.1:5432`, "error"},
		{6, `panic: runtime error: index out of range`, "error"},
		{6, `app.service: Main process exited, code=exited, status=1/FAILURE`, "error"},
		{6, `app.service: Main process exited, code=killed, status=15/TERM`, ""},
		{6, `app.service: Main process exited, code=killed, status=9/KILL`, "error"},
		{6, `app.service: Failed with result 'exit-code'.`, "error"},
		{6, `app.service: Scheduled restart job, restart counter is at 3.`, "warning"},
		{6, `{"level":40,"msg":"slow"}`, "warning"},
		{4, `anything at warning priority`, "warning"},
		{3, `anything at err priority`, "error"},
		{6, `GET /api/health 200 errors=0 lastError=null`, ""},
		{6, `request completed`, ""},
	}
	for _, c := range cases {
		got, ok := Classify(c.prio, c.msg)
		if got != c.level || ok != (c.level != "") {
			t.Errorf("Classify(%d, %q) = %q, %v; want %q", c.prio, c.msg, got, ok, c.level)
		}
	}
}

func TestCollectScrubsRedactsAndCounts(t *testing.T) {
	var sb envfile.ScrubBuilder
	if _, err := envfile.Parse(strings.NewReader("DATABASE_URL=postgres://u:CANARY-pass-77@h/db\nTOKEN=CANARY-token-88\n"), &sb); err != nil {
		t.Fatal(err)
	}
	scrub := sb.Build()

	lines := []string{
		entryJSON(now.Add(-3*time.Hour), "shop.service", "", "6", `{"level":50,"msg":"cannot connect postgres://u:CANARY-pass-77@h/db"}`),
		entryJSON(now.Add(-30*time.Minute), "shop.service", "", "6", `level=ERROR token=CANARY-token-88 user jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig`),
		entryJSON(now.Add(-29*time.Minute), "shop.service", "", "6", `level=ERROR token=CANARY-token-88 user jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig`),
		entryJSON(now.Add(-20*time.Minute), "", "shop.service", "6", `shop.service: Main process exited, code=exited, status=1/FAILURE`),
		entryJSON(now.Add(-10*time.Minute), "shop.service", "", "6", `GET /health 200`),
		entryJSON(now.Add(-5*time.Minute), "other.service", "", "3", `not ours`),
		entryJSON(now.Add(-time.Minute), "shop.service", "", "6", []int{0x57, 0x41, 0x52, 0x4e, 0xff}),
		// Another app claiming to speak for shop.service: not PID 1, so ignored.
		strings.Replace(entryJSON(now.Add(-2*time.Minute), "other.service", "", "3", "spoofed"), `"PRIORITY"`, `"UNIT":"shop.service","_PID":"4242","PRIORITY"`, 1),
		// A clock stepped back: an entry from the next hour must not crash the counters.
		entryJSON(now.Add(90*time.Minute), "shop.service", "", "3", "from the future"),
	}
	var gotArgs [][]string
	c := New("journalctl", map[string]string{"shop.service": "shop"})
	c.now = func() time.Time { return now }
	c.open = func(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
		gotArgs = append(gotArgs, args)
		body := ""
		if len(gotArgs) == 1 {
			body = strings.Join(lines, "\n") + "\n"
		}
		return io.NopCloser(strings.NewReader(body)), func() error { return nil }, nil
	}
	if err := c.Collect(context.Background(), scrub); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), scrub); err != nil {
		t.Fatal(err)
	}
	if !contains(gotArgs[0], "--since=-24h") || !contains(gotArgs[1], "--after-cursor=c-"+fmt.Sprint(now.Add(90*time.Minute).UnixMicro())) {
		t.Fatalf("args: %v", gotArgs)
	}
	res := c.Results()
	if len(res) != 1 {
		t.Fatalf("results: %+v", res)
	}
	r := res[0]
	if r.Errors24 != 4 || r.Warns24 != 1 || r.Errors1h != 3 || strings.Contains(fmt.Sprint(r.Lines), "spoofed") {
		t.Fatalf("counts: e24=%d w24=%d e1h=%d", r.Errors24, r.Warns24, r.Errors1h)
	}
	if len(r.Lines) != 5 || r.Lines[3].Count != 2 {
		t.Fatalf("expected the repeated line collapsed: %+v", r.Lines)
	}
	if r.Lines[0].Message != "from the future" || r.Lines[1].Message != "WARN?" || r.Lines[2].Unit != "shop.service" {
		t.Fatalf("lines: %+v", r.Lines)
	}
	all, _ := json.Marshal(res)
	for _, secret := range []string{"CANARY-pass-77", "CANARY-token-88", "eyJzdWIiOiIxIn0"} {
		if strings.Contains(string(all), secret) {
			t.Fatalf("secret %q survived: %s", secret, all)
		}
	}
	if !strings.Contains(string(all), "[env:DATABASE_URL]") {
		t.Fatalf("expected env placeholder: %s", all)
	}
	if r.PerHour[22] != 3 || r.PerHour[20] != 1 || r.PerHour[23] != 0 {
		t.Fatalf("per hour: %v", r.PerHour)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
