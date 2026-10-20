package status

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Evaluate fills in every derived field of r: per-user health and problem,
// the stale-folder list, the roll-up health and the summary line. now is the
// reference time; staleAfter comes from StaleAfter(cfg.CheckInterval()).
func Evaluate(r *Report, now time.Time, staleAfter time.Duration) {
	r.Schema = Schema
	r.StaleFolders = r.StaleFolders[:0]
	for i := range r.Users {
		u := &r.Users[i]
		u.Health, u.Problem = UserHealth(*u, now, staleAfter)
		if u.Snapshot == nil || u.Agent != AgentOK {
			continue
		}
		for _, o := range u.Snapshot.Orphans {
			r.StaleFolders = append(r.StaleFolders, StaleFolder{User: u.Name, Path: o.Path, Name: o.Name})
		}
	}
	sort.SliceStable(r.StaleFolders, func(i, j int) bool {
		if r.StaleFolders[i].User != r.StaleFolders[j].User {
			return r.StaleFolders[i].User < r.StaleFolders[j].User
		}
		return r.StaleFolders[i].Path < r.StaleFolders[j].Path
	})
	r.StaleFolderCount = len(r.StaleFolders)
	r.Health = RollUp(r.Users)
	r.Summary = summary(r)
}

// UserHealth classifies one user. The second value is a short reason.
func UserHealth(u User, now time.Time, staleAfter time.Duration) (Health, string) {
	switch u.Agent {
	case AgentMissing:
		return Stale, "helper agent is not running for this user"
	case AgentInvalid:
		return Stale, "helper agent status could not be read"
	}
	if u.UpdatedAt == nil || u.Snapshot == nil {
		return Stale, "helper agent has not reported"
	}
	if age := now.Sub(*u.UpdatedAt); age > staleAfter {
		return Stale, fmt.Sprintf("helper agent last reported %s ago", roundDuration(age))
	}
	s := u.Snapshot
	var problems []string
	worst := Healthy
	bump := func(h Health, msg string) {
		if h.Worse(worst) {
			worst = h
		}
		problems = append(problems, msg)
	}
	if n := len(s.Orphans); n > 0 {
		bump(Critical, plural(n, "folder")+" not syncing")
	}
	if f := s.LastFix; f != nil && !f.OK && now.Sub(f.At) < FailedFixWindow {
		bump(Critical, "last fix failed "+f.At.Format("2006-01-02 15:04"))
	}
	if n := s.InterruptedFixes; n > 0 {
		bump(Critical, plural(n, "earlier fix")+" did not finish")
	}
	if s.OneDrive.Configured && !s.OneDrive.Running && !s.OneDrive.Updating {
		switch {
		case s.AutoRestart.GaveUp:
			bump(Warning, "OneDrive not running; automatic restarts failed")
		case s.OneDrive.Alerting:
			bump(Warning, "OneDrive not running; restarting automatically")
		}
	}
	for _, ig := range s.Ignored {
		switch ig.Alert {
		case "notRunning":
			if s.OneDrive.Configured && !s.OneDrive.Running {
				bump(Warning, "user is ignoring the 'OneDrive not running' alert")
			}
		case "orphans":
			if len(s.Orphans) > 0 {
				bump(Warning, "user is ignoring the 'folders not syncing' alert")
			}
		}
	}
	if !s.OneDrive.Configured {
		return Healthy, "OneDrive is not set up for this user"
	}
	return worst, strings.Join(problems, "; ")
}

// RollUp combines user healths: no users → NoUser; else the worst.
func RollUp(users []User) Health {
	if len(users) == 0 {
		return NoUser
	}
	worst := Healthy
	for _, u := range users {
		if u.Health.Worse(worst) {
			worst = u.Health
		}
	}
	return worst
}

func summary(r *Report) string {
	if len(r.Users) == 0 {
		return "No user signed in"
	}
	var parts []string
	if n := r.StaleFolderCount; n > 0 {
		parts = append(parts, plural(n, "folder")+" not syncing")
	}
	down, stale := 0, 0
	for _, u := range r.Users {
		if u.Health == Stale {
			stale++
		}
		if s := u.Snapshot; s != nil && u.Agent == AgentOK && s.OneDrive.Configured && !s.OneDrive.Running && !s.OneDrive.Updating && (s.OneDrive.Alerting || s.AutoRestart.GaveUp) {
			down++
		}
	}
	if down > 0 {
		parts = append(parts, fmt.Sprintf("OneDrive not running for %s", plural(down, "user")))
	}
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%s not reporting", plural(stale, "user")))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Everything looks good (%s)", plural(len(r.Users), "user"))
	}
	return strings.Join(parts, ", ")
}

// Fingerprint identifies the report's content, ignoring timestamps and
// other fields that change on every tick. The service pushes to NinjaOne
// only when it changes (and on a timer).
func Fingerprint(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%s|%s|%s|%s\n", r.Health, r.StaleFolderCount, r.HelperVersion, r.OneDriveInstall.Type, r.OneDriveInstall.Version, strings.Join(r.Notes, ";"))
	for _, ig := range r.IgnoredFolders {
		fmt.Fprintf(&b, "ignore:%s=%s\n", ig.Path, ig.Note)
	}
	for _, u := range r.Users {
		fmt.Fprintf(&b, "%s|%s|%s|%s|", u.Name, u.SID, u.Agent, u.Health)
		if s := u.Snapshot; s != nil && u.Agent == AgentOK {
			fmt.Fprintf(&b, "%v|%v|%v|%v|%v|%s|%v|%v|%d|", s.OneDrive.Configured, s.OneDrive.Running, s.OneDrive.Unknown, s.OneDrive.Updating, s.OneDrive.Alerting, s.OneDrive.Version, s.AutoRestart.GaveUp, s.AutoRestart.Attempts, s.InterruptedFixes)
			for _, a := range s.Accounts {
				fmt.Fprintf(&b, "%s=%s/%d,", a.Key, a.Email, len(a.Libraries))
			}
			b.WriteString("|")
			for _, o := range s.Orphans {
				fmt.Fprintf(&b, "%s,", o.Path)
			}
			b.WriteString("|")
			for _, ig := range s.Ignored {
				fmt.Fprintf(&b, "%s,", ig.Alert)
			}
			if f := s.LastFix; f != nil {
				fmt.Fprintf(&b, "|%s/%v", f.At.Format(time.RFC3339), f.OK)
			}
		}
		b.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
