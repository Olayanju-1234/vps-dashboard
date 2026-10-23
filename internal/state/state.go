// Package state holds the JSON shapes shared by the probe, the server and the
// frontend. Nothing here may carry an environment variable's value.
package state

import "time"

// Snapshot is what the privileged probe writes to disk for the server to read.
type Snapshot struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Version     string           `json:"version"`
	EnvFiles    []EnvFile        `json:"env_files"`
	EnvCopies   []FileMeta       `json:"env_copies"`
	Backups     []Backup         `json:"backups"`
	Journal     []ProjectJournal `json:"journal"`
	Nginx       []NginxSite      `json:"nginx"`
	Errors      []string         `json:"errors,omitempty"`
}

// FileMeta is a file's metadata, never its contents.
type FileMeta struct {
	Project string    `json:"project,omitempty"`
	Path    string    `json:"path"`
	Exists  bool      `json:"exists"`
	Owner   string    `json:"owner,omitempty"`
	Group   string    `json:"group,omitempty"`
	Mode    string    `json:"mode,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// EnvFile lists the variable names in one env file.
type EnvFile struct {
	FileMeta
	Vars        []EnvVar `json:"vars"`
	Unparsed    int      `json:"unparsed_lines"`
	SecretFile  bool     `json:"secret_file,omitempty"`
	ExpectOwner string   `json:"expect_owner,omitempty"`
	ExpectMode  string   `json:"expect_mode,omitempty"`
	UsedBy      []string `json:"used_by,omitempty"`
}

// EnvVar is a name and whether it has a non-empty value. There is
// deliberately no field that could hold the value.
type EnvVar struct {
	Name      string `json:"name"`
	Set       bool   `json:"set"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// Backup reports the newest file matching one backup job's pattern.
type Backup struct {
	Project     string    `json:"project"`
	Label       string    `json:"label"`
	Dir         string    `json:"dir"`
	Glob        string    `json:"glob"`
	MaxAge      string    `json:"max_age"`
	Count       int       `json:"count"`
	TotalBytes  int64     `json:"total_bytes"`
	Newest      string    `json:"newest,omitempty"`
	NewestBytes int64     `json:"newest_bytes"`
	NewestTime  time.Time `json:"newest_time,omitempty"`
	Status      string    `json:"status"` // ok, stale, missing, error
	Error       string    `json:"error,omitempty"`
}

// ProjectJournal is the recent problem lines for one project's units.
type ProjectJournal struct {
	Project  string         `json:"project"`
	Errors1h int            `json:"errors_1h"`
	Errors24 int            `json:"errors_24h"`
	Warns24  int            `json:"warnings_24h"`
	Lines    []JournalLine  `json:"lines"`
	PerHour  []int          `json:"per_hour"` // 24 buckets, oldest first
	Error    string         `json:"error,omitempty"`
	ByUnit   map[string]int `json:"by_unit,omitempty"`
}

// JournalLine is one redacted log line.
type JournalLine struct {
	Time    time.Time `json:"time"`
	Unit    string    `json:"unit"`
	Level   string    `json:"level"` // error, warning
	Message string    `json:"message"`
	Count   int       `json:"count"`
}

// NginxSite is request counts for one virtual host (or one log with no host field).
type NginxSite struct {
	Site     string    `json:"site"`
	Project  string    `json:"project,omitempty"`
	Log      string    `json:"log"`
	NoHost   bool      `json:"no_host,omitempty"`
	Total24  int       `json:"total_24h"`
	Status   [4]int    `json:"status_24h"` // 2xx, 3xx, 4xx, 5xx
	PerHour  []int     `json:"per_hour"`   // 24 buckets, oldest first
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// State is everything the server shows, assembled every few seconds.
type State struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Version     string      `json:"version"`
	Demo        bool        `json:"demo,omitempty"`
	Host        Host        `json:"host"`
	Projects    []Project   `json:"projects"`
	Ports       []Port      `json:"ports"`
	TLS         []Cert      `json:"tls"`
	Nginx       []NginxSite `json:"nginx"`
	EnvFiles    []EnvFile   `json:"env_files"`
	EnvCopies   []FileMeta  `json:"env_copies"`
	Probe       ProbeInfo   `json:"probe"`
	Alerts      []Alert     `json:"alerts"`
}

// ProbeInfo says whether the privileged half is alive.
type ProbeInfo struct {
	GeneratedAt time.Time `json:"generated_at,omitempty"`
	AgeSeconds  float64   `json:"age_seconds"`
	Stale       bool      `json:"stale"`
	Error       string    `json:"error,omitempty"`
	Errors      []string  `json:"errors,omitempty"`
}

// Host is the machine's current resource use.
type Host struct {
	Hostname      string     `json:"hostname"`
	Kernel        string     `json:"kernel"`
	CPUs          int        `json:"cpus"`
	UptimeSeconds float64    `json:"uptime_seconds"`
	CPUPercent    float64    `json:"cpu_percent"`
	StealPercent  float64    `json:"steal_percent"`
	IOWaitPercent float64    `json:"iowait_percent"`
	Load          [3]float64 `json:"load"`
	MemTotal      uint64     `json:"mem_total"`
	MemAvailable  uint64     `json:"mem_available"`
	SwapTotal     uint64     `json:"swap_total"`
	SwapFree      uint64     `json:"swap_free"`
	Disks         []Disk     `json:"disks"`
}

// Disk is one filesystem's usage.
type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Sample is one point of host history.
type Sample struct {
	T          int64   `json:"t"` // unix seconds
	CPU        float64 `json:"cpu"`
	MemUsedPct float64 `json:"mem"`
	SwapPct    float64 `json:"swap"`
}

// History is a time series for one range.
type History struct {
	Range   string   `json:"range"`
	Source  string   `json:"source"` // sampler or sysstat
	Step    int      `json:"step_seconds"`
	Samples []Sample `json:"samples"`
	Error   string   `json:"error,omitempty"`
}

// Project is one app on the box, with everything about it in one place.
type Project struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Repo        string         `json:"repo,omitempty"`
	Status      string         `json:"status"` // ok, warn, critical
	CPUPercent  float64        `json:"cpu_percent"`
	MemoryBytes uint64         `json:"memory_bytes"`
	Units       []Unit         `json:"units"`
	Ports       []Port         `json:"ports"`
	Checks      []Check        `json:"checks"`
	Deploy      Deploy         `json:"deploy"`
	Statics     []StaticSite   `json:"static_sites,omitempty"`
	Backups     []Backup       `json:"backups"`
	Journal     ProjectJournal `json:"journal"`
	EnvFiles    []string       `json:"env_files"`
}

// Unit is one systemd unit's runtime state.
type Unit struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Load        string    `json:"load"`
	Active      string    `json:"active"`
	Sub         string    `json:"sub"`
	Result      string    `json:"result,omitempty"`
	Enabled     string    `json:"enabled,omitempty"`
	MainPID     int       `json:"main_pid"`
	Restarts    int       `json:"restarts"`
	MemoryBytes uint64    `json:"memory_bytes"`
	MemoryPeak  uint64    `json:"memory_peak"`
	MemoryMax   uint64    `json:"memory_max"` // 0 = no limit
	CPUPercent  float64   `json:"cpu_percent"`
	Tasks       int       `json:"tasks"`
	ActiveSince time.Time `json:"active_since,omitempty"`
	LastTrigger time.Time `json:"last_trigger,omitempty"` // timers
	NextElapse  time.Time `json:"next_elapse,omitempty"`  // timers
	EnvFiles    []string  `json:"env_files,omitempty"`
}

// Port is one reserved port and whether something listens on it.
type Port struct {
	Port      int      `json:"port"`
	Project   string   `json:"project"`
	Label     string   `json:"label,omitempty"`
	Public    bool     `json:"public"`    // expected on 0.0.0.0
	Listening []string `json:"listening"` // bind addresses seen
	User      string   `json:"user,omitempty"`
	Status    string   `json:"status"` // ok, exposed, down, unlisted, unexpected
}

// Check is one HTTP health probe of a public URL.
type Check struct {
	URL       string    `json:"url"`
	Status    int       `json:"status"`
	Expect    int       `json:"expect"`
	OK        bool      `json:"ok"`
	LatencyMS float64   `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
}

// Deploy compares what is on disk with the repo's main branch.
type Deploy struct {
	Method       string    `json:"method,omitempty"` // go_binary, git_dir, tree
	Commit       string    `json:"commit,omitempty"`
	CommitTime   time.Time `json:"commit_time,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Exact        bool      `json:"exact"`
	Differing    int       `json:"differing_files"`
	DiffPaths    []string  `json:"differing_paths,omitempty"`
	Compared     int       `json:"compared_files"`
	Dirty        bool      `json:"dirty,omitempty"`
	DeployedAt   time.Time `json:"deployed_at,omitempty"`
	MainCommit   string    `json:"main_commit,omitempty"`
	MainSubject  string    `json:"main_subject,omitempty"`
	MainTime     time.Time `json:"main_time,omitempty"`
	MainSource   string    `json:"main_source,omitempty"` // github, local-ref
	Behind       int       `json:"behind"`                // -1 unknown
	Status       string    `json:"status"`                // current, behind, unknown, error
	Note         string    `json:"note,omitempty"`
	NeedsRestart bool      `json:"needs_restart,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// StaticSite is a directory nginx serves directly.
type StaticSite struct {
	Label      string    `json:"label"`
	Path       string    `json:"path"`
	DeployedAt time.Time `json:"deployed_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Cert is the certificate nginx serves for one hostname.
type Cert struct {
	Host        string    `json:"host"`
	Project     string    `json:"project,omitempty"`
	Subject     string    `json:"subject,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	Names       []string  `json:"names,omitempty"`
	NotAfter    time.Time `json:"not_after,omitempty"`
	DaysLeft    float64   `json:"days_left"`
	Valid       bool      `json:"valid"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Alert is one thing worth a look, worst first.
type Alert struct {
	Level   string `json:"level"` // critical, warning, info
	Project string `json:"project,omitempty"`
	Area    string `json:"area"`
	Message string `json:"message"`
}
