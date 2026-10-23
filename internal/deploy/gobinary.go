package deploy

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// goBinary reads the commit Go stamps into every binary built inside a checkout.
func (d *Detector) goBinary(ctx context.Context, spec config.DeploySpec, dep *state.Deploy) error {
	info, err := buildinfo.ReadFile(spec.Binary)
	if err != nil {
		return fmt.Errorf("deploy: read build info: %w", err)
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			dep.Commit = s.Value
		case "vcs.time":
			dep.CommitTime, _ = time.Parse(time.RFC3339, s.Value)
		case "vcs.modified":
			dep.Dirty = s.Value == "true"
		}
	}
	if dep.Commit == "" {
		// Built with -buildvcs=false: look for "-X ...Version=<sha>" instead.
		dep.Commit = ldflagsCommit(info.Settings)
	}
	if dep.Commit == "" {
		return errors.New("deploy: binary carries no commit (no vcs.revision and no -X version=<sha>)")
	}
	dep.Exact = true
	return d.compareMain(ctx, spec, dep)
}

var ldflagsSHA = regexp.MustCompile(`-X[= ]\S*[Vv]ersion=([0-9a-f]{7,40})\b`)

func ldflagsCommit(settings []debug.BuildSetting) string {
	for _, s := range settings {
		if s.Key == "-ldflags" {
			if m := ldflagsSHA.FindStringSubmatch(s.Value); m != nil {
				return m[1]
			}
		}
	}
	return ""
}
