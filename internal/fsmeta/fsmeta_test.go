package fsmeta

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("A=secret-value\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	m := Stat(p)
	if !m.Exists || m.Mode != "0640" || m.Size != 15 || m.Owner == "" || m.Group == "" {
		t.Fatalf("meta: %+v", m)
	}
	if time.Since(m.ModTime) > time.Minute {
		t.Fatalf("mtime: %v", m.ModTime)
	}
	if missing := Stat(filepath.Join(dir, "nope")); missing.Exists || missing.Error != "" {
		t.Fatalf("missing: %+v", missing)
	}
	c, err := Changed(p)
	if err != nil || time.Since(c) > time.Minute {
		t.Fatalf("changed: %v %v", c, err)
	}
}
