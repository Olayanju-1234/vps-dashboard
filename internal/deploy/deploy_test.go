package deploy

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/github"
)

func blob(content string) string {
	h := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(h[:])
}

type fakeCommit struct {
	sha, subject, tree string
	files              map[string]string // path -> content
}

// fakeGitHub serves commits newest first and a tree per commit.
func fakeGitHub(t *testing.T, commits []fakeCommit, compare map[string]int) (*github.Client, *int) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.URL.Path == "/repos/o/r/commits":
			var out []map[string]any
			for i, c := range commits {
				out = append(out, map[string]any{"sha": c.sha, "commit": map[string]any{
					"message": c.subject, "committer": map[string]any{"date": time.Date(2026, 10, 20-i, 0, 0, 0, 0, time.UTC)},
					"tree": map[string]any{"sha": c.tree}}})
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/git/trees/"):
			sha := strings.TrimPrefix(r.URL.Path, "/repos/o/r/git/trees/")
			for _, c := range commits {
				if c.tree != sha {
					continue
				}
				// Like git, a folder's id depends only on what is inside it.
				subtrees := map[string]string{}
				var entries []map[string]string
				for p, content := range c.files {
					entries = append(entries, map[string]string{"path": p, "type": "blob", "sha": blob(content), "mode": "100644"})
					if dir, _, ok := strings.Cut(p, "/"); ok {
						subtrees[dir] += p + "=" + content + ";"
					}
				}
				for dir, content := range subtrees {
					entries = append(entries, map[string]string{"path": dir, "type": "tree", "sha": blob(content), "mode": "040000"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "tree": entries})
				return
			}
			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/compare/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ahead", "ahead_by": compare[r.URL.Path], "behind_by": 0})
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": strings.TrimPrefix(r.URL.Path, "/repos/o/r/commits/"),
				"commit": map[string]any{"message": "old one", "committer": map[string]any{"date": "2026-01-01T00:00:00Z"}, "tree": map[string]any{"sha": "x"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return github.New(srv.URL, "test"), &calls
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTreeMatchFindsDeployedCommit(t *testing.T) {
	commits := []fakeCommit{
		{"c3", "feat: newest", "t3", map[string]string{"api/a.js": "v3", "api/b.js": "b", "README.md": "r3"}},
		{"c2", "docs: readme only", "t2", map[string]string{"api/a.js": "v2", "api/b.js": "b", "README.md": "r2"}},
		{"c1", "feat: older", "t1", map[string]string{"api/a.js": "v1", "api/b.js": "b", "README.md": "r1"}},
	}
	gh, _ := fakeGitHub(t, commits, nil)
	dir := t.TempDir()
	// The server has api/ at c2, plus build output and an env file git never tracked.
	write(t, dir, map[string]string{"a.js": "v2", "b.js": "b", "dist/main.js": "built", ".env": "SECRET=x"})
	spec := config.DeploySpec{Repo: "o/r", Branch: "main", Method: "tree", Trees: []config.TreeDir{{Dir: dir, Prefix: "api"}}, DeployedAt: []string{dir}}
	d := New(gh)
	dep := d.Detect(context.Background(), spec)
	if dep.Error != "" || dep.Commit != "c2" || !dep.Exact || dep.Behind != 1 || dep.Status != "behind" || dep.Compared != 2 || dep.MainCommit != "c3" || dep.DeployedAt.IsZero() {
		t.Fatalf("deploy: %+v", dep)
	}

	// Deploy main: now current.
	write(t, dir, map[string]string{"a.js": "v3"})
	dep = d.Detect(context.Background(), spec)
	if dep.Commit != "c3" || dep.Behind != 0 || dep.Status != "current" {
		t.Fatalf("after deploy: %+v", dep)
	}

	// A hand edit on the server: closest commit, not exact.
	write(t, dir, map[string]string{"b.js": "hotfix"})
	dep = d.Detect(context.Background(), spec)
	if dep.Exact || dep.Differing != 1 || dep.Commit != "c3" || !strings.Contains(dep.Note, "differ") {
		t.Fatalf("edited: %+v", dep)
	}
}

func looseObject(t *testing.T, gitDir, kind, body string) string {
	t.Helper()
	raw := fmt.Sprintf("%s %d\x00%s", kind, len(body), body)
	sum := sha1.Sum([]byte(raw))
	sha := hex.EncodeToString(sum[:])
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write([]byte(raw))
	_ = zw.Close()
	write(t, gitDir, map[string]string{filepath.Join("objects", sha[:2], sha[2:]): buf.String()})
	return sha
}

func TestGitDirPrivateRepo(t *testing.T) {
	gitDir := t.TempDir()
	head := looseObject(t, gitDir, "commit", "tree abc\nauthor A <a@x> 1792700000 +0000\ncommitter A <a@x> 1792700100 +0000\n\nfix(api): deployed one\n\nbody\n")
	main := strings.Repeat("e", 40)
	write(t, gitDir, map[string]string{
		"HEAD":            "ref: refs/heads/main\n",
		"refs/heads/main": head + "\n",
		"packed-refs":     "# pack-refs with: peeled\n" + main + " refs/remotes/origin/main\n",
		"logs/HEAD":       "0000 " + head + " A <a@x> 1792700200 +0000\tcommit: fix(api): deployed one\n",
	})
	d := New(github.New("http://127.0.0.1:1", "test"))
	dep := d.Detect(context.Background(), config.DeploySpec{Method: "git_dir", GitDir: gitDir, Branch: "main", Private: true})
	if dep.Error != "" || dep.Commit != head || dep.Subject != "fix(api): deployed one" || dep.CommitTime.Unix() != 1792700100 {
		t.Fatalf("head: %+v", dep)
	}
	if dep.MainCommit != main || dep.MainSource != "local-ref" || dep.Status != "unknown" || dep.Behind != -1 {
		t.Fatalf("main: %+v", dep)
	}
	write(t, gitDir, map[string]string{"refs/remotes/origin/main": head + "\n"})
	dep = d.Detect(context.Background(), config.DeploySpec{Method: "git_dir", GitDir: gitDir, Branch: "main", Private: true})
	if dep.Status != "current" || dep.Behind != 0 {
		t.Fatalf("same as origin/main: %+v", dep)
	}
}

func TestGitDirPublicRepoComparesOnGitHub(t *testing.T) {
	gitDir := t.TempDir()
	old := strings.Repeat("a", 40)
	write(t, gitDir, map[string]string{"HEAD": old + "\n"})
	gh, _ := fakeGitHub(t, []fakeCommit{{"c3", "feat: newest", "t3", nil}}, map[string]int{"/repos/o/r/compare/" + old + "...c3": 25})
	dep := New(gh).Detect(context.Background(), config.DeploySpec{Method: "git_dir", GitDir: gitDir, Repo: "o/r", Branch: "main"})
	if dep.Error != "" || dep.Behind != 25 || dep.Status != "behind" || dep.Subject != "old one" {
		t.Fatalf("detached head: %+v", dep)
	}
}

func TestGoBinaryWithoutVCS(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dep := New(github.New("http://127.0.0.1:1", "test")).Detect(context.Background(), config.DeploySpec{Method: "go_binary", Binary: exe})
	if dep.Error == "" && dep.Commit == "" {
		t.Fatalf("expected a commit or an explanation: %+v", dep)
	}
	missing := New(nil).Detect(context.Background(), config.DeploySpec{Method: "go_binary", Binary: "/nonexistent"})
	if missing.Status != "error" || missing.Error == "" {
		t.Fatalf("missing binary: %+v", missing)
	}
}

func TestSameCommit(t *testing.T) {
	full := "9f19ab8c0ffee0000000000000000000000000000"
	if !sameCommit(full, "9f19ab8") || sameCommit(full, "9f19ab9") || sameCommit(full, "9f1") {
		t.Fatal("sameCommit")
	}
}

func TestTreeMatchPerFolder(t *testing.T) {
	commits := []fakeCommit{
		{"c2", "feat(api): newest", "t2", map[string]string{"api/a.js": "a2", "web/w.js": "w1"}},
		{"c1", "feat(web): older", "t1", map[string]string{"api/a.js": "a1", "web/w.js": "w1"}},
		{"c0", "first", "t0", map[string]string{"api/a.js": "a1", "web/w.js": "w0"}},
	}
	gh, _ := fakeGitHub(t, commits, nil)
	apiDir, webDir := t.TempDir(), t.TempDir()
	write(t, apiDir, map[string]string{"a.js": "a2"})
	write(t, webDir, map[string]string{"w.js": "w0"}) // web was last deployed at c0
	spec := config.DeploySpec{Repo: "o/r", Branch: "main", Method: "tree", Trees: []config.TreeDir{{Dir: apiDir, Prefix: "api"}, {Dir: webDir, Prefix: "web"}}}
	dep := New(gh).Detect(context.Background(), spec)
	if dep.Commit != "c0" || dep.Behind != 2 || !dep.Exact || dep.Status != "behind" || !strings.Contains(dep.Note, "api at c2 (matches main)") || !strings.Contains(dep.Note, "web at c0 (2 behind)") {
		t.Fatalf("per folder: %+v", dep)
	}
}
