package deploy

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
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
		return errors.New("deploy: binary has no vcs.revision (built outside a checkout?)")
	}
	dep.Exact = true
	return d.compareMain(ctx, spec, dep)
}
