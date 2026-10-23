// Package config loads the JSON file that describes the box: which projects
// exist and where their units, ports, env files and backups live.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Config is the whole file. One copy is read by both the server and the probe.
type Config struct {
	Listen       string     `json:"listen"`
	SnapshotPath string     `json:"snapshot_path"`
	SysstatDir   string     `json:"sysstat_dir"`
	Disks        []string   `json:"disks"`
	TLSAddr      string     `json:"tls_addr"`
	Nginx        []NginxLog `json:"nginx_logs"`
	Ports        []PortSpec `json:"ports"`
	Projects     []Project  `json:"projects"`
	Intervals    Intervals  `json:"intervals"`
	UserAgent    string     `json:"user_agent"`
	GitHubAPI    string     `json:"github_api"`
	Journalctl   string     `json:"journalctl"`
	Systemctl    string     `json:"systemctl"`
	Sadf         string     `json:"sadf"`
	ProcRoot     string     `json:"proc_root"`
}

// Intervals are how often each collector runs. Zero means the default.
type Intervals struct {
	Fast   Duration `json:"fast"`   // host, units, ports
	Checks Duration `json:"checks"` // HTTP health
	Slow   Duration `json:"slow"`   // GitHub, TLS, sysstat
	Probe  Duration `json:"probe"`  // privileged probe cycle
}

// NginxLog is one access log. Logs whose format ends in "$host" get per-site counts.
type NginxLog struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// PortSpec is a port reserved for something that isn't a project (sshd, postgres).
type PortSpec struct {
	Port    int    `json:"port"`
	Project string `json:"project"`
	Label   string `json:"label"`
	Public  bool   `json:"public"`
}

// Project is one app on the box.
type Project struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Units       []string     `json:"units"`
	Ports       []PortSpec   `json:"ports"`
	Checks      []CheckSpec  `json:"checks"`
	Hosts       []string     `json:"hosts"` // TLS hostnames and nginx sites
	Deploy      DeploySpec   `json:"deploy"`
	Statics     []Static     `json:"static_sites"`
	Backups     []BackupSpec `json:"backups"`
	EnvFiles    []EnvSpec    `json:"env_files"`
	EnvCopies   []string     `json:"env_copy_globs"`
}

// CheckSpec is one URL to GET.
type CheckSpec struct {
	URL    string `json:"url"`
	Expect int    `json:"expect"`
}

// DeploySpec says how to find the deployed commit.
type DeploySpec struct {
	Repo   string    `json:"repo"`   // owner/name on GitHub
	Branch string    `json:"branch"` // default main
	Method string    `json:"method"` // go_binary, git_dir, tree, none
	Binary string    `json:"binary"`
	GitDir string    `json:"git_dir"`
	Trees  []TreeDir `json:"trees"`
	// DeployedAt lists paths whose newest mtime/ctime is the last deploy time.
	DeployedAt []string `json:"deployed_at"`
	Private    bool     `json:"private"` // repo is private: no GitHub API calls
}

// TreeDir maps a folder in the repo to a folder on disk.
type TreeDir struct {
	Dir    string `json:"dir"`
	Prefix string `json:"prefix"`
}

// Static is a directory nginx serves directly.
type Static struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// BackupSpec is one backup job's output folder.
type BackupSpec struct {
	Label  string   `json:"label"`
	Dir    string   `json:"dir"`
	Glob   string   `json:"glob"`
	MaxAge Duration `json:"max_age"`
}

// EnvSpec is one env file. Secret files hold a single value and are never opened.
type EnvSpec struct {
	Path        string `json:"path"`
	Secret      bool   `json:"secret_file"`
	ExpectOwner string `json:"expect_owner"`
	ExpectMode  string `json:"expect_mode"`
}

// Duration reads "90s" style strings from JSON.
type Duration struct{ time.Duration }

// UnmarshalJSON accepts a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("config: duration must be a string like \"90s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalJSON writes the duration back as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Load reads and validates the config file, filling defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(b)
}

// Parse decodes and validates config bytes. Unknown fields are errors, so a
// typo can't silently disable a check.
func Parse(b []byte) (*Config, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	c.defaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) defaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8400"
	}
	if c.SnapshotPath == "" {
		c.SnapshotPath = "/var/lib/vps-dashboard/probe.json"
	}
	if c.SysstatDir == "" {
		c.SysstatDir = "/var/log/sysstat"
	}
	if len(c.Disks) == 0 {
		c.Disks = []string{"/"}
	}
	if c.TLSAddr == "" {
		c.TLSAddr = "127.0.0.1:443"
	}
	if c.UserAgent == "" {
		c.UserAgent = "vps-dashboard (+health check)"
	}
	if c.GitHubAPI == "" {
		c.GitHubAPI = "https://api.github.com"
	}
	if c.Journalctl == "" {
		c.Journalctl = "journalctl"
	}
	if c.Systemctl == "" {
		c.Systemctl = "systemctl"
	}
	if c.Sadf == "" {
		c.Sadf = "sadf"
	}
	if c.ProcRoot == "" {
		c.ProcRoot = "/proc"
	}
	setDefault(&c.Intervals.Fast, 10*time.Second)
	setDefault(&c.Intervals.Checks, 60*time.Second)
	setDefault(&c.Intervals.Slow, 10*time.Minute)
	setDefault(&c.Intervals.Probe, 60*time.Second)
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.Name == "" {
			p.Name = p.ID
		}
		if p.Deploy.Branch == "" {
			p.Deploy.Branch = "main"
		}
		if p.Deploy.Method == "" {
			p.Deploy.Method = "none"
		}
		for j := range p.Checks {
			if p.Checks[j].Expect == 0 {
				p.Checks[j].Expect = 200
			}
		}
		for j := range p.Backups {
			setDefault(&p.Backups[j].MaxAge, 26*time.Hour)
			if p.Backups[j].Glob == "" {
				p.Backups[j].Glob = "*"
			}
		}
		for j := range p.EnvFiles {
			if p.EnvFiles[j].ExpectMode == "" {
				p.EnvFiles[j].ExpectMode = "0600"
			}
		}
	}
}

func setDefault(d *Duration, v time.Duration) {
	if d.Duration <= 0 {
		d.Duration = v
	}
}

func (c *Config) validate() error {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("config: listen %q: %w", c.Listen, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("config: listen %q must be a loopback address; view it over an SSH tunnel", c.Listen)
	}
	seen := map[string]bool{}
	ports := map[int]string{}
	for _, p := range c.Ports {
		if err := claimPort(ports, p.Port, p.Project); err != nil {
			return err
		}
	}
	for _, p := range c.Projects {
		if p.ID == "" {
			return errors.New("config: every project needs an id")
		}
		if seen[p.ID] {
			return fmt.Errorf("config: duplicate project id %q", p.ID)
		}
		seen[p.ID] = true
		for _, pt := range p.Ports {
			if err := claimPort(ports, pt.Port, p.ID); err != nil {
				return err
			}
		}
		switch p.Deploy.Method {
		case "none":
		case "go_binary":
			if p.Deploy.Binary == "" {
				return fmt.Errorf("config: project %s: go_binary needs binary", p.ID)
			}
		case "git_dir":
			if p.Deploy.GitDir == "" {
				return fmt.Errorf("config: project %s: git_dir needs git_dir", p.ID)
			}
		case "tree":
			if len(p.Deploy.Trees) == 0 || p.Deploy.Repo == "" {
				return fmt.Errorf("config: project %s: tree needs repo and trees", p.ID)
			}
		default:
			return fmt.Errorf("config: project %s: unknown deploy method %q", p.ID, p.Deploy.Method)
		}
	}
	return nil
}

func claimPort(ports map[int]string, port int, owner string) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("config: port %d out of range", port)
	}
	if prev, ok := ports[port]; ok {
		return fmt.Errorf("config: port %d claimed by both %s and %s", port, prev, owner)
	}
	ports[port] = owner
	return nil
}

// ProjectFor returns the project that owns a unit, or "".
func (c *Config) ProjectFor(unit string) string {
	for _, p := range c.Projects {
		for _, u := range p.Units {
			if u == unit || u+".service" == unit {
				return p.ID
			}
		}
	}
	return ""
}

// ListenPort is the port this dashboard itself uses.
func (c *Config) ListenPort() string {
	_, port, _ := net.SplitHostPort(c.Listen)
	return port
}
