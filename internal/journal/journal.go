// Package journal keeps the last day of error and warning lines for each
// project's units. Every line is scrubbed of known env values and redacted
// before it is stored, so nothing secret reaches the snapshot.
package journal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olayanju-1234/vps-dashboard/internal/envfile"
	"github.com/olayanju-1234/vps-dashboard/internal/redact"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

const (
	keepLines  = 200
	maxMessage = 600
	window     = 24 * time.Hour
)

// Fields asked of journalctl; __CURSOR and __REALTIME_TIMESTAMP always come along.
var Fields = "MESSAGE,PRIORITY,_SYSTEMD_UNIT,UNIT,_PID"

// Opener starts journalctl and returns its stdout.
type Opener func(ctx context.Context, args []string) (io.ReadCloser, func() error, error)

// Collector reads new journal entries on each call to Collect.
type Collector struct {
	units  map[string]string // unit -> project
	open   Opener
	cursor string
	now    func() time.Time
	byProj map[string]*project
	ignore map[string][]*regexp.Regexp // project -> lines to drop
}

// Ignore drops a project's lines that match any of the patterns (already validated).
func (c *Collector) Ignore(project string, patterns []string) {
	if c.ignore == nil {
		c.ignore = map[string][]*regexp.Regexp{}
	}
	for _, p := range patterns {
		c.ignore[project] = append(c.ignore[project], regexp.MustCompile(p))
	}
}

type project struct {
	lines []state.JournalLine
	hours map[int64]*[2]int // hour -> errors, warnings
}

// New watches the given units, mapped to their projects.
func New(journalctl string, units map[string]string) *Collector {
	c := &Collector{units: units, now: time.Now, byProj: map[string]*project{}}
	c.open = func(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
		cmd := exec.CommandContext(ctx, journalctl, args...)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, fmt.Errorf("journal: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, fmt.Errorf("journal: start %s: %w", journalctl, err)
		}
		return out, cmd.Wait, nil
	}
	for _, p := range units {
		c.proj(p)
	}
	return c
}

func (c *Collector) proj(id string) *project {
	p, ok := c.byProj[id]
	if !ok {
		p = &project{hours: map[int64]*[2]int{}}
		c.byProj[id] = p
	}
	return p
}

type entry struct {
	Cursor   string          `json:"__CURSOR"`
	Realtime string          `json:"__REALTIME_TIMESTAMP"`
	Message  json.RawMessage `json:"MESSAGE"`
	Priority string          `json:"PRIORITY"`
	Unit     string          `json:"_SYSTEMD_UNIT"`
	SysUnit  string          `json:"UNIT"` // set on systemd's own messages about a unit
	PID      string          `json:"_PID"`
}

// Collect reads entries since the last call (or the last 24 h on the first).
// The scrubber is used and dropped; the collector never keeps it.
func (c *Collector) Collect(ctx context.Context, scrub *envfile.Scrubber) error {
	if len(c.units) == 0 {
		return nil
	}
	args := []string{"--no-pager", "--quiet", "-o", "json", "--output-fields=" + Fields}
	if c.cursor == "" {
		args = append(args, "--since=-24h")
	} else {
		args = append(args, "--after-cursor="+c.cursor)
	}
	names := make([]string, 0, len(c.units))
	for u := range c.units {
		names = append(names, u)
	}
	sort.Strings(names)
	for _, u := range names {
		args = append(args, "-u", u)
	}
	out, wait, err := c.open(ctx, args)
	if err != nil {
		return err
	}
	readErr := c.read(out, scrub)
	_ = out.Close()
	waitErr := wait()
	c.prune()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return fmt.Errorf("journal: journalctl: %w", waitErr)
	}
	return nil
}

func (c *Collector) read(r io.Reader, scrub *envfile.Scrubber) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 256*1024), 4<<20)
	for sc.Scan() {
		var e entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if e.Cursor != "" {
			c.cursor = e.Cursor
		}
		unit := e.Unit
		// Any process may send UNIT=; only PID 1's messages speak for another unit.
		if e.SysUnit != "" && e.PID == "1" {
			unit = e.SysUnit
		}
		projID, ok := c.units[unit]
		if !ok {
			continue
		}
		msg := message(e.Message)
		if c.ignored(projID, msg) {
			continue
		}
		prio, err := strconv.Atoi(e.Priority)
		if err != nil {
			prio = 6
		}
		level, ok := Classify(prio, msg)
		if !ok {
			continue
		}
		usec, _ := strconv.ParseInt(e.Realtime, 10, 64)
		at := time.UnixMicro(usec).UTC()
		c.add(projID, state.JournalLine{Time: at, Unit: unit, Level: level, Message: clean(msg, scrub)})
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("journal: read: %w", err)
	}
	return nil
}

func (c *Collector) ignored(project, msg string) bool {
	for _, re := range c.ignore[project] {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

// message decodes MESSAGE, which journald emits as a byte array when it is not UTF-8.
func message(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var b []byte
	var ints []int
	if err := json.Unmarshal(raw, &ints); err == nil {
		for _, i := range ints {
			b = append(b, byte(i))
		}
		return strings.ToValidUTF8(string(b), "?")
	}
	return ""
}

func clean(msg string, scrub *envfile.Scrubber) string {
	msg = redact.Line(scrub.Scrub(msg))
	if utf8.RuneCountInString(msg) > maxMessage {
		r := []rune(msg)
		msg = string(r[:maxMessage]) + "…"
	}
	return msg
}

var (
	errRe    = regexp.MustCompile(`(?i:\blevel[=:]\s*"?(error|fatal|panic|crit)|"level":\s*(50|60)\b|uncaught exception|unhandled (promise )?rejection|traceback \(most recent call last\)|segmentation fault|out of memory|failed with result|start request repeated too quickly)|\b(ERROR|FATAL|CRITICAL)\b|\bError:|\bpanic:`)
	warnRe   = regexp.MustCompile(`(?i:\blevel[=:]\s*"?warn|"level":\s*40\b|scheduled restart job)|\bWARN(ING)?\b`)
	exitedRe = regexp.MustCompile(`Main process exited, code=(\w+), status=([^ ]+)`)
)

// Classify decides whether a line is an error, a warning, or neither. Apps
// log to stdout at priority 6, so their own level markers matter as much as
// journald's priority.
func Classify(priority int, msg string) (string, bool) {
	if m := exitedRe.FindStringSubmatch(msg); m != nil {
		// A SIGTERM from a normal stop or deploy restart is not a failure.
		if (m[1] == "killed" && strings.HasPrefix(m[2], "15/")) || m[2] == "0/SUCCESS" || strings.HasPrefix(m[2], "143") {
			return "", false
		}
		return "error", true
	}
	switch {
	case priority <= 3 || errRe.MatchString(msg):
		return "error", true
	case priority == 4 || warnRe.MatchString(msg):
		return "warning", true
	}
	return "", false
}

func (c *Collector) add(projID string, l state.JournalLine) {
	p := c.proj(projID)
	hour := l.Time.Unix() / 3600 * 3600
	b, ok := p.hours[hour]
	if !ok {
		b = &[2]int{}
		p.hours[hour] = b
	}
	if l.Level == "error" {
		b[0]++
	} else {
		b[1]++
	}
	// Collapse a crash loop's identical lines into one with a count.
	if n := len(p.lines); n > 0 {
		last := &p.lines[n-1]
		if last.Unit == l.Unit && last.Message == l.Message {
			last.Count++
			last.Time = l.Time
			return
		}
	}
	l.Count = 1
	p.lines = append(p.lines, l)
	if len(p.lines) > keepLines {
		p.lines = append([]state.JournalLine(nil), p.lines[len(p.lines)-keepLines:]...)
	}
}

func (c *Collector) prune() {
	cutoff := c.now().Add(-window)
	for _, p := range c.byProj {
		i := 0
		for i < len(p.lines) && p.lines[i].Time.Before(cutoff) {
			i++
		}
		p.lines = p.lines[i:]
		for h := range p.hours {
			if h < cutoff.Unix()/3600*3600 {
				delete(p.hours, h)
			}
		}
	}
}

// Results returns each project's counts and lines, newest line first.
func (c *Collector) Results() []state.ProjectJournal {
	now := c.now()
	nowHour := now.Unix() / 3600 * 3600
	start := nowHour - 23*3600
	out := make([]state.ProjectJournal, 0, len(c.byProj))
	for id, p := range c.byProj {
		r := state.ProjectJournal{Project: id, PerHour: make([]int, 24), ByUnit: map[string]int{}}
		for h, b := range p.hours {
			// A clock stepped back can leave entries in a "future" hour.
			if h >= start && h <= nowHour {
				r.PerHour[(h-start)/3600] += b[0]
				r.Errors24 += b[0]
				r.Warns24 += b[1]
			}
		}
		for _, l := range p.lines {
			if l.Level == "error" && now.Sub(l.Time) <= time.Hour && !l.Time.After(now) {
				r.Errors1h += l.Count
			}
			r.ByUnit[l.Unit] += l.Count
		}
		r.Lines = make([]state.JournalLine, len(p.lines))
		for i, l := range p.lines {
			r.Lines[len(p.lines)-1-i] = l
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}
