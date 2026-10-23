package deploy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/github"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

type fileHash struct {
	size int64
	mod  time.Time
	sha  string
	err  bool
}

type treeResult struct {
	compared, differing int
}

// tree finds, for each deployed folder, the newest recent commit whose files
// match the disk. Folders deployed on different days can match different
// commits; the project reports the one furthest behind.
func (d *Detector) tree(ctx context.Context, spec config.DeploySpec, dep *state.Deploy) error {
	if spec.Private {
		return errors.New("deploy: tree matching needs the GitHub API, which a private repo refuses without a token")
	}
	commits, err := d.gh.Commits(ctx, spec.Repo, spec.Branch, Recent)
	if err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	if len(commits) == 0 {
		return errors.New("deploy: branch has no commits")
	}
	setMain(dep, commits[0], "github")
	type match struct {
		dir config.TreeDir
		idx int
		res treeResult
	}
	var matches []match
	for _, td := range spec.Trees {
		m := match{dir: td, idx: -1}
		// Memo per call only: the answer depends on the disk, which changes on deploy.
		memo := map[string]treeResult{}
		for i, c := range commits {
			t, err := d.gh.Tree(ctx, spec.Repo, c.Tree)
			if err != nil {
				if m.idx >= 0 {
					break // out of API budget: report the best so far
				}
				return fmt.Errorf("deploy: %w", err)
			}
			dirs := []config.TreeDir{td}
			key := subtreeKey(t.SHA, t.Entries, dirs)
			res, ok := memo[key]
			if !ok {
				res = d.compareTree(t.Entries, dirs)
				memo[key] = res
			}
			if m.idx < 0 || res.differing < m.res.differing {
				m.idx, m.res = i, res
			}
			if res.differing == 0 {
				break
			}
		}
		if m.res.compared == 0 {
			return fmt.Errorf("deploy: no tracked files under %q in %s; check the prefix", td.Prefix, spec.Repo)
		}
		matches = append(matches, m)
	}
	worst := matches[0]
	dep.Exact = true
	var notes []string
	for _, m := range matches {
		dep.Compared += m.res.compared
		dep.Differing += m.res.differing
		if m.res.differing > 0 {
			dep.Exact = false
		}
		if m.idx > worst.idx || (m.idx == worst.idx && m.res.differing > worst.res.differing) {
			worst = m
		}
		if len(matches) > 1 {
			label := m.dir.Prefix
			if label == "" {
				label = "/"
			}
			c := commits[m.idx]
			notes = append(notes, fmt.Sprintf("%s at %s (%s)", label, c.SHA[:min(7, len(c.SHA))], behindText(m.idx, m.res)))
		}
	}
	c := commits[worst.idx]
	dep.Commit, dep.Subject, dep.CommitTime, dep.Behind = c.SHA, c.Subject, c.Time, worst.idx
	if len(notes) > 0 {
		dep.Note = strings.Join(notes, "; ")
	} else if !dep.Exact {
		dep.Note = fmt.Sprintf("closest of the last %d commits; %d of %d files differ (edited on the server, or older than that)", len(commits), dep.Differing, dep.Compared)
	}
	setStatus(dep)
	return nil
}

func behindText(idx int, res treeResult) string {
	s := "matches main"
	if idx > 0 {
		s = fmt.Sprintf("%d behind", idx)
	}
	if res.differing > 0 {
		s += fmt.Sprintf(", %d of %d files differ", res.differing, res.compared)
	}
	return s
}

// subtreeKey identifies the content that matters: commits that only touch
// files outside the deployed folders share a key and a result.
func subtreeKey(root string, entries []github.Entry, dirs []config.TreeDir) string {
	parts := []string{}
	for _, td := range dirs {
		prefix := strings.Trim(td.Prefix, "/")
		if prefix == "" {
			parts = append(parts, "root="+root)
			continue
		}
		sha := "missing"
		for _, e := range entries {
			if e.Path == prefix && e.Type == "tree" {
				sha = e.SHA
				break
			}
		}
		parts = append(parts, prefix+"="+sha)
	}
	return strings.Join(parts, ",")
}

func (d *Detector) compareTree(entries []github.Entry, dirs []config.TreeDir) treeResult {
	var res treeResult
	for _, e := range entries {
		if e.Type != "blob" {
			continue
		}
		for _, td := range dirs {
			rel, ok := under(e.Path, td.Prefix)
			if !ok {
				continue
			}
			res.compared++
			if d.hashFile(filepath.Join(td.Dir, filepath.FromSlash(rel)), e.Mode == "120000") != e.SHA {
				res.differing++
			}
			break
		}
	}
	return res
}

func under(path, prefix string) (string, bool) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return path, true
	}
	rel, ok := strings.CutPrefix(path, prefix+"/")
	return rel, ok
}

// hashFile computes a file's git blob id, cached by size and mtime.
func (d *Detector) hashFile(path string, symlink bool) string {
	fi, err := os.Lstat(path)
	if err != nil || (!fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0) {
		return "" // a FIFO or device must never be opened: it could block forever
	}
	d.mu.Lock()
	h, ok := d.hashes[path]
	d.mu.Unlock()
	if ok && h.size == fi.Size() && h.mod.Equal(fi.ModTime()) {
		if h.err {
			return ""
		}
		return h.sha
	}
	sha, err := blobSHA(path, symlink, fi.Size())
	d.mu.Lock()
	d.hashes[path] = fileHash{size: fi.Size(), mod: fi.ModTime(), sha: sha, err: err != nil}
	d.mu.Unlock()
	return sha
}

// blobSHA is sha1("blob <len>\x00" + content), git's object id for a file.
func blobSHA(path string, symlink bool, size int64) (string, error) {
	h := sha1.New()
	if symlink {
		target, err := os.Readlink(path)
		if err != nil {
			return "", fmt.Errorf("deploy: readlink: %w", err)
		}
		_, _ = io.WriteString(h, "blob "+strconv.Itoa(len(target))+"\x00"+target)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("deploy: open: %w", err)
	}
	defer f.Close()
	_, _ = io.WriteString(h, "blob "+strconv.FormatInt(size, 10)+"\x00")
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("deploy: read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
