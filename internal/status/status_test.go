package status

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

func ptr(t time.Time) *time.Time { return &t }

func okSnapshot(name string) Snapshot {
	return Snapshot{
		Schema: Schema, User: `CORP\` + name, SID: "S-1-5-21-1-2-3-1001", Session: 1, AgentVersion: "1.0.0", At: t0,
		OneDrive: OneDriveState{Configured: true, Running: true, Version: "25.100.0001"},
		Accounts: []Account{{Key: "Business1", Email: name + "@corp.example", Folder: `C:\Users\` + name + `\OneDrive - Corp`,
			Libraries: []string{`C:\Users\` + name + `\Corp\Projects - Documents`}}},
	}
}

func user(name string, s Snapshot) User {
	return User{Session: s.Session, SID: s.SID, Name: s.User, Agent: AgentOK, UpdatedAt: ptr(t0), Snapshot: &s}
}

func eval(users ...User) Report {
	r := Report{GeneratedAt: t0, Hostname: "PC01", HelperVersion: "1.0.0", Users: users}
	Evaluate(&r, t0.Add(30*time.Second), StaleAfter(60*time.Second))
	return r
}

func TestRollUpNoUser(t *testing.T) {
	r := eval()
	if r.Health != NoUser || r.Summary != "No user signed in" || r.StaleFolderCount != 0 {
		t.Errorf("got %s %q %d", r.Health, r.Summary, r.StaleFolderCount)
	}
}

func TestRollUpHealthy(t *testing.T) {
	r := eval(user("jsmith", okSnapshot("jsmith")))
	if r.Health != Healthy || r.Users[0].Problem != "" {
		t.Errorf("got %s %q", r.Health, r.Users[0].Problem)
	}
	if r.Summary != "Everything looks good (1 user)" {
		t.Errorf("summary %q", r.Summary)
	}
}

func TestRollUpOrphansCritical(t *testing.T) {
	s := okSnapshot("jsmith")
	s.Orphans = []Orphan{{Path: `C:\Users\jsmith\Corp\Old - Documents`, Name: "Old - Documents", Kind: "library"},
		{Path: `C:\Users\jsmith\Corp\Archive - Documents`, Name: "Archive - Documents", Kind: "library", CanResync: true}}
	r := eval(user("jsmith", s))
	if r.Health != Critical || r.StaleFolderCount != 2 {
		t.Fatalf("got %s %d", r.Health, r.StaleFolderCount)
	}
	if r.StaleFolders[0].Path != `C:\Users\jsmith\Corp\Archive - Documents` || r.StaleFolders[0].User != `CORP\jsmith` {
		t.Errorf("stale folders not sorted/attributed: %+v", r.StaleFolders)
	}
	if !strings.Contains(r.Users[0].Problem, "2 folders not syncing") {
		t.Errorf("problem %q", r.Users[0].Problem)
	}
	if !strings.Contains(r.Summary, "2 folders not syncing") {
		t.Errorf("summary %q", r.Summary)
	}
}

func TestRollUpFailedFixWindow(t *testing.T) {
	s := okSnapshot("a")
	s.LastFix = &LastFix{At: t0.Add(-time.Hour), OK: false, Folders: 1}
	if r := eval(user("a", s)); r.Health != Critical {
		t.Errorf("recent failed fix: %s", r.Health)
	}
	s.LastFix.At = t0.Add(-8 * 24 * time.Hour)
	if r := eval(user("a", s)); r.Health != Healthy {
		t.Errorf("old failed fix: %s", r.Health)
	}
}

func TestRollUpNotRunning(t *testing.T) {
	s := okSnapshot("a")
	s.OneDrive.Running = false
	// Missing but inside the grace period: not yet a problem.
	if r := eval(user("a", s)); r.Health != Healthy {
		t.Errorf("within grace: %s", r.Health)
	}
	s.OneDrive.Alerting = true
	r := eval(user("a", s))
	if r.Health != Warning || !strings.Contains(r.Users[0].Problem, "restarting automatically") {
		t.Errorf("alerting: %s %q", r.Health, r.Users[0].Problem)
	}
	if !strings.Contains(r.Summary, "OneDrive not running for 1 user") {
		t.Errorf("summary %q", r.Summary)
	}
	s.AutoRestart.GaveUp = true
	if r := eval(user("a", s)); r.Health != Warning || !strings.Contains(r.Users[0].Problem, "automatic restarts failed") {
		t.Errorf("gave up: %s %q", r.Health, r.Users[0].Problem)
	}
	s.OneDrive.Updating = true
	if r := eval(user("a", s)); r.Health != Healthy {
		t.Errorf("updating is not an outage: %s", r.Health)
	}
}

func TestRollUpIgnoredOnlyWhileProblemPresent(t *testing.T) {
	s := okSnapshot("a")
	s.Ignored = []Ignored{{Alert: "notRunning", Until: ptr(t0.Add(time.Hour))}}
	if r := eval(user("a", s)); r.Health != Healthy {
		t.Errorf("ignored with OneDrive running: %s", r.Health)
	}
	s.OneDrive.Running = false
	if r := eval(user("a", s)); r.Health != Warning {
		t.Errorf("ignored with OneDrive down: %s", r.Health)
	}
}

func TestRollUpStale(t *testing.T) {
	u := user("a", okSnapshot("a"))
	u.UpdatedAt = ptr(t0.Add(-10 * time.Minute))
	r := eval(u)
	if r.Health != Stale || !strings.Contains(r.Users[0].Problem, "last reported") {
		t.Errorf("old snapshot: %s %q", r.Health, r.Users[0].Problem)
	}
	u.UpdatedAt = ptr(t0.Add(10 * time.Minute)) // clock stepped back: treat as fresh
	if r := eval(u); r.Health != Healthy {
		t.Errorf("future snapshot: %s", r.Health)
	}
	for _, st := range []string{AgentMissing, AgentInvalid} {
		m := User{Session: 2, SID: "S-1-5-21-9", Name: `CORP\b`, Agent: st}
		if r := eval(m); r.Health != Stale || r.Summary != "1 user not reporting" {
			t.Errorf("%s: %s %q", st, r.Health, r.Summary)
		}
	}
}

func TestRollUpPrecedence(t *testing.T) {
	crit := okSnapshot("a")
	crit.Orphans = []Orphan{{Path: `C:\x`, Name: "x", Kind: "library"}}
	stale := User{Session: 2, SID: "S-2", Name: `CORP\b`, Agent: AgentMissing}
	warn := okSnapshot("c")
	warn.OneDrive.Running, warn.OneDrive.Alerting = false, true
	if r := eval(stale, user("c", warn)); r.Health != Warning {
		t.Errorf("warning+stale: %s", r.Health)
	}
	if r := eval(stale, user("c", warn), user("a", crit)); r.Health != Critical {
		t.Errorf("critical+warning+stale: %s", r.Health)
	}
	if r := eval(stale, user("a", okSnapshot("a"))); r.Health != Stale {
		t.Errorf("healthy+stale: %s", r.Health)
	}
}

func TestUnconfiguredUserIsHealthy(t *testing.T) {
	s := okSnapshot("svc")
	s.OneDrive = OneDriveState{}
	s.Accounts = nil
	r := eval(user("svc", s))
	if r.Health != Healthy || r.Users[0].Problem != "OneDrive is not set up for this user" {
		t.Errorf("got %s %q", r.Health, r.Users[0].Problem)
	}
}

func TestParseSnapshotRoundTrip(t *testing.T) {
	s := okSnapshot("a")
	s.Orphans = []Orphan{{Path: `C:\p`, Name: "p", Kind: "library"}}
	s.LastFix = &LastFix{At: t0, OK: true, Folders: 1, Removed: 1}
	b, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(got)
	if !bytes.Equal(b, gb) {
		t.Errorf("round trip changed the snapshot:\n%s\n%s", b, gb)
	}
}

func TestParseSnapshotRejects(t *testing.T) {
	if _, err := ParseSnapshot(bytes.Repeat([]byte("x"), MaxSnapshotBytes+1)); err == nil {
		t.Error("oversized accepted")
	}
	if _, err := ParseSnapshot([]byte("{not json")); err == nil {
		t.Error("bad JSON accepted")
	}
	if _, err := ParseSnapshot([]byte(`{"user":"x"}`)); err == nil {
		t.Error("missing schema accepted")
	}
	if _, err := ParseSnapshot([]byte(`{"schema":99}`)); err == nil {
		t.Error("future schema accepted")
	}
	if _, err := ParseSnapshot([]byte(`{"schema":1,"somethingNew":true}`)); err != nil {
		t.Errorf("unknown field rejected: %v", err)
	}
}

func TestSanitizeClamps(t *testing.T) {
	s := okSnapshot("a")
	s.User = strings.Repeat("u", MaxString+50) + "\x00\x07tail"
	for i := 0; i < MaxOrphans+5; i++ {
		s.Orphans = append(s.Orphans, Orphan{Path: "C:\\p\r\n" + string(rune('a'+i%26)), Name: "n"})
	}
	s.Sanitize()
	if n := len([]rune(s.User)); n != MaxString+1 || strings.ContainsAny(s.User, "\x00\x07") {
		t.Errorf("user not clamped: len %d", n)
	}
	if len(s.Orphans) != MaxOrphans || !s.Truncated {
		t.Errorf("orphans not capped: %d truncated=%v", len(s.Orphans), s.Truncated)
	}
	if strings.ContainsAny(s.Orphans[0].Path, "\r\n") {
		t.Errorf("control characters kept: %q", s.Orphans[0].Path)
	}
}

func TestFingerprintIgnoresTimestamps(t *testing.T) {
	a := eval(user("a", okSnapshot("a")))
	b := a
	b.GeneratedAt = t0.Add(time.Hour)
	b.Users = []User{a.Users[0]}
	b.Users[0].UpdatedAt = ptr(t0.Add(time.Hour))
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("timestamps changed the fingerprint")
	}
	s := okSnapshot("a")
	s.Orphans = []Orphan{{Path: `C:\x`, Name: "x", Kind: "library"}}
	c := eval(user("a", s))
	if Fingerprint(a) == Fingerprint(c) {
		t.Error("an orphan did not change the fingerprint")
	}
}

func TestRenderHTML(t *testing.T) {
	s := okSnapshot("jsmith")
	s.Orphans = []Orphan{{Path: `C:\Users\jsmith\Corp\<Old> - Documents`, Name: "Old", Kind: "library", CanResync: true}}
	s.Ignored = []Ignored{{Alert: "orphans", UntilReboot: true}}
	s.LastFix = &LastFix{At: t0.Add(-time.Hour), OK: false, Folders: 2, Removed: 1, FilesSaved: 40, Errors: 1}
	stale := User{Session: 3, SID: "S-3", Name: `CORP\gone`, Agent: AgentMissing}
	r := eval(user("jsmith", s), stale)
	r.Notes = []string{"ODHelperConfig: SupportEmail \"x\" is not a valid e-mail address"}
	h := RenderHTML(r, time.UTC)
	for _, want := range []string{"Critical", `CORP\jsmith`, "&lt;Old&gt; - Documents", "(can re-sync)", "folders not syncing until restart",
		"FAILED", "1/2 removed, 40 files saved, 1 errors", `CORP\gone`, "not running for this user", "ODHelperConfig", "PC01", "2026-10-19 09:00"} {
		if !strings.Contains(h, want) {
			t.Errorf("HTML lacks %q", want)
		}
	}
	for _, bad := range []string{"<script", "<Old>", "\n"} {
		if strings.Contains(h, bad) {
			t.Errorf("HTML contains %q", bad)
		}
	}
	empty := RenderHTML(eval(), time.UTC)
	if !strings.Contains(empty, "No user signed in") || !strings.Contains(empty, "NoUser") {
		t.Errorf("empty report: %s", empty)
	}
}

func TestRenderHTMLCapsRows(t *testing.T) {
	var users []User
	for i := 0; i < 400; i++ {
		s := okSnapshot("user" + strings.Repeat("x", 40))
		for j := 0; j < 20; j++ {
			s.Orphans = append(s.Orphans, Orphan{Path: strings.Repeat("p", 200), Name: "n"})
		}
		users = append(users, user("u", s))
	}
	h := RenderHTML(eval(users...), time.UTC)
	if len(h) > MaxHTMLBytes {
		t.Errorf("HTML is %d bytes", len(h))
	}
	if !strings.Contains(h, "more users") {
		t.Error("no truncation note")
	}
}

func TestWriteTableAndStaleText(t *testing.T) {
	s := okSnapshot("a")
	s.Orphans = []Orphan{{Path: `C:\p`, Name: "p", Kind: "library"}}
	r := eval(user("a", s))
	var buf bytes.Buffer
	if err := WriteTable(&buf, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PC01", "Health: Critical", `CORP\a`, "running", `C:\p`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, buf.String())
		}
	}
	if got := StaleFolderText(r); got != `CORP\a: C:\p` {
		t.Errorf("stale text %q", got)
	}
}

func TestStaleAfter(t *testing.T) {
	if StaleAfter(60*time.Second) != 5*time.Minute || StaleAfter(10*time.Minute) != 30*time.Minute {
		t.Error("StaleAfter")
	}
}
