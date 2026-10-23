// Package collect runs the unprivileged collectors on their own schedules and
// assembles the dashboard's state. It reads secrets from nowhere: env names,
// backups and logs arrive through the probe's snapshot file.
package collect

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/checks"
	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/deploy"
	"github.com/olayanju-1234/vps-dashboard/internal/fsmeta"
	"github.com/olayanju-1234/vps-dashboard/internal/github"
	"github.com/olayanju-1234/vps-dashboard/internal/host"
	"github.com/olayanju-1234/vps-dashboard/internal/netstat"
	"github.com/olayanju-1234/vps-dashboard/internal/probe"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
	"github.com/olayanju-1234/vps-dashboard/internal/systemd"
)

// SamplerSize at the default 10 s tick is six hours.
const SamplerSize = 2160

// Collector owns every serve-side reader and the latest result of each.
type Collector struct {
	cfg     *config.Config
	version string
	log     *slog.Logger

	sampler *host.Sampler
	sysstat *host.Sysstat
	units   *systemd.Poller
	http    *checks.HTTP
	deploy  *deploy.Detector
	roots   *x509.CertPool
	now     func() time.Time

	mu        sync.RWMutex
	unitState map[string]state.Unit
	unitErr   string
	listeners []netstat.Listener
	listenErr string
	checks    map[string]state.Check
	certs     []state.Cert
	deploys   map[string]state.Deploy
	statics   map[string][]state.StaticSite
	snap      state.Snapshot
	snapMod   time.Time
	snapErr   string
	hostErr   string
}

// New builds a collector for the config.
func New(cfg *config.Config, version string, log *slog.Logger) *Collector {
	return &Collector{
		cfg: cfg, version: version, log: log,
		sampler: host.NewSampler(host.Proc{Root: cfg.ProcRoot}, cfg.Disks, SamplerSize),
		sysstat: host.NewSysstat(cfg.SysstatDir, cfg.Sadf),
		units:   systemd.NewPoller(cfg.Systemctl),
		http:    checks.NewHTTP(cfg.UserAgent),
		deploy:  deploy.New(github.New(cfg.GitHubAPI, cfg.UserAgent)),
		now:     time.Now,
		checks:  map[string]state.Check{}, deploys: map[string]state.Deploy{}, statics: map[string][]state.StaticSite{},
	}
}

// Run refreshes each group now and then on its own interval.
func (c *Collector) Run(ctx context.Context) {
	var wg sync.WaitGroup
	loop := func(every time.Duration, f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(ctx)
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					f(ctx)
				}
			}
		}()
	}
	loop(c.cfg.Intervals.Fast.Duration, c.Fast)
	loop(c.cfg.Intervals.Checks.Duration, c.Checks)
	loop(c.cfg.Intervals.Slow.Duration, c.Slow)
	wg.Wait()
}

// Fast reads host counters, units, listeners and the probe snapshot.
func (c *Collector) Fast(ctx context.Context) {
	hostErr := ""
	if err := c.sampler.Tick(c.now()); err != nil {
		hostErr = err.Error()
		c.log.Warn("collect: host sample", "err", err)
	}
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	units, uerr := c.units.Poll(tctx, c.allUnits())
	cancel()
	ls, lerr := netstat.Listeners(c.cfg.ProcRoot)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.hostErr = hostErr
	if uerr != nil {
		c.unitErr = uerr.Error()
		c.log.Warn("collect: units", "err", uerr)
	} else {
		c.unitState, c.unitErr = units, ""
	}
	if lerr != nil {
		c.listenErr = lerr.Error()
	} else {
		c.listeners, c.listenErr = ls, ""
	}
	c.loadSnapshotLocked()
}

func (c *Collector) loadSnapshotLocked() {
	fi, err := os.Stat(c.cfg.SnapshotPath)
	if err != nil {
		c.snapErr = fmt.Sprintf("no probe snapshot: %v", err)
		return
	}
	if fi.ModTime().Equal(c.snapMod) {
		return
	}
	snap, err := probe.Read(c.cfg.SnapshotPath)
	if err != nil {
		c.snapErr = err.Error()
		return
	}
	c.snap, c.snapMod, c.snapErr = snap, fi.ModTime(), ""
}

// Checks probes every configured URL in parallel.
func (c *Collector) Checks(ctx context.Context) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := map[string]state.Check{}
	for _, p := range c.cfg.Projects {
		for _, spec := range p.Checks {
			wg.Add(1)
			go func(spec config.CheckSpec) {
				defer wg.Done()
				r := c.http.Check(ctx, spec.URL, spec.Expect)
				mu.Lock()
				results[spec.URL] = r
				mu.Unlock()
			}(spec)
		}
	}
	wg.Wait()
	c.mu.Lock()
	c.checks = results
	c.mu.Unlock()
}

// Slow works out deployed commits, certificate expiry and static-site ages.
func (c *Collector) Slow(ctx context.Context) {
	deploys := map[string]state.Deploy{}
	statics := map[string][]state.StaticSite{}
	for _, p := range c.cfg.Projects {
		dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		deploys[p.ID] = c.deploy.Detect(dctx, p.Deploy)
		cancel()
		for _, s := range p.Statics {
			st := state.StaticSite{Label: s.Label, Path: s.Path}
			for _, path := range []string{s.Path, filepath.Join(s.Path, "index.html")} {
				t, err := fsmeta.Changed(path)
				if err != nil {
					if path == s.Path {
						st.Error = err.Error()
					}
					continue
				}
				if t.After(st.DeployedAt) {
					st.DeployedAt = t
				}
			}
			statics[p.ID] = append(statics[p.ID], st)
		}
	}
	var certs []state.Cert
	for _, p := range c.cfg.Projects {
		for _, h := range p.Hosts {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			cert := checks.Cert(cctx, c.cfg.TLSAddr, h, c.roots, c.now())
			cancel()
			cert.Project = p.ID
			certs = append(certs, cert)
		}
	}
	c.mu.Lock()
	c.deploys, c.statics, c.certs = deploys, statics, certs
	c.mu.Unlock()
}

func (c *Collector) allUnits() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range c.cfg.Projects {
		for _, u := range p.Units {
			u = systemd.Canonical(u)
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// History returns host samples for a range: 1h and 6h from the in-memory
// sampler, 24h and 7d from sysstat.
func (c *Collector) History(ctx context.Context, rng string) (state.History, error) {
	now := c.now()
	h := state.History{Range: rng}
	switch rng {
	case "1h", "6h":
		d := time.Hour
		if rng == "6h" {
			d = 6 * time.Hour
		}
		h.Source, h.Step = "sampler", int(c.cfg.Intervals.Fast.Seconds())
		h.Samples = c.sampler.Since(now.Add(-d))
		if rng == "6h" {
			h.Samples, h.Step = Downsample(h.Samples, 60), 60
		}
		return h, nil
	case "24h", "7d":
		d := 24 * time.Hour
		if rng == "7d" {
			d = 7 * 24 * time.Hour
		}
		h.Source, h.Step = "sysstat", 600
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		samples, err := c.sysstat.Since(sctx, now.Add(-d))
		if err != nil {
			// Fall back to whatever the sampler has.
			h.Error = err.Error()
			h.Source, h.Step = "sampler", 60
			h.Samples = Downsample(c.sampler.Since(now.Add(-d)), 60)
			return h, nil
		}
		h.Samples = samples
		return h, nil
	}
	return h, fmt.Errorf("collect: unknown range %q", rng)
}

// Downsample averages samples into buckets of step seconds.
func Downsample(in []state.Sample, step int64) []state.Sample {
	if len(in) == 0 || step <= 0 {
		return in
	}
	var out []state.Sample
	var acc state.Sample
	n := 0.0
	bucket := in[0].T / step
	flush := func() {
		if n > 0 {
			out = append(out, state.Sample{T: bucket * step, CPU: round1(acc.CPU / n), MemUsedPct: round1(acc.MemUsedPct / n), SwapPct: round1(acc.SwapPct / n)})
		}
		acc, n = state.Sample{}, 0
	}
	for _, s := range in {
		if b := s.T / step; b != bucket {
			flush()
			bucket = b
		}
		acc.CPU += s.CPU
		acc.MemUsedPct += s.MemUsedPct
		acc.SwapPct += s.SwapPct
		n++
	}
	flush()
	return out
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// ErrUnknownProject is returned for a project id not in the config.
var ErrUnknownProject = errors.New("collect: unknown project")

// Journal returns one project's recent problem lines.
func (c *Collector) Journal(project string) (state.ProjectJournal, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, j := range c.snap.Journal {
		if j.Project == project {
			return j, nil
		}
	}
	for _, p := range c.cfg.Projects {
		if p.ID == project {
			return state.ProjectJournal{Project: project, PerHour: make([]int, 24)}, nil
		}
	}
	return state.ProjectJournal{}, ErrUnknownProject
}

// State assembles the current view.
func (c *Collector) State() state.State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.now()
	s := state.State{GeneratedAt: now.UTC(), Version: c.version, Host: c.sampler.Now()}

	probeAge := now.Sub(c.snap.GeneratedAt).Seconds()
	s.Probe = state.ProbeInfo{GeneratedAt: c.snap.GeneratedAt, AgeSeconds: probeAge, Error: c.snapErr, Errors: c.snap.Errors,
		Stale: c.snap.GeneratedAt.IsZero() || probeAge > 3*c.cfg.Intervals.Probe.Seconds()+30}

	usedBy := map[string][]string{}
	for _, u := range c.unitState {
		for _, f := range u.EnvFiles {
			usedBy[f] = append(usedBy[f], u.Name)
		}
	}
	for _, ef := range c.snap.EnvFiles {
		ef.UsedBy = usedBy[ef.Path]
		sort.Strings(ef.UsedBy)
		s.EnvFiles = append(s.EnvFiles, ef)
	}
	s.EnvCopies = c.snap.EnvCopies
	s.Nginx = c.snap.Nginx
	s.TLS = c.certs
	s.Ports = c.portsLocked()

	for _, pc := range c.cfg.Projects {
		s.Projects = append(s.Projects, c.projectLocked(pc, s.Ports, now))
	}
	s.Alerts = c.alertsLocked(&s, now)
	for i := range s.Projects {
		s.Projects[i].Status = projectStatus(s.Projects[i], s.Alerts)
	}
	return s
}

func (c *Collector) projectLocked(pc config.Project, ports []state.Port, now time.Time) state.Project {
	p := state.Project{ID: pc.ID, Name: pc.Name, Description: pc.Description, Repo: pc.Deploy.Repo}
	for _, name := range pc.Units {
		u, ok := c.unitState[systemd.Canonical(name)]
		if !ok {
			u = state.Unit{Name: systemd.Canonical(name), Load: "unknown", Active: "unknown"}
		}
		p.CPUPercent += u.CPUPercent
		p.MemoryBytes += u.MemoryBytes
		p.Units = append(p.Units, u)
	}
	for _, pt := range ports {
		if pt.Project == pc.ID {
			p.Ports = append(p.Ports, pt)
		}
	}
	for _, spec := range pc.Checks {
		ch, ok := c.checks[spec.URL]
		if !ok {
			ch = state.Check{URL: spec.URL, Expect: spec.Expect}
		}
		p.Checks = append(p.Checks, ch)
	}
	p.Deploy = c.deploys[pc.ID]
	if p.Deploy.Method == "" {
		p.Deploy = state.Deploy{Method: pc.Deploy.Method, Behind: -1, Status: "unknown"}
	}
	if last := latestStart(p.Units); !last.IsZero() && p.Deploy.DeployedAt.After(last.Add(2*time.Minute)) {
		p.Deploy.NeedsRestart = true
	}
	p.Statics = c.statics[pc.ID]
	for _, b := range c.snap.Backups {
		if b.Project == pc.ID {
			p.Backups = append(p.Backups, b)
		}
	}
	p.Journal = state.ProjectJournal{Project: pc.ID, PerHour: make([]int, 24)}
	for _, j := range c.snap.Journal {
		if j.Project == pc.ID {
			p.Journal = j
			p.Journal.Lines = nil // served separately: /api/journal
		}
	}
	for _, ef := range pc.EnvFiles {
		p.EnvFiles = append(p.EnvFiles, ef.Path)
	}
	return p
}

// latestStart is the most recent start of any running service. Using the
// latest, not the earliest, keeps shared services (RabbitMQ) and parts that a
// deploy didn't touch from flagging every deploy as unrestarted.
func latestStart(units []state.Unit) time.Time {
	var t time.Time
	for _, u := range units {
		if u.Sub == "running" && u.ActiveSince.After(t) {
			t = u.ActiveSince
		}
	}
	return t
}

func (c *Collector) portsLocked() []state.Port {
	specs := map[int]config.PortSpec{}
	for _, p := range c.cfg.Ports {
		specs[p.Port] = p
	}
	for _, proj := range c.cfg.Projects {
		for _, p := range proj.Ports {
			p.Project = proj.ID
			specs[p.Port] = p
		}
	}
	if port, err := strconv.Atoi(c.cfg.ListenPort()); err == nil {
		if _, ok := specs[port]; !ok {
			specs[port] = config.PortSpec{Port: port, Project: "vps-dashboard", Label: "this dashboard"}
		}
	}
	byPort := map[int][]netstat.Listener{}
	for _, l := range c.listeners {
		byPort[l.Port] = append(byPort[l.Port], l)
	}
	var out []state.Port
	for port, sp := range specs {
		p := state.Port{Port: port, Project: sp.Project, Label: sp.Label, Public: sp.Public, Status: "down"}
		exposed := false
		for _, l := range byPort[port] {
			p.Listening = appendUnique(p.Listening, l.IP.String())
			p.User = fsmeta.UserName(l.UID)
			if !l.Loopback() {
				exposed = true
			}
		}
		switch {
		case len(p.Listening) == 0:
		case exposed && !sp.Public:
			p.Status = "exposed"
		default:
			p.Status = "ok"
		}
		out = append(out, p)
	}
	for port, ls := range byPort {
		if _, ok := specs[port]; ok {
			continue
		}
		p := state.Port{Port: port, Status: "unlisted"}
		for _, l := range ls {
			p.Listening = appendUnique(p.Listening, l.IP.String())
			p.User = fsmeta.UserName(l.UID)
			if !l.Loopback() {
				p.Status = "unexpected"
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func appendUnique(ss []string, s string) []string {
	for _, x := range ss {
		if x == s {
			return ss
		}
	}
	return append(ss, s)
}
