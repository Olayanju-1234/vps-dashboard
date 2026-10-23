// Package nginxlog counts requests per site from nginx access logs. It reads
// each log incrementally and only keeps hourly counters, never request lines:
// paths and query strings can carry tokens.
package nginxlog

import (
	"bufio"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Hours is how much history is kept and reported.
const Hours = 24

// Source is one log file. Label names the bucket for lines with no host field.
type Source struct {
	Path  string
	Label string
}

// combined plus an optional trailing "$host" (and anything after it).
var lineRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([^"]*)" (\d{3}) (\S+) "([^"]*)" "([^"]*)"(?: "([^"]*)")?`)

// Tracker keeps hourly counts per site.
type Tracker struct {
	sources []*tail
	skipUA  string
	hosts   map[string]string // host -> project
	sites   map[string]*site
	seen    map[uint64]int64 // combined-part hash -> unix time, from logs with a host
	now     func() time.Time
}

type tail struct {
	Source
	ino    uint64
	offset int64
	primed bool
}

type site struct {
	state.NginxSite
	hours map[int64]*[4]int // hour start (unix) -> status classes
}

// New tracks the given logs. Logs listed first should be the ones with a host
// field, so their lines can be recognised when they also land in a shared log.
func New(sources []Source, hosts map[string]string, skipUA string) *Tracker {
	t := &Tracker{skipUA: skipUA, hosts: hosts, sites: map[string]*site{}, seen: map[uint64]int64{}, now: time.Now}
	for _, s := range sources {
		t.sources = append(t.sources, &tail{Source: s})
	}
	return t
}

// Update reads whatever was appended since the last call. The first call also
// reads the rotated ".1" file so the 24 h window starts full.
func (t *Tracker) Update() []error {
	var errs []error
	for _, s := range t.sources {
		if err := t.update(s); err != nil {
			errs = append(errs, err)
		}
	}
	t.prune()
	return errs
}

func (t *Tracker) update(s *tail) error {
	fi, err := os.Stat(s.Path)
	if err != nil {
		return fmt.Errorf("nginxlog: %w", err)
	}
	ino := inode(fi)
	if !s.primed {
		s.primed = true
		if err := t.readFrom(s, s.Path+".1", 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		s.ino, s.offset = ino, 0
	}
	if ino != s.ino {
		// Rotated: finish the old file (now .1) if it is the one we were reading.
		if old, err := os.Stat(s.Path + ".1"); err == nil && inode(old) == s.ino {
			if err := t.readFrom(s, s.Path+".1", s.offset); err != nil {
				return err
			}
		}
		s.ino, s.offset = ino, 0
	}
	if fi.Size() < s.offset {
		s.offset = 0 // truncated in place
	}
	n, err := t.read(s, s.Path, s.offset)
	s.offset += n
	return err
}

func (t *Tracker) readFrom(s *tail, path string, offset int64) error {
	_, err := t.read(s, path, offset)
	return err
}

func (t *Tracker) read(s *tail, path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("nginxlog: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("nginxlog: seek %s: %w", path, err)
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var consumed int64
	cutoff := t.now().Add(-Hours * time.Hour).Unix()
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			// A partial last line is left for the next read.
			return consumed, nil
		}
		if err != nil {
			return consumed, fmt.Errorf("nginxlog: read %s: %w", path, err)
		}
		consumed += int64(len(line))
		t.add(s, line, cutoff)
	}
}

func (t *Tracker) add(s *tail, line string, cutoff int64) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	if t.skipUA != "" && strings.Contains(m[7], t.skipUA) {
		return
	}
	ts, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[2])
	if err != nil || ts.Unix() < cutoff {
		return
	}
	code, _ := strconv.Atoi(m[4])
	key := hash(m[1], m[2], m[3], m[4], m[5], m[6], m[7])
	var name, project string
	noHost := m[8] == ""
	if noHost {
		if _, dup := t.seen[key]; dup {
			return // already counted under its host from a log that has one
		}
		name = s.Label
	} else {
		t.seen[key] = ts.Unix()
		name = strings.ToLower(m[8])
		if h, _, ok := strings.Cut(name, ":"); ok {
			name = h
		}
		project = t.hosts[name]
	}
	st, ok := t.sites[name]
	if !ok {
		st = &site{NginxSite: state.NginxSite{Site: name, Project: project, Log: s.Path, NoHost: noHost}, hours: map[int64]*[4]int{}}
		t.sites[name] = st
	}
	hour := ts.Unix() / 3600 * 3600
	b, ok := st.hours[hour]
	if !ok {
		b = &[4]int{}
		st.hours[hour] = b
	}
	b[class(code)]++
	if ts.After(st.LastSeen) {
		st.LastSeen = ts.UTC()
	}
}

func class(code int) int {
	switch {
	case code < 300:
		return 0
	case code < 400:
		return 1
	case code < 500:
		return 2
	}
	return 3
}

func hash(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

func (t *Tracker) prune() {
	cutoff := t.now().Add(-(Hours + 1) * time.Hour).Unix()
	for k, ts := range t.seen {
		if ts < cutoff {
			delete(t.seen, k)
		}
	}
	for name, st := range t.sites {
		for h := range st.hours {
			if h < cutoff {
				delete(st.hours, h)
			}
		}
		if len(st.hours) == 0 {
			delete(t.sites, name)
		}
	}
}

// Sites returns per-site totals for the last 24 h, busiest first.
func (t *Tracker) Sites() []state.NginxSite {
	now := t.now().Unix() / 3600 * 3600
	start := now - (Hours-1)*3600
	out := make([]state.NginxSite, 0, len(t.sites))
	for _, st := range t.sites {
		s := st.NginxSite
		s.PerHour = make([]int, Hours)
		s.Status = [4]int{}
		s.Total24 = 0
		for h, b := range st.hours {
			if h < start || h > now {
				continue
			}
			n := b[0] + b[1] + b[2] + b[3]
			s.PerHour[(h-start)/3600] += n
			s.Total24 += n
			for i := range b {
				s.Status[i] += b[i]
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total24 != out[j].Total24 {
			return out[i].Total24 > out[j].Total24
		}
		return out[i].Site < out[j].Site
	})
	return out
}

func inode(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
