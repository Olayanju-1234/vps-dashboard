package deploy

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/github"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// gitDir reads HEAD from a .git folder that was copied along with the code.
func (d *Detector) gitDir(ctx context.Context, spec config.DeploySpec, dep *state.Deploy) error {
	sha, err := resolveRef(spec.GitDir, "HEAD")
	if err != nil {
		return err
	}
	dep.Commit, dep.Exact = sha, true
	if t, subject, err := readCommit(spec.GitDir, sha); err == nil {
		dep.CommitTime, dep.Subject = t, subject
	} else if t, msg, ok := lastReflog(spec.GitDir); ok {
		dep.CommitTime, dep.Subject = t, msg
	}
	if spec.Private || spec.Repo == "" {
		// No API access: compare with origin/<branch> as it was when the code was copied.
		main, err := resolveRef(spec.GitDir, "refs/remotes/origin/"+spec.Branch)
		if err != nil {
			dep.Note = "private repo; no copy of origin/" + spec.Branch + " to compare with"
			return nil
		}
		mc := github.Commit{SHA: main}
		if t, subject, err := readCommit(spec.GitDir, main); err == nil {
			mc.Time, mc.Subject = t, subject
		}
		setMain(dep, mc, "local-ref")
		if main == sha {
			dep.Behind = 0
		}
		setStatus(dep)
		dep.Note = "private repo: compared with origin/" + spec.Branch + " as last fetched before the deploy"
		return nil
	}
	return d.compareMain(ctx, spec, dep)
}

// resolveRef follows symbolic refs through loose and packed refs.
func resolveRef(gitDir, ref string) (string, error) {
	for i := 0; i < 5; i++ {
		b, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref)))
		if err == nil {
			s := strings.TrimSpace(string(b))
			if target, ok := strings.CutPrefix(s, "ref: "); ok {
				ref = target
				continue
			}
			if isSHA(s) {
				return s, nil
			}
			return "", fmt.Errorf("deploy: %s: not a commit", ref)
		}
		if sha, ok := packedRef(gitDir, ref); ok {
			return sha, nil
		}
		return "", fmt.Errorf("deploy: resolve %s: %w", ref, err)
	}
	return "", fmt.Errorf("deploy: %s: too many symbolic refs", ref)
}

func packedRef(gitDir, ref string) (string, bool) {
	f, err := os.Open(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sha, name, ok := strings.Cut(sc.Text(), " ")
		if ok && name == ref && isSHA(sha) {
			return sha, true
		}
	}
	return "", false
}

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// readCommit inflates a loose commit object for its committer time and subject.
// Packed objects are skipped; the reflog is the fallback.
func readCommit(gitDir, sha string) (time.Time, string, error) {
	f, err := os.Open(filepath.Join(gitDir, "objects", sha[:2], sha[2:]))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("deploy: loose object: %w", err)
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("deploy: inflate: %w", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, 1<<20))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("deploy: inflate: %w", err)
	}
	_, body, ok := bytes.Cut(raw, []byte{0})
	if !ok || !bytes.HasPrefix(raw, []byte("commit ")) {
		return time.Time{}, "", fmt.Errorf("deploy: %s is not a commit", sha)
	}
	header, msg, _ := bytes.Cut(body, []byte("\n\n"))
	var t time.Time
	for _, line := range strings.Split(string(header), "\n") {
		if rest, ok := strings.CutPrefix(line, "committer "); ok {
			t = signatureTime(rest)
		}
	}
	subject, _, _ := strings.Cut(string(msg), "\n")
	return t, subject, nil
}

// signatureTime reads "<name> <email> 1729638481 +0000"; only the time is kept.
func signatureTime(sig string) time.Time {
	f := strings.Fields(sig)
	if len(f) < 2 {
		return time.Time{}
	}
	n, err := strconv.ParseInt(f[len(f)-2], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// lastReflog returns the time and message of the last HEAD move.
func lastReflog(gitDir string) (time.Time, string, bool) {
	b, err := os.ReadFile(filepath.Join(gitDir, "logs", "HEAD"))
	if err != nil {
		return time.Time{}, "", false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	last := lines[len(lines)-1]
	sig, msg, _ := strings.Cut(last, "\t")
	return signatureTime(sig), msg, true
}
