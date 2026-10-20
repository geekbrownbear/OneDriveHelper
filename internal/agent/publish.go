package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/geekbrownbear/OneDriveHelper/internal/config"
	"github.com/geekbrownbear/OneDriveHelper/internal/onedrive"
	"github.com/geekbrownbear/OneDriveHelper/internal/remediate"
	"github.com/geekbrownbear/OneDriveHelper/internal/status"
	"github.com/geekbrownbear/OneDriveHelper/internal/version"
)

// agentState is a copy of the fields guarded by Agent.mu that the snapshot needs.
type agentState struct {
	missingSince time.Time
	outageLogged bool
	autoGaveUp   bool
	restarts     []time.Time
}

func (a *Agent) state() agentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agentState{missingSince: a.missingSince, outageLogged: a.outageLogged, autoGaveUp: a.autoGaveUp,
		restarts: append([]time.Time(nil), a.restarts...)}
}

// buildSnapshot converts a check result into the status.Snapshot the service
// reads. Pure, so it is unit-tested with made-up data.
func buildSnapshot(user, sid string, s Status, st agentState, fix *status.LastFix, now time.Time) status.Snapshot {
	snap := status.Snapshot{
		Schema: status.Schema, User: user, SID: sid, Session: s.Session, AgentVersion: version.Version, At: now,
		OneDrive: status.OneDriveState{Configured: s.Configured, Running: s.Running, Unknown: s.Unknown, Updating: s.Updating,
			Alerting: st.outageLogged, Version: s.OneDriveVersion},
		AutoRestart:      status.AutoRestart{GaveUp: st.autoGaveUp},
		LastFix:          fix,
		InterruptedFixes: s.unfinished(),
	}
	if !s.Running && !st.missingSince.IsZero() {
		t := st.missingSince
		snap.OneDrive.MissingSince = &t
	}
	if recent := recentRestarts(st.restarts, now); len(recent) > 0 {
		last := recent[len(recent)-1]
		snap.AutoRestart.Attempts, snap.AutoRestart.Last = len(recent), &last
	}
	for _, acct := range s.Accounts {
		if !acct.Configured() {
			continue
		}
		snap.Accounts = append(snap.Accounts, status.Account{Key: acct.Key, Email: acct.UserEmail, Folder: acct.UserFolder,
			Libraries: append([]string(nil), acct.SyncedFolders...)})
	}
	for _, o := range s.Orphans {
		kind := "library"
		switch {
		case o.EmptyOrg:
			kind = "emptyOrg"
		case o.OldOneDrive:
			kind = "oldOneDrive"
		}
		snap.Orphans = append(snap.Orphans, status.Orphan{Path: o.Path, Name: o.DisplayName(), Kind: kind,
			CanResync: o.CanResync(), SiteURL: o.WebURL, Protected: len(o.KnownFolders) > 0})
	}
	for _, ig := range []struct {
		name string
		info SnoozeInfo
	}{{"notRunning", s.NotRunningSnooze}, {"orphans", s.OrphanSnooze}} {
		if !ig.info.Active {
			continue
		}
		x := status.Ignored{Alert: ig.name, UntilReboot: ig.info.UntilReboot}
		if !ig.info.Until.IsZero() {
			u := ig.info.Until
			x.Until = &u
		}
		snap.Ignored = append(snap.Ignored, x)
	}
	snap.Sanitize()
	return snap
}

// publish writes the snapshot to HKCU\Software\OneDriveHelper\State\Status.
// Failures are logged once, not every minute.
func (a *Agent) publish(snap status.Snapshot, now time.Time) {
	b, err := snap.Marshal()
	if err == nil && len(b) > status.MaxSnapshotBytes {
		// Far too many libraries: drop the lists, keep the counts meaningful.
		for i := range snap.Accounts {
			snap.Accounts[i].Libraries = nil
		}
		snap.Truncated = true
		b, err = snap.Marshal()
	}
	if err == nil {
		err = writeSnapshot(a.hive, b, now)
	}
	if err != nil {
		if !a.publishErrLogged {
			a.log.Printf("publish status: %v", err)
			a.publishErrLogged = true
		}
		return
	}
	a.publishErrLogged = false
}

func writeSnapshot(h onedrive.Hive, b []byte, now time.Time) error {
	k, err := h.Create(status.SnapshotKey)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(status.ValueJSON, string(b)); err != nil {
		return err
	}
	return k.SetQWordValue(status.ValueUpdatedAt, uint64(now.Unix()))
}

// lastFix returns the newest remediation run's summary, re-reading the
// manifest only when a new run folder appears.
func (a *Agent) lastFix(cfg config.Config) *status.LastFix {
	dir, fix := latestManifest(cfg.BackupDir(a.profile), a.lastFixDir, a.lastFixCached)
	a.lastFixDir, a.lastFixCached = dir, fix
	return fix
}

const runDirLayout = "2006-01-02_150405"

// latestManifest finds the newest run folder under backupDir. If it is the
// one already cached (cachedDir), cached is returned without reading.
func latestManifest(backupDir, cachedDir string, cached *status.LastFix) (string, *status.LastFix) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return "", nil
	}
	newest := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := time.ParseInLocation(runDirLayout, e.Name(), time.Local); err != nil {
			continue
		}
		if e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return "", nil
	}
	if newest == cachedDir {
		return cachedDir, cached
	}
	return newest, readManifest(filepath.Join(backupDir, newest, "manifest.json"))
}

func readManifest(path string) *status.LastFix {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil
	}
	var res remediate.Result
	if err := json.Unmarshal(b, &res); err != nil {
		return nil
	}
	return summariseResult(&res)
}

func summariseResult(res *remediate.Result) *status.LastFix {
	fix := &status.LastFix{At: res.Finished, OK: res.OK(), RunDir: res.RunDir, Folders: len(res.Outcomes), ApprovedBy: res.ApprovedBy}
	if fix.At.IsZero() {
		fix.At = res.Started
	}
	for _, o := range res.Outcomes {
		if o.Removed {
			fix.Removed++
		}
		if o.Error != "" {
			fix.Errors++
		}
		fix.FilesSaved += o.Copied.LocalFiles
	}
	return fix
}
