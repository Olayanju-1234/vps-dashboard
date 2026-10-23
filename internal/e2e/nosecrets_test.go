// Package e2e runs the whole pipeline (probe, snapshot file, collector, HTTP
// server) against planted secrets and proves no value comes out the far end.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/collect"
	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/probe"
	"github.com/olayanju-1234/vps-dashboard/internal/server"
	"github.com/olayanju-1234/vps-dashboard/web"
)

func canary(t *testing.T, label string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "CANARY" + label + hex.EncodeToString(b)
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestNoSecretValueLeavesThePipeline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell scripts as fake journalctl/systemctl")
	}
	dir := t.TempDir()
	var secrets []string
	newSecret := func(label string) string {
		s := canary(t, label)
		secrets = append(secrets, s)
		return s
	}

	dbPass := newSecret("db")
	jwt := newSecret("jwt")
	quoted := newSecret("quoted")
	multi := newSecret("multi")
	token := newSecret("token")
	oldCopy := newSecret("copy")
	queryTok := newSecret("query")
	bodySecret := newSecret("body")
	logOnly := newSecret("logonly")

	envPath := filepath.Join(dir, "opt/shop/.env")
	write(t, envPath, fmt.Sprintf("HOST=127.0.0.1\nDATABASE_URL=postgres://shop:%s@127.0.0.1:5432/shop?x=1&y=2\nexport JWT_SECRET=%s\nQUOTED=\"%s with spaces\"\nMULTI='line one %s\nline two'\nEMPTY=\nnot a line %s\n",
		dbPass, jwt, quoted, multi, newSecret("junk")), 0o600)
	tokenPath := filepath.Join(dir, "etc/engine.token")
	write(t, tokenPath, token+"\n", 0o640)
	write(t, filepath.Join(dir, "opt/shop/.env.bak-1"), "JWT_SECRET="+oldCopy+"\n", 0o600)
	write(t, filepath.Join(dir, "opt/shop/.env.example"), "JWT_SECRET=\n", 0o644)

	backupDir := filepath.Join(dir, "backups")
	write(t, filepath.Join(backupDir, "shop-1.sql.gz"), "dump "+dbPass, 0o600)

	// The journal repeats secrets the way a careless app would.
	journal := filepath.Join(dir, "journal.jsonl")
	now := time.Now()
	var jl bytes.Buffer
	for i, msg := range []string{
		`{"level":50,"msg":"db connect failed","url":"postgres://shop:` + dbPass + `@127.0.0.1:5432/shop?x=1&y=2"}`,
		`level=ERROR msg="bad token" jwt=` + jwt,
		`Error: auth failed for header x-internal-token: ` + logOnly,
		`ERROR quoted value ` + quoted + ` with spaces`,
		`ERROR Authorization: Bearer ` + logOnly,
	} {
		e, _ := json.Marshal(map[string]string{
			"__CURSOR": fmt.Sprintf("c%d", i), "__REALTIME_TIMESTAMP": fmt.Sprint(now.Add(-time.Duration(10-i) * time.Minute).UnixMicro()),
			"MESSAGE": msg, "PRIORITY": "6", "_SYSTEMD_UNIT": "shop-api.service",
		})
		jl.Write(append(e, '\n'))
	}
	write(t, journal, jl.String(), 0o644)
	fakeJournalctl := filepath.Join(dir, "bin/journalctl")
	write(t, fakeJournalctl, "#!/bin/sh\ncat '"+journal+"'\n", 0o755)

	fakeSystemctl := filepath.Join(dir, "bin/systemctl")
	write(t, fakeSystemctl, fmt.Sprintf("#!/bin/sh\nprintf 'Id=shop-api.service\\nLoadState=loaded\\nActiveState=active\\nSubState=running\\nCPUUsageNSec=1000\\nMemoryCurrent=1048576\\nEnvironmentFiles=%s (ignore_errors=no)\\n'\n", envPath), 0o755)

	nginxLog := filepath.Join(dir, "access.log")
	ts := now.UTC().Format("02/Jan/2006:15:04:05 -0700")
	write(t, nginxLog, fmt.Sprintf(`203.0.113.7 - - [%s] "GET /confirm-email?token=%s HTTP/1.1" 200 12 "https://x.test/?k=%s" "curl/8" "shop.example.test" 0.001`+"\n", ts, queryTok, queryTok), 0o644)

	proc := filepath.Join(dir, "proc")
	write(t, filepath.Join(proc, "stat"), "cpu  100 0 50 1000 0 0 0 0 0 0\ncpu0 100 0 50 1000 0 0 0 0 0 0\n", 0o644)
	write(t, filepath.Join(proc, "meminfo"), "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n", 0o644)
	write(t, filepath.Join(proc, "loadavg"), "0.10 0.20 0.30 1/100 1\n", 0o644)
	write(t, filepath.Join(proc, "uptime"), "1000.0 900.0\n", 0o644)
	write(t, filepath.Join(proc, "net/tcp"), "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n   0: 0100007F:0FA0 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0 100 0 0 10 0\n", 0o644)

	// A health endpoint whose body holds a secret: the check must only keep the status.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","debug":"`+bodySecret+`"}`)
	}))
	defer app.Close()

	cfgJSON := fmt.Sprintf(`{
	  "listen": "127.0.0.1:8400",
	  "snapshot_path": %q,
	  "proc_root": %q,
	  "sysstat_dir": %q,
	  "disks": [%q],
	  "tls_addr": "127.0.0.1:1",
	  "journalctl": %q,
	  "systemctl": %q,
	  "nginx_logs": [{"path": %q, "label": "access.log"}],
	  "projects": [{
	    "id": "shop", "name": "Shop",
	    "units": ["shop-api"],
	    "ports": [{"port": 4000, "label": "API"}],
	    "checks": [{"url": %q}],
	    "hosts": ["shop.example.test"],
	    "backups": [{"label": "db", "dir": %q, "glob": "shop-*.sql.gz"}],
	    "env_files": [{"path": %q}, {"path": %q, "secret_file": true}],
	    "env_copy_globs": [%q]
	  }]
	}`, filepath.Join(dir, "probe.json"), proc, filepath.Join(dir, "sysstat"), dir, fakeJournalctl, fakeSystemctl, nginxLog,
		app.URL+"/health", backupDir, envPath, tokenPath, filepath.Join(dir, "opt/shop/.env*"))
	cfg, err := config.Parse([]byte(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.Background()

	p := probe.New(cfg, "test", log)
	snap := p.Cycle(ctx)
	if err := probe.Write(cfg.SnapshotPath, snap); err != nil {
		t.Fatal(err)
	}
	snapBytes, err := os.ReadFile(cfg.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}

	col := collect.New(cfg, "test", log)
	col.Fast(ctx)
	col.Fast(ctx) // second tick gives the sampler a CPU delta
	col.Checks(ctx)
	col.Slow(ctx)

	srv := httptest.NewServer(server.New(col, web.FS(), "test", log))
	defer srv.Close()
	bodies := map[string]string{}
	for _, path := range []string{"/api/state", "/api/history?range=1h", "/api/history?range=24h", "/api/journal?project=shop", "/healthz", "/", "/app.js", "/styles.css"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
		}
		bodies[path] = string(b)
	}

	// Positive controls: the pipeline really carried names, logs and counts.
	st := bodies["/api/state"]
	if !strings.Contains(st, ".env.bak-1") || strings.Contains(st, ".env.example") {
		t.Errorf("env copies should list the backup and skip the example")
	}
	for _, want := range []string{`"name":"DATABASE_URL","set":true`, `"name":"EMPTY","set":false`, `"name":"MULTI","set":true`, `"secret_file":true`, `"total_24h":1`, `"status":"ok"`, `"used_by":["shop-api.service"]`} {
		if !strings.Contains(st, want) {
			t.Errorf("state is missing %s", want)
		}
	}
	jr := bodies["/api/journal?project=shop"]
	for _, want := range []string{"[env:DATABASE_URL]", "[env:JWT_SECRET]", "[env:QUOTED]", "[redacted]"} {
		if !strings.Contains(jr, want) {
			t.Errorf("journal is missing placeholder %s: %s", want, jr)
		}
	}
	if !strings.Contains(string(snapBytes), `"count":1`) || !strings.Contains(string(snapBytes), "shop-1.sql.gz") {
		t.Errorf("snapshot is missing the backup: %s", snapBytes)
	}

	places := map[string]string{"snapshot file": string(snapBytes), "logs": logs.String()}
	for path, b := range bodies {
		places["GET "+path] = b
	}
	for where, text := range places {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("secret %s leaked into %s", s, where)
			}
		}
	}
	if t.Failed() {
		t.Logf("logs:\n%s", logs.String())
	}
}
