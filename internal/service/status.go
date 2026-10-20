package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/geekbrownbear/OneDriveHelper/internal/config"
	"github.com/geekbrownbear/OneDriveHelper/internal/logx"
	"github.com/geekbrownbear/OneDriveHelper/internal/onedrive"
	"github.com/geekbrownbear/OneDriveHelper/internal/status"
	"github.com/geekbrownbear/OneDriveHelper/internal/version"
)

// StatusKeyPath is HKLM\SOFTWARE\OneDriveHelper\Status: a few plain values an
// RMM script can read without parsing JSON.
const StatusKeyPath = config.KeyPath + `\Status`

// StatusFile is the full machine report.
func StatusFile() string { return filepath.Join(logx.ProgramDataDir(), status.FileName) }

// sessionSID returns the SID and DOMAIN\name of the user in a session, via
// the same token the service uses to start the agent.
func sessionSID(session uint32) (sid, name string, err error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(session, &tok); err != nil {
		return "", "", fmt.Errorf("WTSQueryUserToken(%d): %w", session, err)
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid = u.User.Sid.String()
	name = sid
	if acct, dom, _, err := u.User.Sid.LookupAccount(""); err == nil {
		name = dom + `\` + acct
	}
	return sid, name, nil
}

// readSnapshot reads a user's status snapshot from HKEY_USERS\<sid>. The
// hive belongs to the user, so everything in it is untrusted.
func readSnapshot(sid string) (snap *status.Snapshot, updated *time.Time, agent string, err error) {
	k, err := onedrive.UserHive(sid).Open(status.SnapshotKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil, status.AgentMissing, nil
	}
	defer k.Close()
	// Size first: a user could store megabytes here.
	n, _, err := k.GetValue(status.ValueJSON, nil)
	if err != nil {
		return nil, nil, status.AgentMissing, nil
	}
	if n > 2*status.MaxSnapshotBytes { // UTF-16 on disk
		return nil, nil, status.AgentInvalid, fmt.Errorf("snapshot value is %d bytes", n)
	}
	js, _, err := k.GetStringValue(status.ValueJSON)
	if err != nil {
		return nil, nil, status.AgentInvalid, err
	}
	s, err := status.ParseSnapshot([]byte(js))
	if err != nil {
		return nil, nil, status.AgentInvalid, err
	}
	if ts, _, err := k.GetIntegerValue(status.ValueUpdatedAt); err == nil && ts > 0 && ts < 1<<40 {
		t := time.Unix(int64(ts), 0)
		updated = &t
	}
	return &s, updated, status.AgentOK, nil
}

// buildReport assembles the machine report from the known sessions.
func (h *handler) buildReport(cfg config.Config, now time.Time) status.Report {
	host, _ := os.Hostname()
	r := status.Report{
		Schema: status.Schema, GeneratedAt: now, Hostname: host, HelperVersion: version.Version,
		ServiceStarted: h.started, CheckIntervalSeconds: cfg.CheckIntervalSeconds, Notes: append([]string(nil), h.notes...),
	}
	for _, ig := range onedrive.IgnoredFolders() {
		r.IgnoredFolders = append(r.IgnoredFolders, status.IgnoredFolder{Path: ig.Path, Note: ig.Note})
	}
	if v, perMachine := onedrive.MachineClientVersion(); v != "" {
		r.OneDriveInstall = status.Install{Version: v, Type: "per-user"}
		if perMachine {
			r.OneDriveInstall.Type = "per-machine"
		}
	}
	ids := make([]uint32, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		st := h.sessions[id]
		u := status.User{Session: id, SID: st.sid, Name: st.user, Agent: status.AgentMissing}
		if st.sid == "" {
			u.Name = fmt.Sprintf("session %d", id)
		} else {
			snap, updated, agent, err := readSnapshot(st.sid)
			if err != nil && !st.snapErrLogged {
				h.log.Printf("status of %s (session %d): %v", st.user, id, err)
				st.snapErrLogged = true
			}
			u.Agent, u.UpdatedAt = agent, updated
			if snap != nil {
				snap.SID, snap.Session, snap.User = st.sid, id, st.user // never trust these from the hive
				u.Snapshot = snap
				if r.OneDriveInstall.Version == "" && snap.OneDrive.Version != "" {
					r.OneDriveInstall = status.Install{Version: snap.OneDrive.Version, Type: "per-user"}
				}
			}
		}
		r.Users = append(r.Users, u)
	}
	status.Evaluate(&r, now, status.StaleAfter(cfg.CheckInterval()))
	return r
}

// writeAtomic writes data to path via a temporary file and a rename, so a
// reader never sees a half-written file. A reader holding the file open
// without FILE_SHARE_DELETE blocks the rename briefly, hence the retries.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for i := 0; ; i++ {
		if err = os.Rename(tmp, path); err == nil || i >= 5 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// writeStatusKey publishes the headline values to HKLM\SOFTWARE\OneDriveHelper\Status.
func writeStatusKey(r status.Report) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, StatusKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	folders := make([]string, 0, len(r.StaleFolders))
	for _, f := range r.StaleFolders {
		folders = append(folders, f.User+"|"+f.Path)
	}
	if len(folders) == 0 {
		folders = []string{} // REG_MULTI_SZ needs at least an empty list, not nil
	}
	return errors.Join(
		k.SetStringValue("Health", string(r.Health)),
		k.SetStringValue("Summary", r.Summary),
		k.SetDWordValue("StaleFolderCount", uint32(r.StaleFolderCount)),
		k.SetStringsValue("StaleFolders", folders),
		k.SetDWordValue("Users", uint32(len(r.Users))),
		k.SetStringValue("UpdatedAt", r.GeneratedAt.Format(time.RFC3339)),
		k.SetQWordValue("UpdatedAtUnix", uint64(r.GeneratedAt.Unix())),
		k.SetStringValue("HelperVersion", r.HelperVersion),
	)
}

// RemoveStatus deletes the status file and registry key (uninstall).
func RemoveStatus() {
	_ = os.Remove(StatusFile())
	_ = os.Remove(StatusFile() + ".tmp")
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, StatusKeyPath)
}

// publishStatus runs after every reconcile: resolve SIDs, build the report,
// write the file and the registry key, then sync with NinjaOne.
func (h *handler) publishStatus(cfg config.Config) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Printf("panic in publishStatus: %v", r)
		}
	}()
	for id, st := range h.sessions {
		if st.sid != "" {
			continue
		}
		sid, name, err := sessionSID(id)
		if err != nil {
			if !st.sidErrLogged {
				h.log.Printf("identify user of session %d: %v", id, err)
				st.sidErrLogged = true
			}
			continue
		}
		st.sid, st.user = sid, name
	}
	now := time.Now()
	h.pullNinjaConfig(cfg, now)
	cfg = config.Load() // the pull may have changed settings
	r := h.buildReport(cfg, now)

	b, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = writeAtomic(StatusFile(), b)
	}
	if err == nil {
		err = writeStatusKey(r)
	}
	if err != nil {
		if !h.writeErrLogged {
			h.log.Printf("write status: %v", err)
			h.writeErrLogged = true
		}
	} else {
		h.writeErrLogged = false
	}
	h.pushNinja(cfg, r, now)
}

// ReadReport loads the machine report written by the service.
func ReadReport() (status.Report, error) {
	var r status.Report
	b, err := os.ReadFile(StatusFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, fmt.Errorf("%s does not exist yet (is the %s service running?)", StatusFile(), version.ServiceName)
		}
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", StatusFile(), err)
	}
	if strings.TrimSpace(string(r.Health)) == "" {
		return r, fmt.Errorf("%s has no health value", StatusFile())
	}
	return r, nil
}
