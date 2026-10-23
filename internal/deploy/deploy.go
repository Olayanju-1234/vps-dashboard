// Package deploy works out which commit is deployed and how it compares with
// the repo's main branch, without needing anything from the deploy process:
// a Go binary carries its commit, an rsynced checkout carries .git, and a
// `git archive` extract is matched file by file against recent commits.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/fsmeta"
	"github.com/olayanju-1234/vps-dashboard/internal/github"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Recent is how many commits on main are considered when matching a tree.
const Recent = 20

// Detector holds caches that make repeated detection cheap.
type Detector struct {
	gh *github.Client

	mu     sync.Mutex
	hashes map[string]fileHash
}

// New returns a detector that asks gh about remote branches.
func New(gh *github.Client) *Detector {
	return &Detector{gh: gh, hashes: map[string]fileHash{}}
}

// Detect fills a Deploy for one project.
func (d *Detector) Detect(ctx context.Context, spec config.DeploySpec) state.Deploy {
	dep := state.Deploy{Method: spec.Method, Behind: -1, Status: "unknown"}
	dep.DeployedAt = deployedAt(spec.DeployedAt)
	var err error
	switch spec.Method {
	case "go_binary":
		err = d.goBinary(ctx, spec, &dep)
	case "git_dir":
		err = d.gitDir(ctx, spec, &dep)
	case "tree":
		err = d.tree(ctx, spec, &dep)
	default:
		return dep
	}
	if err != nil {
		dep.Error = err.Error()
		if dep.Status == "unknown" && dep.Commit == "" {
			dep.Status = "error"
		}
	}
	return dep
}

func deployedAt(paths []string) time.Time {
	var newest time.Time
	for _, p := range paths {
		if t, err := fsmeta.Changed(p); err == nil && t.After(newest) {
			newest = t
		}
	}
	return newest
}

// compareMain fills the main-branch fields and the behind count for a known commit.
func (d *Detector) compareMain(ctx context.Context, spec config.DeploySpec, dep *state.Deploy) error {
	if spec.Repo == "" || spec.Private {
		return nil
	}
	commits, err := d.gh.Commits(ctx, spec.Repo, spec.Branch, Recent)
	if err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	if len(commits) == 0 {
		return errors.New("deploy: branch has no commits")
	}
	setMain(dep, commits[0], "github")
	for i, c := range commits {
		if sameCommit(c.SHA, dep.Commit) {
			dep.Behind = i
			dep.Commit = c.SHA
			if dep.Subject == "" {
				dep.Subject, dep.CommitTime = c.Subject, c.Time
			}
			setStatus(dep)
			return nil
		}
	}
	cmp, err := d.gh.Compare(ctx, spec.Repo, dep.Commit, commits[0].SHA)
	if err != nil {
		return fmt.Errorf("deploy: compare: %w", err)
	}
	dep.Behind = cmp.AheadBy
	if cmp.BehindBy > 0 {
		dep.Note = fmt.Sprintf("deployed commit is not on %s (%d commits only in the deploy)", spec.Branch, cmp.BehindBy)
	}
	if dep.Subject == "" {
		if c, err := d.gh.Commit(ctx, spec.Repo, dep.Commit); err == nil {
			dep.Subject, dep.CommitTime = c.Subject, c.Time
		}
	}
	setStatus(dep)
	return nil
}

func setMain(dep *state.Deploy, c github.Commit, source string) {
	dep.MainCommit, dep.MainSubject, dep.MainTime, dep.MainSource = c.SHA, c.Subject, c.Time, source
}

func setStatus(dep *state.Deploy) {
	switch {
	case dep.Behind == 0:
		dep.Status = "current"
	case dep.Behind > 0:
		dep.Status = "behind"
	}
}

// sameCommit compares a full SHA with a possibly abbreviated one.
func sameCommit(a, b string) bool {
	if len(a) < 7 || len(b) < 7 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}
