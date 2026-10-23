package envfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

const fixture = `# comment
; also a comment

DATABASE_URL=postgres://app:CANARY-db-pass-1@127.0.0.1:5432/app?schema=public&x=1
export JWT_SECRET="CANARY-jwt-secret-2"
EMPTY=
EMPTY_QUOTED=""
SPACED = CANARY-spaced-value-3
MULTI="first line CANARY-multi-4
second line"
SINGLE='CANARY-single-5 with "quotes"'
CONT=CANARY-cont-\
inued-6
PORT=4101
not a variable line CANARY-junk-7
1BAD=CANARY-badname-8
JWT_SECRET=CANARY-dup-9
`

var canaries = []string{"CANARY-db-pass-1", "CANARY-jwt-secret-2", "CANARY-spaced-value-3",
	"CANARY-multi-4", "CANARY-single-5", "CANARY-cont-", "CANARY-junk-7", "CANARY-badname-8", "CANARY-dup-9"}

func TestParseNames(t *testing.T) {
	res, err := Parse(strings.NewReader(fixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []state.EnvVar{
		{Name: "DATABASE_URL", Set: true},
		{Name: "JWT_SECRET", Set: true, Duplicate: true},
		{Name: "EMPTY", Set: false},
		{Name: "EMPTY_QUOTED", Set: false},
		{Name: "SPACED", Set: true},
		{Name: "MULTI", Set: true},
		{Name: "SINGLE", Set: true},
		{Name: "CONT", Set: true},
		{Name: "PORT", Set: true},
	}
	if !reflect.DeepEqual(res.Vars, want) {
		t.Fatalf("vars:\n got %+v\nwant %+v", res.Vars, want)
	}
	if res.Unparsed != 2 {
		t.Fatalf("unparsed = %d, want 2", res.Unparsed)
	}
}

// The result type must have no field that could carry a value.
func TestEnvVarHasNoValueField(t *testing.T) {
	typ := reflect.TypeOf(state.EnvVar{})
	allowed := map[string]reflect.Kind{"Name": reflect.String, "Set": reflect.Bool, "Duplicate": reflect.Bool}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if k, ok := allowed[f.Name]; !ok || f.Type.Kind() != k {
			t.Fatalf("state.EnvVar has field %s %s: only names and booleans may leave the parser", f.Name, f.Type)
		}
	}
}

func TestResultLeaksNoValue(t *testing.T) {
	var sb ScrubBuilder
	res, err := Parse(strings.NewReader(fixture), &sb)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res)
	text := string(out) + fmt.Sprintf("%v %+v %#v", res, res, res)
	for _, c := range canaries {
		if strings.Contains(text, c) {
			t.Fatalf("parse result contains value %q: %s", c, text)
		}
	}
}

func TestScrubberNeverPrintsValues(t *testing.T) {
	var sb ScrubBuilder
	if _, err := Parse(strings.NewReader(fixture), &sb); err != nil {
		t.Fatal(err)
	}
	s := sb.Build()
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("scrubber", "s", s)
	slog.New(slog.NewTextHandler(&logs, nil)).Info("scrubber", "s", s)
	js, _ := json.Marshal(map[string]any{"s": s})
	text := fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s) + string(js) + logs.String()
	for _, c := range canaries {
		if strings.Contains(text, c) {
			t.Fatalf("scrubber printed value %q: %s", c, text)
		}
	}
	if !strings.Contains(text, "[scrubber:") {
		t.Fatalf("expected the safe placeholder, got %s", text)
	}
}

func TestScrub(t *testing.T) {
	var sb ScrubBuilder
	if _, err := Parse(strings.NewReader(fixture), &sb); err != nil {
		t.Fatal(err)
	}
	s := sb.Build()
	line := `connect failed postgres://app:CANARY-db-pass-1@127.0.0.1:5432/app?schema=public&x=1 token=CANARY-dup-9 port 4101`
	got := s.Scrub(line)
	for _, c := range canaries {
		if strings.Contains(got, c) {
			t.Fatalf("scrubbed line still has %q: %s", c, got)
		}
	}
	if !strings.Contains(got, "[env:DATABASE_URL]") || !strings.Contains(got, "[env:JWT_SECRET]") {
		t.Fatalf("expected named placeholders: %s", got)
	}
	if !strings.Contains(got, "port 4101") {
		t.Fatalf("short values must not be scrubbed: %s", got)
	}
	if (*Scrubber)(nil).Scrub("x") != "x" {
		t.Fatal("nil scrubber must pass text through")
	}
}

func TestCRLFAndLimits(t *testing.T) {
	res, err := Parse(strings.NewReader("A=1\r\nB=\r\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Vars) != 2 || !res.Vars[0].Set || res.Vars[1].Set {
		t.Fatalf("crlf: %+v", res.Vars)
	}
	big := strings.Repeat("X=1\n", MaxSize) // 4 MiB, read only up to the cap
	res, err = Parse(strings.NewReader(big), nil)
	if err != nil || len(res.Vars) != 1 {
		t.Fatalf("big: %v %d", err, len(res.Vars))
	}
}

// Every case from the security review: a fragment of a value must never come
// back as a name, under systemd's reading or dotenv's.
func TestNoValueFragmentBecomesAName(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string // names that must be found
		forbid   []string // fragments that must not appear as names
	}{
		{"backtick multi-line", "KEY=`l1\nSECRETPART=abc`\nAFTER=1\n", []string{"KEY", "AFTER"}, []string{"SECRETPART"}},
		{"quote mid-value", "KEY=pre\"l1\nInnerSecret9=x\"\nAFTER=1\n", []string{"KEY", "AFTER"}, []string{"InnerSecret9"}},
		{"escaped single quote", "A='abc\\'\nLEAKED=x'\nAFTER=1\n", []string{"A", "AFTER"}, []string{"LEAKED"}},
		{"double-quoted multi-line", "K=\"line one\nHIDDEN=two\"\nAFTER=1\n", []string{"K", "AFTER"}, []string{"HIDDEN"}},
		{"unquoted PEM", "KEY=-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1b2c3d4==\nAoGBAMx7yZq9Rk2Lp4TnWvB8Jc==\n-----END RSA PRIVATE KEY-----\nAFTER=1\n", []string{"KEY", "AFTER"}, []string{"MIIEowIBAAKCAQEA1b2c3d4", "AoGBAMx7yZq9Rk2Lp4TnWvB8Jc"}},
		{"bare base64 token", "dGhpc0lzQVNlY3JldFRva2VuMTIz==\n", nil, []string{"dGhpc0lzQVNlY3JldFRva2VuMTIz"}},
		{"base64 with padding", "Zm9vYmFyYmF6cXV4==\n", nil, []string{"Zm9vYmFyYmF6cXV4"}},
		{"token without padding", "c2VjcmV0dG9rZW52YWx1ZQ\n", nil, []string{"c2VjcmV0dG9rZW52YWx1ZQ"}},
		{"continuation", "K=abc\\\nNOTAKEY=def\nAFTER=1\n", []string{"K", "AFTER"}, []string{"NOTAKEY"}},
		{"no trailing newline", "A=1\nB=\"x", []string{"A", "B"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := Parse(strings.NewReader(c.in), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, v := range res.Vars {
				got[v.Name] = true
			}
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("missing name %s in %+v", w, res.Vars)
				}
			}
			for _, f := range c.forbid {
				for n := range got {
					if strings.Contains(n, f) || strings.Contains(f, n) && len(n) > 3 {
						t.Errorf("value fragment %q surfaced as name %q", f, n)
					}
				}
			}
		})
	}
}

func TestSystemdDecoding(t *testing.T) {
	in := "A=\"x\\\"y\"\nB='lit\\n'\nC= spaced  \nD=\"\"\nE=''\nF=a\" \"b\n# c\n; c\nexport G=1\n"
	res, err := Parse(strings.NewReader(in), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []state.EnvVar{{Name: "A", Set: true}, {Name: "B", Set: true}, {Name: "C", Set: true}, {Name: "D"}, {Name: "E"}, {Name: "F", Set: true}, {Name: "G", Set: true}}
	if !reflect.DeepEqual(res.Vars, want) {
		t.Fatalf("got %+v", res.Vars)
	}
}

func TestScrubberCatchesEveryForm(t *testing.T) {
	in := "Q=\"pa\\\"ss-word-123\"\nC=plainvalue123 # a comment\nM=\"first-line-secret\nsecond-line-secret\"\n"
	var sb ScrubBuilder
	if _, err := Parse(strings.NewReader(in), &sb); err != nil {
		t.Fatal(err)
	}
	sb.AddSecret("engine.token", "tok-0123456789abcdef\n")
	s := sb.Build()
	line := `got pa"ss-word-123 and plainvalue123 then first-line-secret, second-line-secret, tok-0123456789abcdef`
	got := s.Scrub(line)
	for _, leak := range []string{`pa"ss-word-123`, "plainvalue123", "first-line-secret", "second-line-secret", "tok-0123456789abcdef"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	text := fmt.Sprintf("%v %+v %#v %s", sb, &sb, sb, &sb)
	js, _ := json.Marshal(map[string]any{"b": sb, "p": &sb})
	if strings.Contains(text+string(js), "plainvalue123") || !strings.Contains(text, "[scrub builder:") {
		t.Fatalf("builder printed values: %s %s", text, js)
	}
}

func TestScrubberSkipsAddressesAndNumbers(t *testing.T) {
	var sb ScrubBuilder
	if _, err := Parse(strings.NewReader("HOST=127.0.0.1\nBIG=1234567890\nV6=0:0:0:0:0:0:0:1\nKEY=real-secret-value\n"), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.Build().Scrub("from 127.0.0.1 id 1234567890 ::1 0:0:0:0:0:0:0:1 key real-secret-value")
	if got != "from 127.0.0.1 id 1234567890 ::1 0:0:0:0:0:0:0:1 key [env:KEY]" {
		t.Fatalf("got %q", got)
	}
}
