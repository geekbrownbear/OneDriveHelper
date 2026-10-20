// Package status defines what the agent reports about one user and what the
// service aggregates for the whole machine, plus the roll-up rules and the
// renderers for NinjaOne. It deliberately has no Windows dependencies so its
// tests run anywhere.
//
// Flow: every agent writes a Snapshot to its own hive
// (HKCU\Software\OneDriveHelper\State\Status). The service reads every
// signed-in user's snapshot, builds a Report, writes it to
// %ProgramData%\OneDriveHelper\status.json and pushes the NinjaOne custom
// fields. The snapshot is written by the user, so the service treats it as
// untrusted input: ParseSnapshot caps its size and clamps every field.
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Schema is bumped when the JSON shape changes incompatibly.
const Schema = 1

// Registry location of a user's snapshot, relative to the user's hive.
const (
	SnapshotKey    = `Software\OneDriveHelper\State\Status`
	ValueJSON      = "Json"      // REG_SZ: the Snapshot as JSON
	ValueUpdatedAt = "UpdatedAt" // REG_QWORD: Unix seconds
)

// FileName is the machine report under %ProgramData%\OneDriveHelper.
const FileName = "status.json"

// Limits applied to untrusted snapshots.
const (
	MaxSnapshotBytes = 256 << 10
	MaxString        = 1024
	MaxAccounts      = 16
	MaxLibraries     = 200
	MaxOrphans       = 200
	MaxIgnored       = 8
)

// Roll-up tuning.
const (
	// FailedFixWindow: a failed fix keeps a user Critical for this long. The
	// folder it failed on is still reported as an orphan anyway.
	FailedFixWindow = 7 * 24 * time.Hour
	// MinStaleAfter is the floor for "snapshot too old" so a short sleep or a
	// slow logon doesn't flag a user as Stale.
	MinStaleAfter = 5 * time.Minute
)

// Health is the roll-up value shown in NinjaOne's ODHealth dropdown.
type Health string

const (
	Healthy  Health = "Healthy"
	Warning  Health = "Warning"
	Critical Health = "Critical"
	NoUser   Health = "NoUser" // nobody signed in
	Stale    Health = "Stale"  // an agent is not reporting
)

// AllHealth lists the values in precedence order (worst first).
var AllHealth = []Health{Critical, Warning, Stale, Healthy, NoUser}

func (h Health) rank() int {
	for i, x := range AllHealth {
		if x == h {
			return i
		}
	}
	return len(AllHealth)
}

// Worse reports whether h outranks o.
func (h Health) Worse(o Health) bool { return h.rank() < o.rank() }

// Agent states for a user row.
const (
	AgentOK      = "ok"
	AgentMissing = "missing" // no snapshot in the hive (agent never ran)
	AgentStale   = "stale"   // snapshot too old (agent stopped)
	AgentInvalid = "invalid" // snapshot unreadable
)

// Snapshot is what one agent reports about its user.
type Snapshot struct {
	Schema       int           `json:"schema"`
	User         string        `json:"user"` // DOMAIN\name
	SID          string        `json:"sid"`
	Session      uint32        `json:"session"`
	AgentVersion string        `json:"agentVersion"`
	At           time.Time     `json:"at"`
	OneDrive     OneDriveState `json:"oneDrive"`
	AutoRestart  AutoRestart   `json:"autoRestart"`
	Accounts     []Account     `json:"accounts"`
	Orphans      []Orphan      `json:"orphans"`
	Ignored      []Ignored     `json:"ignored,omitempty"`
	LastFix      *LastFix      `json:"lastFix,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"` // lists were cut to the limits
}

// OneDriveState is the OneDrive.exe state for the user.
type OneDriveState struct {
	Configured   bool       `json:"configured"`
	Running      bool       `json:"running"`
	Updating     bool       `json:"updating"`
	MissingSince *time.Time `json:"missingSince,omitempty"`
	// Alerting: OneDrive has been missing past the grace period (event 3001
	// written; automatic restarts or the alert are in progress).
	Alerting bool   `json:"alerting"`
	Version  string `json:"version,omitempty"`
}

// AutoRestart summarises automatic restarts in the last 30 minutes.
type AutoRestart struct {
	Attempts int        `json:"attempts"`
	Last     *time.Time `json:"last,omitempty"`
	GaveUp   bool       `json:"gaveUp"`
}

// Account is a signed-in OneDrive account.
type Account struct {
	Key       string   `json:"key"` // Business1, Personal...
	Email     string   `json:"email,omitempty"`
	Folder    string   `json:"folder"`
	Libraries []string `json:"libraries,omitempty"` // synced SharePoint/Teams library folders
}

// Orphan is a folder on disk that OneDrive no longer syncs.
type Orphan struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // library, emptyOrg, personal
	CanResync bool   `json:"canResync"`
	SiteURL   string `json:"siteUrl,omitempty"`
	// Protected: contains a redirected Desktop/Documents; never fixed automatically.
	Protected bool `json:"protected,omitempty"`
}

// Ignored is an alert the user chose to ignore.
type Ignored struct {
	Alert       string     `json:"alert"` // notRunning, orphans
	Until       *time.Time `json:"until,omitempty"`
	UntilReboot bool       `json:"untilReboot,omitempty"`
}

// LastFix summarises the newest remediation run (from its manifest.json).
type LastFix struct {
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	RunDir     string    `json:"runDir"`
	Folders    int       `json:"folders"`
	Removed    int       `json:"removed"`
	FilesSaved int       `json:"filesSaved"`
	Errors     int       `json:"errors"`
	ApprovedBy string    `json:"approvedBy,omitempty"`
}

// Report is the machine-wide status the service writes.
type Report struct {
	Schema               int       `json:"schema"`
	GeneratedAt          time.Time `json:"generatedAt"`
	Hostname             string    `json:"hostname"`
	HelperVersion        string    `json:"helperVersion"`
	ServiceStarted       time.Time `json:"serviceStarted"`
	OneDriveInstall      Install   `json:"oneDriveInstall"`
	CheckIntervalSeconds int       `json:"checkIntervalSeconds"`

	Health           Health        `json:"health"`
	Summary          string        `json:"summary"`
	StaleFolderCount int           `json:"staleFolderCount"`
	StaleFolders     []StaleFolder `json:"staleFolders"`
	Users            []User        `json:"users"`
	// Notes are machine-level remarks shown under the table (e.g. a settings
	// problem from NinjaOne).
	Notes []string `json:"notes,omitempty"`
}

// Install describes the OneDrive client installation.
type Install struct {
	Type    string `json:"type,omitempty"` // per-machine, per-user, ""
	Version string `json:"version,omitempty"`
}

// StaleFolder is one orphaned folder with its user, for the ticket.
type StaleFolder struct {
	User string `json:"user"`
	Path string `json:"path"`
	Name string `json:"name"`
}

// User is one signed-in user's row in the report.
type User struct {
	Session   uint32     `json:"session"`
	SID       string     `json:"sid"`
	Name      string     `json:"name"` // resolved by the service, never from the snapshot
	Agent     string     `json:"agent"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Health    Health     `json:"health"`
	Problem   string     `json:"problem,omitempty"` // one line, "" when healthy
	Snapshot  *Snapshot  `json:"snapshot,omitempty"`
}

// ParseSnapshot decodes an untrusted snapshot, rejecting oversized or
// malformed input and clamping every field to the limits.
func ParseSnapshot(b []byte) (Snapshot, error) {
	var s Snapshot
	if len(b) > MaxSnapshotBytes {
		return s, fmt.Errorf("snapshot is %d bytes (limit %d)", len(b), MaxSnapshotBytes)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("snapshot is not valid JSON: %w", err)
	}
	if s.Schema > Schema {
		return s, fmt.Errorf("snapshot schema %d is newer than this service understands (%d)", s.Schema, Schema)
	}
	if s.Schema == 0 {
		return s, errors.New("snapshot has no schema")
	}
	s.Sanitize()
	return s, nil
}

// Sanitize clamps strings and lists in place. Used on the untrusted side and
// by the agent before writing, so both ends agree on the limits.
func (s *Snapshot) Sanitize() {
	s.User = clamp(s.User)
	s.SID = clamp(s.SID)
	s.AgentVersion = clamp(s.AgentVersion)
	s.OneDrive.Version = clamp(s.OneDrive.Version)
	if len(s.Accounts) > MaxAccounts {
		s.Accounts, s.Truncated = s.Accounts[:MaxAccounts], true
	}
	for i := range s.Accounts {
		a := &s.Accounts[i]
		a.Key, a.Email, a.Folder = clamp(a.Key), clamp(a.Email), clamp(a.Folder)
		if len(a.Libraries) > MaxLibraries {
			a.Libraries, s.Truncated = a.Libraries[:MaxLibraries], true
		}
		for j := range a.Libraries {
			a.Libraries[j] = clamp(a.Libraries[j])
		}
	}
	if len(s.Orphans) > MaxOrphans {
		s.Orphans, s.Truncated = s.Orphans[:MaxOrphans], true
	}
	for i := range s.Orphans {
		o := &s.Orphans[i]
		o.Path, o.Name, o.Kind, o.SiteURL = clamp(o.Path), clamp(o.Name), clamp(o.Kind), clamp(o.SiteURL)
	}
	if len(s.Ignored) > MaxIgnored {
		s.Ignored, s.Truncated = s.Ignored[:MaxIgnored], true
	}
	for i := range s.Ignored {
		s.Ignored[i].Alert = clamp(s.Ignored[i].Alert)
	}
	if s.LastFix != nil {
		s.LastFix.RunDir = clamp(s.LastFix.RunDir)
		s.LastFix.ApprovedBy = clamp(s.LastFix.ApprovedBy)
	}
}

// clamp truncates to MaxString runes and drops control characters.
func clamp(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if n++; n > MaxString {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// StaleAfter is how old a snapshot may be before its user counts as Stale.
func StaleAfter(checkInterval time.Duration) time.Duration {
	if d := 3 * checkInterval; d > MinStaleAfter {
		return d
	}
	return MinStaleAfter
}

// Marshal encodes a snapshot for the registry.
func (s Snapshot) Marshal() ([]byte, error) {
	s.Schema = Schema
	return json.Marshal(s)
}
