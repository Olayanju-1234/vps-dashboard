// Package envfile reads env files for their variable names. Values are never
// returned, logged or marshalled: the only thing a value can become is a
// redaction rule inside a Scrubber, which refuses to print itself.
package envfile

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// MaxSize caps how much of a file is read; env files are tiny.
const MaxSize = 1 << 20

// MinScrubLen is the shortest value worth scrubbing from logs; shorter ones
// (ports, "true") would redact half of every line.
const MinScrubLen = 8

// Result is what a parse yields: names, never values.
type Result struct {
	Vars     []state.EnvVar
	Unparsed int
}

// Parse returns the variable names in an env file. When sink is non-nil each
// non-empty value is added to it for log scrubbing, then dropped.
func Parse(r io.Reader, sink *ScrubBuilder) (Result, error) {
	var res Result
	seen := map[string]int{}
	err := scan(io.LimitReader(r, MaxSize), func(name, value string, ok bool) {
		if !ok {
			res.Unparsed++
			return
		}
		if i, dup := seen[name]; dup {
			res.Vars[i].Duplicate = true
			res.Vars[i].Set = value != ""
		} else {
			seen[name] = len(res.Vars)
			res.Vars = append(res.Vars, state.EnvVar{Name: name, Set: value != ""})
		}
		if sink != nil {
			sink.add(name, value)
		}
	})
	if err != nil {
		return Result{}, fmt.Errorf("envfile: %w", err)
	}
	return res, nil
}

// scan walks KEY=VALUE lines the way systemd's EnvironmentFile and dotenv do:
// comments, "export ", quotes that span lines, and backslash continuations.
func scan(r io.Reader, emit func(name, value string, ok bool)) error {
	br := bufio.NewReader(r)
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			emit("", "", false)
			continue
		}
		name := strings.TrimSpace(line[:eq])
		if !validName(name) {
			emit("", "", false)
			continue
		}
		raw := strings.TrimSpace(line[eq+1:])
		var value string
		switch {
		case raw != "" && (raw[0] == '"' || raw[0] == '\''):
			q := raw[0]
			body := raw[1:]
			for {
				if end := closingQuote(body, q); end >= 0 {
					value = body[:end]
					break
				}
				if i+1 >= len(lines) {
					value = body // unterminated: still a name, value as read
					break
				}
				i++
				body += "\n" + lines[i]
			}
		default:
			for strings.HasSuffix(raw, `\`) && i+1 < len(lines) {
				i++
				raw = strings.TrimSuffix(raw, `\`) + strings.TrimSpace(lines[i])
			}
			value = raw
		}
		emit(name, strings.TrimSpace(value), true)
	}
	return nil
}

func closingQuote(s string, q byte) int {
	for i := 0; i < len(s); i++ {
		if q == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == q {
			return i
		}
	}
	return -1
}

func validName(s string) bool {
	for i, c := range s {
		switch {
		case c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
		case i > 0 && ((c >= '0' && c <= '9') || c == '.'):
		default:
			return false
		}
	}
	return s != ""
}

// ScrubBuilder collects values from several files into one Scrubber.
type ScrubBuilder struct {
	pairs map[string]string // value -> name
}

func (b *ScrubBuilder) add(name, value string) {
	if len(value) < MinScrubLen {
		return
	}
	if b.pairs == nil {
		b.pairs = map[string]string{}
	}
	b.pairs[value] = name
}

// Build returns a Scrubber that replaces every collected value with [env:NAME].
func (b *ScrubBuilder) Build() *Scrubber {
	vals := make([]string, 0, len(b.pairs))
	for v := range b.pairs {
		vals = append(vals, v)
	}
	// Longest first: strings.Replacer tries patterns in argument order.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	args := make([]string, 0, 2*len(vals))
	for _, v := range vals {
		args = append(args, v, "[env:"+b.pairs[v]+"]")
	}
	return &Scrubber{r: strings.NewReplacer(args...), n: len(vals)}
}

// Scrubber removes known secret values from text. It holds those values, so
// every way of printing it is overridden to print only a count.
type Scrubber struct {
	r *strings.Replacer
	n int
}

// Scrub replaces any known value in s.
func (s *Scrubber) Scrub(text string) string {
	if s == nil || s.n == 0 {
		return text
	}
	return s.r.Replace(text)
}

// Len is how many values the scrubber knows.
func (s *Scrubber) Len() int {
	if s == nil {
		return 0
	}
	return s.n
}

func (s *Scrubber) safe() string { return fmt.Sprintf("[scrubber: %d values]", s.Len()) }

// String implements fmt.Stringer without revealing values.
func (s *Scrubber) String() string { return s.safe() }

// GoString implements fmt.GoStringer so %#v can't dump the fields.
func (s *Scrubber) GoString() string { return s.safe() }

// Format implements fmt.Formatter for every verb.
func (s *Scrubber) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.safe()) }

// MarshalJSON keeps values out of any JSON the scrubber ends up in.
func (s *Scrubber) MarshalJSON() ([]byte, error) { return []byte(`"` + s.safe() + `"`), nil }

// LogValue keeps values out of slog output.
func (s *Scrubber) LogValue() slog.Value { return slog.StringValue(s.safe()) }
