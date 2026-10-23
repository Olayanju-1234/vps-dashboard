package collect

import (
	"fmt"
	"sort"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

var levelRank = map[string]int{"critical": 0, "warning": 1, "info": 2}

// alertsLocked lists everything worth a look, worst first.
func (c *Collector) alertsLocked(s *state.State, now time.Time) []state.Alert {
	var out []state.Alert
	add := func(level, project, area, format string, args ...any) {
		out = append(out, state.Alert{Level: level, Project: project, Area: area, Message: fmt.Sprintf(format, args...)})
	}

	h := s.Host
	if h.MemTotal > 0 {
		if avail := 100 * float64(h.MemAvailable) / float64(h.MemTotal); avail < 10 {
			add("critical", "", "host", "only %.0f%% of memory available", avail)
		} else if avail < 20 {
			add("warning", "", "host", "only %.0f%% of memory available", avail)
		}
	}
	if h.SwapTotal > 0 && float64(h.SwapTotal-h.SwapFree)/float64(h.SwapTotal) > 0.5 {
		add("warning", "", "host", "swap is more than half used")
	}
	for _, d := range h.Disks {
		if d.Total == 0 {
			continue
		}
		used := 100 * (1 - float64(d.Free)/float64(d.Total))
		if used > 90 {
			add("critical", "", "host", "disk %s is %.0f%% full", d.Path, used)
		} else if used > 80 {
			add("warning", "", "host", "disk %s is %.0f%% full", d.Path, used)
		}
	}
	if c.hostErr != "" {
		add("warning", "", "host", "host metrics unavailable: %s", c.hostErr)
	}
	if c.unitErr != "" {
		add("warning", "", "units", "systemctl failed: %s", c.unitErr)
	}
	if s.Probe.Stale {
		msg := "the probe has not written a snapshot recently, so env names, backups, logs and nginx counts are missing or old"
		if s.Probe.Error != "" {
			msg += " (" + s.Probe.Error + ")"
		}
		add("warning", "", "probe", "%s", msg)
	}

	for _, p := range s.Projects {
		for _, u := range p.Units {
			switch {
			case u.Load != "loaded":
				add("critical", p.ID, "units", "%s is %s", u.Name, u.Load)
			case u.Active == "failed":
				add("critical", p.ID, "units", "%s has failed (%s)", u.Name, u.Result)
			case u.Active != "active":
				add("critical", p.ID, "units", "%s is %s", u.Name, u.Active)
			case u.Restarts > 0:
				add("warning", p.ID, "units", "%s was restarted %d time(s) by systemd since it started", u.Name, u.Restarts)
			}
			if u.MemoryMax > 0 && float64(u.MemoryBytes) > 0.9*float64(u.MemoryMax) {
				add("warning", p.ID, "units", "%s is at %.0f%% of its memory limit", u.Name, 100*float64(u.MemoryBytes)/float64(u.MemoryMax))
			}
		}
		for _, ch := range p.Checks {
			if !ch.CheckedAt.IsZero() && !ch.OK {
				detail := ch.Error
				if detail == "" {
					detail = fmt.Sprintf("got %d, want %d", ch.Status, ch.Expect)
				}
				add("critical", p.ID, "health", "%s: %s", ch.URL, detail)
			}
		}
		d := p.Deploy
		switch {
		case d.Status == "behind":
			add("warning", p.ID, "deploy", "deployed commit is %d behind %s", d.Behind, "main")
		case d.Error != "":
			add("info", p.ID, "deploy", "can't tell what's deployed: %s", d.Error)
		}
		if d.Status != "behind" && d.Note != "" && !d.Exact && d.Method == "tree" {
			add("info", p.ID, "deploy", "%s", d.Note)
		}
		if d.NeedsRestart {
			add("warning", p.ID, "deploy", "files changed %s ago and no unit has restarted since; a restart may be pending", ago(now, d.DeployedAt))
		}
		for _, b := range p.Backups {
			switch b.Status {
			case "missing":
				add("critical", p.ID, "backups", "%s: no backup files in %s", b.Label, b.Dir)
			case "stale":
				add("warning", p.ID, "backups", "%s: newest backup is %s old", b.Label, ago(now, b.NewestTime))
			case "error":
				if !s.Probe.Stale {
					add("warning", p.ID, "backups", "%s: %s", b.Label, b.Error)
				}
			}
		}
		if p.Journal.Errors1h > 0 {
			add("info", p.ID, "logs", "%d error line(s) in the last hour", p.Journal.Errors1h)
		}
	}

	for _, pt := range s.Ports {
		switch pt.Status {
		case "exposed":
			add("critical", pt.Project, "ports", "port %d (%s) listens on %v, not only 127.0.0.1", pt.Port, pt.Label, pt.Listening)
		case "unexpected":
			add("critical", "", "ports", "unknown public listener on port %d %v (user %s)", pt.Port, pt.Listening, pt.User)
		}
	}
	for _, cert := range s.TLS {
		switch {
		case cert.Error != "" && cert.NotAfter.IsZero():
			add("warning", cert.Project, "tls", "%s: %s", cert.Host, cert.Error)
		case cert.DaysLeft < 7:
			add("critical", cert.Project, "tls", "%s certificate expires in %.0f days", cert.Host, cert.DaysLeft)
		case cert.DaysLeft < 21:
			add("warning", cert.Project, "tls", "%s certificate expires in %.0f days", cert.Host, cert.DaysLeft)
		case !cert.Valid:
			add("warning", cert.Project, "tls", "%s certificate does not verify: %s", cert.Host, cert.Error)
		}
	}
	for _, ef := range s.EnvFiles {
		switch {
		case ef.Error != "":
			add("warning", ef.Project, "env", "%s: %s", ef.Path, ef.Error)
		case !ef.Exists:
			add("warning", ef.Project, "env", "%s does not exist", ef.Path)
		case ef.ExpectMode != "" && ef.Mode != ef.ExpectMode:
			add("warning", ef.Project, "env", "%s has mode %s, expected %s", ef.Path, ef.Mode, ef.ExpectMode)
		case ef.ExpectOwner != "" && ef.Owner != ef.ExpectOwner:
			add("warning", ef.Project, "env", "%s is owned by %s, expected %s", ef.Path, ef.Owner, ef.ExpectOwner)
		}
	}
	if n := len(s.EnvCopies); n > 0 {
		add("info", "", "env", "%d copies of env files sit on disk (backups from past edits); each holds old secrets", n)
	}
	sort.SliceStable(out, func(i, j int) bool { return levelRank[out[i].Level] < levelRank[out[j].Level] })
	return out
}

func projectStatus(p state.Project, alerts []state.Alert) string {
	status := "ok"
	for _, a := range alerts {
		if a.Project != p.ID {
			continue
		}
		switch a.Level {
		case "critical":
			return "critical"
		case "warning":
			status = "warn"
		}
	}
	return status
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.0fd", d.Hours()/24)
}
