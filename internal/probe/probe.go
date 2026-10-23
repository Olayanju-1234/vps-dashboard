// Package probe is the privileged half. It runs with CAP_DAC_READ_SEARCH (read
// any file) and no network, and writes one JSON snapshot holding env names,
// backup ages, redacted journal lines and nginx counts. Values never leave it.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/envfile"
	"github.com/olayanju-1234/vps-dashboard/internal/fsmeta"
	"github.com/olayanju-1234/vps-dashboard/internal/journal"
	"github.com/olayanju-1234/vps-dashboard/internal/nginxlog"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
	"github.com/olayanju-1234/vps-dashboard/internal/systemd"
)

// Probe holds the incremental readers between cycles.
type Probe struct {
	cfg     *config.Config
	version string
	log     *slog.Logger
	journal *journal.Collector
	nginx   *nginxlog.Tracker
	now     func() time.Time
}

// New builds a probe from the config.
func New(cfg *config.Config, version string, log *slog.Logger) *Probe {
	units := map[string]string{}
	hosts := map[string]string{}
	for _, p := range cfg.Projects {
		for _, u := range p.Units {
			units[systemd.Canonical(u)] = p.ID
		}
		for _, h := range p.Hosts {
			hosts[strings.ToLower(h)] = p.ID
		}
	}
	var sources []nginxlog.Source
	for _, l := range cfg.Nginx {
		label := l.Label
		if label == "" {
			label = filepath.Base(l.Path)
		}
		sources = append(sources, nginxlog.Source{Path: l.Path, Label: label})
	}
	return &Probe{
		cfg: cfg, version: version, log: log, now: time.Now,
		journal: journal.New(cfg.Journalctl, units),
		nginx:   nginxlog.New(sources, hosts, cfg.UserAgent),
	}
}

// Run writes a snapshot every interval until ctx ends.
func (p *Probe) Run(ctx context.Context) error {
	t := time.NewTicker(p.cfg.Intervals.Probe.Duration)
	defer t.Stop()
	for {
		snap := p.Cycle(ctx)
		if err := Write(p.cfg.SnapshotPath, snap); err != nil {
			p.log.Error("probe: write snapshot", "path", p.cfg.SnapshotPath, "err", err)
		} else {
			p.log.Debug("probe: snapshot written", "env_files", len(snap.EnvFiles), "errors", len(snap.Errors))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Cycle collects one snapshot.
func (p *Probe) Cycle(ctx context.Context) state.Snapshot {
	snap := state.Snapshot{GeneratedAt: p.now().UTC(), Version: p.version}
	fail := func(err error) {
		snap.Errors = append(snap.Errors, err.Error())
		p.log.Warn("probe: collector failed", "err", err)
	}

	// The scrubber lives only for this cycle: env values never outlast it.
	var sb envfile.ScrubBuilder
	snap.EnvFiles = p.envFiles(&sb)
	scrub := sb.Build()
	jctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := p.journal.Collect(jctx, scrub); err != nil {
		fail(err)
	}
	cancel()
	snap.Journal = p.journal.Results()

	for _, err := range p.nginx.Update() {
		fail(err)
	}
	snap.Nginx = p.nginx.Sites()
	snap.Backups = p.backups()
	snap.EnvCopies = p.envCopies()
	return snap
}

func (p *Probe) envFiles(sb *envfile.ScrubBuilder) []state.EnvFile {
	var out []state.EnvFile
	for _, proj := range p.cfg.Projects {
		for _, spec := range proj.EnvFiles {
			ef := state.EnvFile{FileMeta: fsmeta.Stat(spec.Path), SecretFile: spec.Secret, ExpectOwner: spec.ExpectOwner, ExpectMode: spec.ExpectMode}
			ef.Project = proj.ID
			if !ef.Exists || spec.Secret {
				// A secret file is one value; its size says whether it is set.
				out = append(out, ef)
				continue
			}
			res, err := parseFile(spec.Path, sb)
			if err != nil {
				ef.Error = err.Error()
			}
			ef.Vars, ef.Unparsed = res.Vars, res.Unparsed
			out = append(out, ef)
		}
	}
	return out
}

func parseFile(path string, sb *envfile.ScrubBuilder) (envfile.Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return envfile.Result{}, fmt.Errorf("probe: open env file: %w", err)
	}
	defer f.Close()
	return envfile.Parse(f, sb)
}

func (p *Probe) envCopies() []state.FileMeta {
	known := map[string]bool{}
	for _, proj := range p.cfg.Projects {
		for _, spec := range proj.EnvFiles {
			known[spec.Path] = true
		}
	}
	var out []state.FileMeta
	for _, proj := range p.cfg.Projects {
		for _, pattern := range proj.EnvCopies {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			for _, m := range matches {
				if known[m] || isTemplate(m) {
					continue
				}
				known[m] = true
				meta := fsmeta.Stat(m)
				meta.Project = proj.ID
				out = append(out, meta)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// isTemplate skips .env.example and friends, which hold no secrets.
func isTemplate(path string) bool {
	for _, suffix := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func (p *Probe) backups() []state.Backup {
	var out []state.Backup
	now := p.now()
	for _, proj := range p.cfg.Projects {
		for _, spec := range proj.Backups {
			b := state.Backup{Project: proj.ID, Label: spec.Label, Dir: spec.Dir, Glob: spec.Glob, MaxAge: spec.MaxAge.String()}
			entries, err := os.ReadDir(spec.Dir)
			if err != nil {
				b.Status, b.Error = "error", fmt.Sprintf("probe: read backup dir: %v", err)
				out = append(out, b)
				continue
			}
			for _, e := range entries {
				if ok, _ := filepath.Match(spec.Glob, e.Name()); !ok || !e.Type().IsRegular() {
					continue
				}
				fi, err := e.Info()
				if err != nil {
					continue
				}
				b.Count++
				b.TotalBytes += fi.Size()
				if fi.ModTime().After(b.NewestTime) {
					b.Newest, b.NewestBytes, b.NewestTime = e.Name(), fi.Size(), fi.ModTime().UTC()
				}
			}
			switch {
			case b.Count == 0:
				b.Status = "missing"
			case now.Sub(b.NewestTime) > spec.MaxAge.Duration:
				b.Status = "stale"
			case b.NewestBytes == 0:
				b.Status = "stale"
			default:
				b.Status = "ok"
			}
			out = append(out, b)
		}
	}
	return out
}

// Write replaces the snapshot atomically, readable by the dashboard's group only.
func Write(path string, snap state.Snapshot) error {
	b, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("probe: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".probe-*.json")
	if err != nil {
		return fmt.Errorf("probe: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("probe: write: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return fmt.Errorf("probe: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("probe: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("probe: rename: %w", err)
	}
	return nil
}

// Read loads a snapshot written by Write.
func Read(path string) (state.Snapshot, error) {
	var snap state.Snapshot
	b, err := os.ReadFile(path)
	if err != nil {
		return snap, fmt.Errorf("probe: read snapshot: %w", err)
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		return snap, fmt.Errorf("probe: decode snapshot: %w", err)
	}
	return snap, nil
}
