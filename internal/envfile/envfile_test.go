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
