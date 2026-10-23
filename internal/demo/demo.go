// Package demo serves made-up state, so the UI can be tried and screenshotted
// on a laptop without /proc, systemd or a real server.
package demo

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Source fabricates a plausible small server.
type Source struct {
	version string
	start   time.Time
}

// New returns a demo source.
func New(version string) *Source { return &Source{version: version, start: time.Now()} }

const (
	mib = 1 << 20
	gib = 1 << 30
)

func wave(t, period, phase float64) float64 {
	return math.Sin(2*math.Pi*t/period + phase)
}

func cpuAt(t float64) float64 {
	v := 4 + 2.5*wave(t, 3600, 0) + 1.5*wave(t, 600, 1) + 1.2*wave(t, 97, 2)
	if math.Mod(t, 86400) > 9000 && math.Mod(t, 86400) < 9900 {
		v += 35 // a nightly build
	}
	return math.Max(0.5, v)
}

func memAt(t float64) float64 { return 27 + 2*wave(t, 7200, 0.5) + 0.6*wave(t, 300, 0) }

// History returns synthetic samples.
func (s *Source) History(_ context.Context, rng string) (state.History, error) {
	spans := map[string]struct {
		d    time.Duration
		step int64
		src  string
	}{"1h": {time.Hour, 10, "sampler"}, "6h": {6 * time.Hour, 60, "sampler"}, "24h": {24 * time.Hour, 600, "sysstat"}, "7d": {7 * 24 * time.Hour, 600, "sysstat"}}
	sp, ok := spans[rng]
	if !ok {
		return state.History{}, fmt.Errorf("demo: unknown range %q", rng)
	}
	now := time.Now().Unix()
	h := state.History{Range: rng, Source: sp.src, Step: int(sp.step)}
	for t := now - int64(sp.d.Seconds()); t <= now; t += sp.step {
		ft := float64(t)
		h.Samples = append(h.Samples, state.Sample{T: t, CPU: round1(cpuAt(ft)), MemUsedPct: round1(memAt(ft)), SwapPct: 0})
	}
	return h, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Journal returns a few made-up problem lines.
func (s *Source) Journal(project string) (state.ProjectJournal, error) {
	st := s.State()
	for _, p := range st.Projects {
		if p.ID != project {
			continue
		}
		j := p.Journal
		now := time.Now().UTC()
		switch project {
		case "storefront":
			j.Lines = []state.JournalLine{
				{Time: now.Add(-14 * time.Minute), Unit: "storefront-api.service", Level: "error", Count: 3, Message: `[Nest] 4120  - ERROR [PaymentsService] charge failed for order 8812: card_declined`},
				{Time: now.Add(-2 * time.Hour), Unit: "storefront-api.service", Level: "warning", Count: 1, Message: `{"level":40,"msg":"slow query","ms":1840,"db":"postgres://[redacted]@127.0.0.1:5432/storefront"}`},
				{Time: now.Add(-5 * time.Hour), Unit: "storefront-web.service", Level: "error", Count: 1, Message: `Error: connect ECONNREFUSED 127.0.0.1:3101 (token=[redacted])`},
			}
		case "billing":
			j.Lines = []state.JournalLine{
				{Time: now.Add(-40 * time.Minute), Unit: "billing-worker.service", Level: "error", Count: 12, Message: `level=ERROR msg="invoice webhook rejected" status=401 secret=[env:BILLING_WEBHOOK_SECRET]`},
				{Time: now.Add(-41 * time.Minute), Unit: "billing-worker.service", Level: "warning", Count: 1, Message: `billing-worker.service: Scheduled restart job, restart counter is at 2.`},
			}
		}
		return j, nil
	}
	return state.ProjectJournal{}, fmt.Errorf("demo: unknown project %q", project)
}

func hourly(base int, seed float64) []int {
	out := make([]int, 24)
	for i := range out {
		v := float64(base) * (1 + 0.6*math.Sin(float64(i)/24*2*math.Pi+seed))
		out[i] = int(math.Max(0, v))
	}
	return out
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

func unit(name, desc string, memMB, maxMB uint64, cpu float64, restarts int, since time.Duration) state.Unit {
	return state.Unit{
		Name: name, Description: desc, Load: "loaded", Active: "active", Sub: "running", Enabled: "enabled",
		MainPID: 1000 + int(memMB), Restarts: restarts, MemoryBytes: memMB * mib, MemoryPeak: memMB * mib * 13 / 10,
		MemoryMax: maxMB * mib, CPUPercent: cpu, Tasks: 11, ActiveSince: time.Now().Add(-since).UTC(),
	}
}

func check(url string, ms float64, status int) state.Check {
	return state.Check{URL: url, Status: status, Expect: 200, OK: status == 200, LatencyMS: ms, CheckedAt: time.Now().UTC()}
}

func vars(names ...string) []state.EnvVar {
	out := make([]state.EnvVar, len(names))
	for i, n := range names {
		set := true
		if n[0] == '-' {
			n, set = n[1:], false
		}
		out[i] = state.EnvVar{Name: n, Set: set}
	}
	return out
}

// State fabricates the full view; the numbers drift so the page looks alive.
func (s *Source) State() state.State {
	now := time.Now().UTC()
	t := float64(now.Unix())
	day := 24 * time.Hour
	cpu := cpuAt(t)

	st := state.State{GeneratedAt: now, Version: s.version, Demo: true}
	st.Host = state.Host{
		Hostname: "demo-box", Kernel: "6.8.0-demo", CPUs: 1, UptimeSeconds: 41 * 86400, CPUPercent: round1(cpu),
		StealPercent: 0.3, IOWaitPercent: 0.1, Load: [3]float64{0.21 + cpu/100, 0.18, 0.12},
		MemTotal: 6 * gib, MemAvailable: uint64((100 - memAt(t)) / 100 * 6 * gib), SwapTotal: gib, SwapFree: gib - 3*mib,
		Disks: []state.Disk{{Path: "/", Total: 48 * gib, Free: 33 * gib}},
	}
	st.Probe = state.ProbeInfo{GeneratedAt: now.Add(-12 * time.Second), AgeSeconds: 12}

	backup := func(project, label, dir, newest string, age time.Duration, size int64, status string) state.Backup {
		return state.Backup{Project: project, Label: label, Dir: dir, Glob: "*", MaxAge: "26h0m0s", Count: 14, TotalBytes: size * 14,
			Newest: newest, NewestBytes: size, NewestTime: now.Add(-age), Status: status}
	}
	jr := func(project string, e1h, e24, w24 int, seed float64) state.ProjectJournal {
		ph := hourly(e24/24, seed)
		return state.ProjectJournal{Project: project, Errors1h: e1h, Errors24: e24, Warns24: w24, PerHour: ph}
	}

	st.Projects = []state.Project{
		{
			ID: "storefront", Name: "Storefront", Description: "NestJS API and Next.js shop", Repo: "example/storefront",
			Units: []state.Unit{
				unit("storefront-api.service", "Storefront API", 182, 512, round1(1.4+0.6*wave(t, 120, 0)), 0, 3*day),
				unit("storefront-web.service", "Storefront web", 121, 384, 0.3, 0, 3*day),
			},
			Checks: []state.Check{check("https://shop.example.com/", 42, 200), check("https://api.shop.example.com/health", 18, 200)},
			Deploy: state.Deploy{Method: "tree", Commit: "4be1c0d9a1e2", Subject: "feat(cart): keep the basket across devices", CommitTime: now.Add(-26 * time.Hour),
				Exact: true, Compared: 412, DeployedAt: now.Add(-25 * time.Hour), MainCommit: "4be1c0d9a1e2", MainSubject: "feat(cart): keep the basket across devices",
				MainTime: now.Add(-26 * time.Hour), MainSource: "github", Behind: 0, Status: "current"},
			Backups:  []state.Backup{backup("storefront", "Postgres nightly", "/var/backups/storefront", "storefront-20261023T023001Z.sql.gz", 9*time.Hour, 28_253, "ok")},
			Journal:  jr("storefront", 3, 9, 4, 0),
			EnvFiles: []string{"/opt/storefront/api/.env"},
		},
		{
			ID: "billing", Name: "Billing", Description: "Invoice workers on RabbitMQ", Repo: "example/billing",
			Units: []state.Unit{
				unit("billing-api.service", "Billing API", 96, 256, 0.4, 0, 6*day),
				unit("billing-worker.service", "Billing worker", 141, 256, round1(2.1+wave(t, 60, 1)), 2, 40*time.Minute),
				unit("rabbitmq-server.service", "RabbitMQ", 118, 0, 0.6, 0, 20*day),
			},
			Checks: []state.Check{check("https://billing.example.com/health", 23, 200)},
			Deploy: state.Deploy{Method: "tree", Commit: "91ac22e07b4d", Subject: "fix(invoices): round VAT per line", CommitTime: now.Add(-5 * day),
				Exact: true, Compared: 233, DeployedAt: now.Add(-5 * day), MainCommit: "c0ffee1234ab", MainSubject: "feat(invoices): PDF receipts",
				MainTime: now.Add(-day), MainSource: "github", Behind: 3, Status: "behind"},
			Backups:  []state.Backup{backup("billing", "Postgres nightly", "/var/backups/billing", "billing-20261021T024501Z.sql.gz", 57*time.Hour, 23_593, "stale")},
			Journal:  jr("billing", 12, 48, 6, 1.2),
			EnvFiles: []string{"/opt/billing/.env"},
		},
		{
			ID: "engine", Name: "Engine", Description: "Go workflow engine (public demo)", Repo: "example/engine",
			Units: []state.Unit{
				unit("engine.service", "Durable engine", 9, 256, 0.1, 0, 2*day),
				{Name: "engine-backup.timer", Description: "Daily engine backup", Load: "loaded", Active: "active", Sub: "waiting", Enabled: "enabled",
					LastTrigger: now.Add(-8 * time.Hour), NextElapse: now.Add(16 * time.Hour)},
			},
			Checks: []state.Check{check("https://engine.example.com/api/health", 11, 200)},
			Deploy: state.Deploy{Method: "go_binary", Commit: "9f19ab8c41d0", Subject: "feat(engine): parallel steps", CommitTime: now.Add(-50 * time.Hour),
				Exact: true, DeployedAt: now.Add(-2*day + time.Hour), MainCommit: "9f19ab8c41d0", MainSubject: "feat(engine): parallel steps", MainTime: now.Add(-50 * time.Hour),
				MainSource: "github", Behind: 0, Status: "current"},
			Statics: []state.StaticSite{{Label: "landing page", Path: "/var/www/engine", DeployedAt: now.Add(-2 * day)}},
			Backups: []state.Backup{backup("engine", "history files", "/var/backups/engine", "data-20261023T033859Z.tar.gz", 8*time.Hour, 5_631, "ok")},
			Journal: jr("engine", 0, 0, 1, 2),
		},
		{
			ID: "shared", Name: "Shared", Description: "nginx, Postgres, Redis, sshd",
			Units: []state.Unit{
				unit("nginx.service", "nginx", 14, 0, 0.2, 0, 9*day),
				unit("postgresql@16-main.service", "PostgreSQL 16", 212, 0, 0.5, 0, 20*day),
				unit("redis-server.service", "Redis", 7, 0, 0.1, 0, 20*day),
			},
			Deploy:  state.Deploy{Behind: -1, Status: "unknown"},
			Journal: jr("shared", 0, 0, 0, 3),
		},
	}
	st.Projects[1].Units[1].Restarts = 2

	st.Ports = []state.Port{
		{Port: 22, Project: "shared", Label: "sshd", Public: true, Listening: []string{"0.0.0.0", "::"}, User: "root", Status: "ok"},
		{Port: 80, Project: "shared", Label: "nginx", Public: true, Listening: []string{"0.0.0.0"}, User: "root", Status: "ok"},
		{Port: 443, Project: "shared", Label: "nginx", Public: true, Listening: []string{"0.0.0.0"}, User: "root", Status: "ok"},
		{Port: 3100, Project: "storefront", Label: "API", Listening: []string{"127.0.0.1"}, User: "deploy", Status: "ok"},
		{Port: 3101, Project: "storefront", Label: "web", Listening: []string{"127.0.0.1"}, User: "deploy", Status: "ok"},
		{Port: 4200, Project: "billing", Label: "API", Listening: []string{"127.0.0.1"}, User: "deploy", Status: "ok"},
		{Port: 5432, Project: "shared", Label: "postgres", Listening: []string{"127.0.0.1"}, User: "postgres", Status: "ok"},
		{Port: 5672, Project: "billing", Label: "rabbitmq", Listening: []string{"127.0.0.1"}, User: "rabbitmq", Status: "ok"},
		{Port: 6379, Project: "shared", Label: "redis", Listening: []string{"127.0.0.1", "::1"}, User: "redis", Status: "ok"},
		{Port: 7300, Project: "engine", Label: "gRPC", Listening: []string{"127.0.0.1"}, User: "deploy", Status: "ok"},
		{Port: 8300, Project: "engine", Label: "dashboard", Listening: []string{"127.0.0.1"}, User: "deploy", Status: "ok"},
		{Port: 8400, Project: "vps-dashboard", Label: "this dashboard", Listening: []string{"127.0.0.1"}, User: "vpsdash", Status: "ok"},
		{Port: 9100, Project: "engine", Label: "metrics", Status: "down"},
	}
	for _, p := range st.Ports {
		for i := range st.Projects {
			if st.Projects[i].ID == p.Project {
				st.Projects[i].Ports = append(st.Projects[i].Ports, p)
			}
		}
	}
	for i := range st.Projects {
		for _, u := range st.Projects[i].Units {
			st.Projects[i].CPUPercent += u.CPUPercent
			st.Projects[i].MemoryBytes += u.MemoryBytes
		}
	}

	certFor := func(host, project string, days float64) state.Cert {
		return state.Cert{Host: host, Project: project, Subject: host, Issuer: "E6", Names: []string{host}, NotAfter: now.Add(time.Duration(days * float64(day))),
			DaysLeft: days, Valid: true, Fingerprint: fmt.Sprintf("%x", len(host)*7919)}
	}
	st.TLS = []state.Cert{
		certFor("shop.example.com", "storefront", 74), certFor("api.shop.example.com", "storefront", 74),
		certFor("billing.example.com", "billing", 16.4), certFor("engine.example.com", "engine", 61),
	}

	site := func(name, project string, base int, seed float64, codes [4]int) state.NginxSite {
		ph := hourly(base, seed)
		return state.NginxSite{Site: name, Project: project, Log: "/var/log/nginx/access.log", PerHour: ph, Total24: sum(ph), Status: codes, LastSeen: now.Add(-time.Minute)}
	}
	st.Nginx = []state.NginxSite{
		site("shop.example.com", "storefront", 140, 0, [4]int{2900, 210, 160, 2}),
		site("api.shop.example.com", "storefront", 90, 0.7, [4]int{1950, 0, 120, 6}),
		site("engine.example.com", "engine", 30, 2, [4]int{640, 12, 48, 0}),
		site("billing.example.com", "billing", 12, 1, [4]int{250, 0, 30, 0}),
	}
	other := site("access.log", "", 25, 3, [4]int{300, 40, 260, 0})
	other.NoHost = true
	st.Nginx = append(st.Nginx, other)

	ef := func(project, path, owner, mode string, age time.Duration, v []state.EnvVar, used ...string) state.EnvFile {
		return state.EnvFile{FileMeta: state.FileMeta{Project: project, Path: path, Exists: true, Owner: owner, Group: owner, Mode: mode, Size: 900, ModTime: now.Add(-age)},
			Vars: v, ExpectOwner: "deploy", ExpectMode: "0600", UsedBy: used}
	}
	st.EnvFiles = []state.EnvFile{
		ef("storefront", "/opt/storefront/api/.env", "deploy", "0600", 3*day,
			vars("HOST", "PORT", "DATABASE_URL", "REDIS_URL", "JWT_SECRET", "JWT_REFRESH_SECRET", "STRIPE_SECRET_KEY", "-SENTRY_DSN", "RESEND_API_KEY", "EMAIL_FROM"), "storefront-api.service"),
		ef("billing", "/opt/billing/.env", "deploy", "0640", 9*day,
			vars("HOST", "DATABASE_URL", "RABBITMQ_URL", "BILLING_WEBHOOK_SECRET", "-STRIPE_SECRET_KEY", "INTERNAL_API_TOKEN"), "billing-api.service", "billing-worker.service"),
		{FileMeta: state.FileMeta{Project: "engine", Path: "/etc/engine.token", Exists: true, Owner: "root", Group: "deploy", Mode: "0640", Size: 65, ModTime: now.Add(-day)}, SecretFile: true},
	}
	st.EnvCopies = []state.FileMeta{
		{Project: "storefront", Path: "/opt/storefront/api/.env.bak-20261018", Exists: true, Owner: "deploy", Group: "deploy", Mode: "0600", Size: 870, ModTime: now.Add(-5 * day)},
	}
	st.Alerts = []state.Alert{
		{Level: "warning", Project: "billing", Area: "deploy", Message: "deployed commit is 3 behind main"},
		{Level: "warning", Project: "billing", Area: "backups", Message: "Postgres nightly: newest backup is 57.0h old"},
		{Level: "warning", Project: "billing", Area: "units", Message: "billing-worker.service was restarted 2 time(s) by systemd since it started"},
		{Level: "warning", Project: "billing", Area: "tls", Message: "billing.example.com certificate expires in 16 days"},
		{Level: "warning", Project: "billing", Area: "env", Message: "/opt/billing/.env has mode 0640, expected 0600"},
		{Level: "info", Project: "storefront", Area: "logs", Message: "3 error line(s) in the last hour"},
		{Level: "info", Area: "env", Message: "1 copies of env files sit on disk (backups from past edits); each holds old secrets"},
	}
	for i, p := range st.Projects {
		status := "ok"
		for _, a := range st.Alerts {
			if a.Project == p.ID && a.Level == "warning" {
				status = "warn"
			}
		}
		st.Projects[i].Status = status
	}
	return st
}
