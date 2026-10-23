package nginxlog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

var now = time.Date(2026, 10, 23, 12, 30, 0, 0, time.UTC)

func line(t time.Time, path string, status int, ua, host string) string {
	s := fmt.Sprintf(`203.0.113.9 - - [%s] "GET %s HTTP/1.1" %d 54 "-" "%s"`, t.Format("02/Jan/2006:15:04:05 -0700"), path, status, ua)
	if host != "" {
		s += fmt.Sprintf(` "%s" 0.002`, host)
	}
	return s + "\n"
}

func appendTo(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrackerCountsDedupesAndRotates(t *testing.T) {
	dir := t.TempDir()
	hostLog := filepath.Join(dir, "app.access.log")
	shared := filepath.Join(dir, "access.log")

	t1 := now.Add(-2 * time.Hour)
	appLine := line(t1, "/health", 200, "curl/8", "")
	// nginx writes the same request to both logs; only the app log has a host.
	appendTo(t, hostLog, appLine[:len(appLine)-1]+` "API.Shop.example:443" 0.002`+"\n")
	appendTo(t, shared, appLine)
	appendTo(t, shared, line(t1, "/", 404, "scanner", ""))
	appendTo(t, shared, line(t1, "/", 200, "vps-dashboard (+health check)", ""))
	appendTo(t, shared, line(now.Add(-30*time.Hour), "/old", 200, "x", "")) // outside the window

	tr := New([]Source{{Path: hostLog, Label: "app log"}, {Path: shared, Label: "shared log"}}, map[string]string{"api.shop.example": "shop"}, "vps-dashboard")
	tr.now = func() time.Time { return now }
	if errs := tr.Update(); len(errs) > 0 {
		t.Fatal(errs)
	}
	sites := byName(tr.Sites())
	if s := sites["api.shop.example"]; s.Total24 != 1 || s.Project != "shop" || s.Status[0] != 1 {
		t.Fatalf("host site: %+v", s)
	}
	if s := sites["shared log"]; s.Total24 != 1 || s.Status[2] != 1 || !s.NoHost {
		t.Fatalf("shared log should hold only the scanner 404: %+v", s)
	}

	// Rotate the shared log the way logrotate does, with a line written in between.
	appendTo(t, shared, line(now.Add(-time.Minute), "/a", 500, "x", ""))
	if err := os.Rename(shared, shared+".1"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, shared, line(now, "/b", 302, "x", ""))
	if errs := tr.Update(); len(errs) > 0 {
		t.Fatal(errs)
	}
	s := byName(tr.Sites())["shared log"]
	if s.Total24 != 3 || s.Status[3] != 1 || s.Status[1] != 1 {
		t.Fatalf("after rotation: %+v", s)
	}
	if s.PerHour[Hours-1] != 2 || len(s.PerHour) != Hours {
		t.Fatalf("hourly: %v", s.PerHour)
	}

	// A partial line is not counted until its newline arrives.
	partial := line(now, "/c", 200, "x", "")
	appendTo(t, shared, partial[:20])
	tr.Update()
	if got := byName(tr.Sites())["shared log"].Total24; got != 3 {
		t.Fatalf("partial line counted: %d", got)
	}
	appendTo(t, shared, partial[20:])
	tr.Update()
	if got := byName(tr.Sites())["shared log"].Total24; got != 4 {
		t.Fatalf("completed line not counted: %d", got)
	}
}

func byName(sites []state.NginxSite) map[string]state.NginxSite {
	m := map[string]state.NginxSite{}
	for _, s := range sites {
		m[s.Site] = s
	}
	return m
}
