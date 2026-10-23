// Package envfile reads env files for their variable names. Values are never
// returned, logged or marshalled: the only thing a value can become is a
// redaction rule inside a Scrubber, which refuses to print itself.
//
// Parsing follows systemd's EnvironmentFile state machine, because systemd is
// what loads these files. A line that dotenv would read as part of a multi-line
// value is never reported as a name either, so a fragment of a value can't
// surface as a "name" under either reading.
package envfile

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strconv"
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

type entry struct {
	name  string
	value string // as systemd decodes it
	raw   string // the first line's text after '=', as dotenv sees it
	line  int    // line the key starts on, 0-based
}

// Parse returns the variable names in an env file. When sink is non-nil every
// value (and every line that wasn't understood) is added to it for log
// scrubbing, then dropped.
func Parse(r io.Reader, sink *ScrubBuilder) (Result, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxSize))
	if err != nil {
		return Result{}, fmt.Errorf("envfile: read: %w", err)
	}
	text := string(b)
	entries, junk := parseSystemd(text)
	lines := strings.Split(text, "\n")
	ambiguous := quoteContinuations(lines)
	for l := range pemBodies(lines) {
		ambiguous[l] = true
	}

	var res Result
	seen := map[string]int{}
	for _, e := range entries {
		name := strings.TrimPrefix(e.name, "export ")
		if sink != nil {
			sink.addValue(name, e.value, e.raw)
		}
		if ambiguous[e.line] || !plausibleName(name) || onlyPadding(e.raw) {
			res.Unparsed++
			continue
		}
		set := e.value != ""
		if i, dup := seen[name]; dup {
			res.Vars[i].Duplicate = true
			res.Vars[i].Set = set
			continue
		}
		seen[name] = len(res.Vars)
		res.Vars = append(res.Vars, state.EnvVar{Name: name, Set: set})
	}
	res.Unparsed += len(junk)
	if sink != nil {
		for _, j := range junk {
			sink.add("unparsed", j)
		}
	}
	return res, nil
}

const (
	stPreKey = iota
	stKey
	stPreValue
	stValue
	stValueEscape
	stSingle
	stDouble
	stDoubleEscape
	stComment
)

// parseSystemd mirrors src/basic/env-file.c (systemd 255): unquoted values end
// at a newline unless it is escaped; quotes may open anywhere in a value and
// span lines. Lines without '=' are returned as junk.
func parseSystemd(text string) ([]entry, []string) {
	var entries []entry
	var junk []string
	var key, val strings.Builder
	st, line, keyLine, rawStart := stPreKey, 0, 0, 0
	unquotedTail := false

	emit := func(at int) {
		v := val.String()
		if unquotedTail {
			v = strings.TrimRight(v, " \t")
		}
		raw := text[rawStart:at]
		if nl := strings.IndexAny(raw, "\r\n"); nl >= 0 {
			raw = raw[:nl]
		}
		entries = append(entries, entry{name: strings.TrimRight(key.String(), " \t"), value: v, raw: strings.TrimSpace(raw), line: keyLine})
		key.Reset()
		val.Reset()
		unquotedTail = false
	}

	for i := 0; i < len(text); i++ {
		c := text[i]
		newline := c == '\n' || c == '\r'
		switch st {
		case stPreKey:
			switch {
			case c == '#' || c == ';':
				st = stComment
			case c == ' ' || c == '\t' || newline:
			default:
				st, keyLine = stKey, line
				key.WriteByte(c)
			}
		case stKey:
			switch {
			case newline:
				junk = append(junk, strings.TrimSpace(key.String()))
				key.Reset()
				st = stPreKey
			case c == '=':
				st, rawStart = stPreValue, i+1
			default:
				key.WriteByte(c)
			}
		case stPreValue:
			switch {
			case newline:
				emit(i)
				st = stPreKey
			case c == '\'':
				st = stSingle
			case c == '"':
				st = stDouble
			case c == '\\':
				st = stValueEscape
			case c == ' ' || c == '\t':
			default:
				st, unquotedTail = stValue, true
				val.WriteByte(c)
			}
		case stValue:
			switch {
			case newline:
				emit(i)
				st = stPreKey
			case c == '\\':
				st = stValueEscape
			default:
				val.WriteByte(c)
			}
		case stValueEscape:
			st, unquotedTail = stValue, true
			if !newline {
				val.WriteByte(c)
			}
		case stSingle:
			if c == '\'' {
				st, unquotedTail = stPreValue, false
			} else {
				val.WriteByte(c)
			}
		case stDouble:
			switch c {
			case '"':
				st, unquotedTail = stPreValue, false
			case '\\':
				st = stDoubleEscape
			default:
				val.WriteByte(c)
			}
		case stDoubleEscape:
			st = stDouble
			switch {
			case strings.IndexByte("\"\\`$", c) >= 0:
				val.WriteByte(c)
			case newline:
			default:
				val.WriteByte('\\')
				val.WriteByte(c)
			}
		case stComment:
			if newline {
				st = stPreKey
			}
		}
		if c == '\n' {
			line++
		}
	}
	switch st {
	case stKey:
		junk = append(junk, strings.TrimSpace(key.String()))
	case stPreValue, stValue, stValueEscape, stSingle, stDouble, stDoubleEscape:
		emit(len(text))
	}
	return entries, junk
}

var assignment = regexp.MustCompile(`^\s*(?:export\s+)?[\w.-]+(?:\s*=|:\s)`)

// quoteContinuations marks lines that some reader could take as the inside of
// a quoted value: dotenv's quotes at the start of a value, systemd's quotes
// mid-value, backticks. If any quote is still open at the end of a KEY= line,
// every line up to its first unescaped closing quote is marked. Erring wide
// can hide a real name; it can never show a slice of a value as one.
func quoteContinuations(lines []string) map[int]bool {
	inside := map[int]bool{}
	for i := 0; i < len(lines); i++ {
		m := assignment.FindStringIndex(lines[i])
		if m == nil {
			continue
		}
		q := openQuote(lines[i][m[1]:])
		if q == 0 {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if closingQuote(lines[j], q) >= 0 {
				for k := i + 1; k <= j; k++ {
					inside[k] = true
				}
				i = j
				break
			}
		}
	}
	return inside
}

// openQuote returns the quote left open at the end of s, treating \q as escaped.
func openQuote(s string) byte {
	var open byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			i++
		case open == 0 && (c == '"' || c == '\'' || c == '`'):
			open = c
		case c == open:
			open = 0
		}
	}
	return open
}

func closingQuote(s string, q byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == q && (i == 0 || s[i-1] != '\\') {
			return i
		}
	}
	return -1
}

// pemBodies marks the lines between -----BEGIN and -----END: a key pasted
// without quotes must not have its base64 lines read as names.
func pemBodies(lines []string) map[int]bool {
	inside := map[int]bool{}
	for i := 0; i < len(lines); i++ {
		if !strings.Contains(lines[i], "-----BEGIN") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			inside[j] = true
			if strings.Contains(lines[j], "-----END") {
				i = j
				break
			}
		}
	}
	return inside
}

var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// plausibleName is systemd's rule for names, minus strings that look like a
// slice of base64 (long, mixed case and digits, no underscore).
func plausibleName(s string) bool {
	if !validName.MatchString(s) {
		return false
	}
	if len(s) >= 20 && !strings.Contains(s, "_") {
		var lower, upper, digit bool
		for _, c := range s {
			switch {
			case c >= 'a' && c <= 'z':
				lower = true
			case c >= 'A' && c <= 'Z':
				upper = true
			case c >= '0' && c <= '9':
				digit = true
			}
		}
		if lower && upper && digit {
			return false
		}
	}
	return true
}

// onlyPadding catches base64 lines ending in "==", which split into NAME and "=".
func onlyPadding(raw string) bool {
	return raw != "" && strings.Trim(raw, "=") == ""
}

// ScrubBuilder collects values from several files into one Scrubber. It holds
// values, so like Scrubber it never prints them.
type ScrubBuilder struct {
	pairs map[string]string // value -> name
}

func (b *ScrubBuilder) add(name, value string) {
	value = strings.TrimSpace(value)
	if len(value) < MinScrubLen || notSecret(value) {
		return
	}
	if b.pairs == nil {
		b.pairs = map[string]string{}
	}
	b.pairs[value] = name
}

// addValue records every form a value may take in a log: as systemd decodes
// it, as dotenv reads it (quotes and a trailing comment removed), and line by
// line when it spans several lines.
func (b *ScrubBuilder) addValue(name, decoded, raw string) {
	b.add(name, decoded)
	for _, l := range strings.Split(decoded, "\n") {
		b.add(name, l)
	}
	r := raw
	if len(r) >= 2 && strings.IndexByte("\"'`", r[0]) >= 0 {
		if end := strings.LastIndexByte(r, r[0]); end > 0 {
			r = r[1:end]
		} else {
			r = r[1:]
		}
	} else if i := strings.Index(r, " #"); i >= 0 {
		r = r[:i]
	}
	b.add(name, r)
}

// notSecret spots values that can't be secrets but would otherwise be scrubbed
// from every log line they appear in, like HOST=127.0.0.1 or a port.
func notSecret(v string) bool {
	if net.ParseIP(v) != nil {
		return true
	}
	_, err := strconv.ParseFloat(v, 64)
	return err == nil
}

// AddSecret records the contents of a single-value secret file.
func (b *ScrubBuilder) AddSecret(name, content string) {
	b.add(name, content)
	for _, l := range strings.Split(content, "\n") {
		b.add(name, l)
	}
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

func safe(kind string, n int) string { return fmt.Sprintf("[%s: %d values]", kind, n) }

// String, GoString, Format, MarshalJSON and LogValue keep the builder's values
// out of fmt, slog and JSON. Value receivers cover both b and &b.
func (b ScrubBuilder) String() string               { return safe("scrub builder", len(b.pairs)) }
func (b ScrubBuilder) GoString() string             { return b.String() }
func (b ScrubBuilder) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, b.String()) }
func (b ScrubBuilder) MarshalJSON() ([]byte, error) { return []byte(`"` + b.String() + `"`), nil }
func (b ScrubBuilder) LogValue() slog.Value         { return slog.StringValue(b.String()) }

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

// String implements fmt.Stringer without revealing values.
func (s Scrubber) String() string { return safe("scrubber", s.n) }

// GoString implements fmt.GoStringer so %#v can't dump the fields.
func (s Scrubber) GoString() string { return s.String() }

// Format implements fmt.Formatter for every verb.
func (s Scrubber) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// MarshalJSON keeps values out of any JSON the scrubber ends up in.
func (s Scrubber) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

// LogValue keeps values out of slog output.
func (s Scrubber) LogValue() slog.Value { return slog.StringValue(s.String()) }
