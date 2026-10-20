package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geekbrownbear/OneDriveHelper/internal/onedrive"
	"github.com/geekbrownbear/OneDriveHelper/internal/remediate"
	"github.com/geekbrownbear/OneDriveHelper/internal/status"
)

func TestBuildSnapshot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := Status{
		Session: 2, Configured: true, Running: false, OneDriveVersion: "25.1.1",
		Accounts: []onedrive.Account{
			{Key: "Business1", UserEmail: "d@corp.example", UserFolder: `C:\Users\d\OneDrive - Corp`, SyncedFolders: []string{`C:\Users\d\Corp\A - Documents`}},
			{Key: "Personal"}, // not configured: left out
		},
		Orphans:          sampleOrphans(),
		OrphanSnooze:     SnoozeInfo{Active: true, UntilReboot: true},
		NotRunningSnooze: SnoozeInfo{Active: true, Until: now.Add(time.Hour)},
	}
	st := agentState{missingSince: now.Add(-3 * time.Minute), outageLogged: true, restarts: []time.Time{now.Add(-time.Hour), now.Add(-time.Minute)}}
	fix := &status.LastFix{At: now, OK: true}
	snap := buildSnapshot(`CORP\d`, "S-1-5-21-1", s, st, fix, now)

	if snap.Schema != status.Schema || snap.User != `CORP\d` || snap.SID != "S-1-5-21-1" || snap.Session != 2 {
		t.Errorf("identity: %+v", snap)
	}
	if !snap.OneDrive.Configured || snap.OneDrive.Running || !snap.OneDrive.Alerting || snap.OneDrive.Version != "25.1.1" {
		t.Errorf("onedrive: %+v", snap.OneDrive)
	}
	if snap.OneDrive.MissingSince == nil || !snap.OneDrive.MissingSince.Equal(st.missingSince) {
		t.Errorf("missingSince: %v", snap.OneDrive.MissingSince)
	}
	if snap.AutoRestart.Attempts != 1 || snap.AutoRestart.Last == nil || !snap.AutoRestart.Last.Equal(now.Add(-time.Minute)) {
		t.Errorf("auto restart (only the attempt inside the window counts): %+v", snap.AutoRestart)
	}
	if len(snap.Accounts) != 1 || snap.Accounts[0].Email != "d@corp.example" || len(snap.Accounts[0].Libraries) != 1 {
		t.Errorf("accounts: %+v", snap.Accounts)
	}
	if len(snap.Orphans) != 2 || snap.Orphans[0].Kind != "library" || snap.Orphans[1].Kind != "emptyOrg" || snap.Orphans[1].Name != "OLD LTD (empty organisation folder)" {
		t.Errorf("orphans: %+v", snap.Orphans)
	}
	if len(snap.Ignored) != 2 || snap.Ignored[0].Alert != "notRunning" || snap.Ignored[0].Until == nil || snap.Ignored[1].Alert != "orphans" || !snap.Ignored[1].UntilReboot {
		t.Errorf("ignored: %+v", snap.Ignored)
	}
	if snap.LastFix != fix {
		t.Error("last fix not carried over")
	}
	if _, err := snap.Marshal(); err != nil {
		t.Fatal(err)
	}
}

func TestLatestManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, res remediate.Result) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res)
		if err := os.WriteFile(filepath.Join(p, "manifest.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if d, f := latestManifest(dir, "", nil); d != "" || f != nil {
		t.Errorf("empty dir: %q %v", d, f)
	}
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	write("2026-01-02_030405", remediate.Result{Started: started, Finished: started.Add(time.Minute), RunDir: "old",
		Outcomes: []remediate.Outcome{{Removed: true, Copied: remediate.Stats{LocalFiles: 3}}}})
	write("2026-03-04_050607", remediate.Result{Started: started, RunDir: "new",
		Outcomes: []remediate.Outcome{{Removed: true, Copied: remediate.Stats{LocalFiles: 10}}, {Removed: false, Error: "kept"}}})
	_ = os.MkdirAll(filepath.Join(dir, "not-a-run"), 0o755)

	d, f := latestManifest(dir, "", nil)
	if d != "2026-03-04_050607" || f == nil {
		t.Fatalf("got %q %v", d, f)
	}
	if f.OK || f.RunDir != "new" || f.Folders != 2 || f.Removed != 1 || f.FilesSaved != 10 || f.Errors != 1 || !f.At.Equal(started) {
		t.Errorf("summary: %+v", f)
	}
	// Cached: same dir returns the cached pointer without re-reading.
	if d2, f2 := latestManifest(dir, d, f); d2 != d || f2 != f {
		t.Error("cache not used")
	}
}
