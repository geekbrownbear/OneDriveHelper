package status

import (
	"fmt"
	"html"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// MaxHTMLBytes keeps the WYSIWYG field well inside NinjaOne's limit. Rows
// beyond it are replaced by "... and N more users".
const MaxHTMLBytes = 60 << 10

const timeFmt = "2006-01-02 15:04"

// Colours for the health column and row tint.
var healthColour = map[Health]string{
	Healthy:  "#2e7d32",
	Warning:  "#ed6c02",
	Critical: "#c62828",
	Stale:    "#616161",
	NoUser:   "#616161",
}

var healthTint = map[Health]string{
	Healthy:  "#f1f8f1",
	Warning:  "#fff4e5",
	Critical: "#fdecea",
	Stale:    "#f5f5f5",
	NoUser:   "#f5f5f5",
}

// RenderHTML renders the report for NinjaOne's WYSIWYG field: inline styles
// only, no scripts, every value escaped. loc is the time zone for display
// (nil = the report's own zone).
func RenderHTML(r Report, loc *time.Location) string {
	if loc == nil {
		loc = r.GeneratedAt.Location()
	}
	e := html.EscapeString
	ts := func(t time.Time) string { return e(t.In(loc).Format(timeFmt)) }
	tsp := func(t *time.Time) string {
		if t == nil {
			return "–"
		}
		return ts(*t)
	}
	var b strings.Builder
	badge := func(h Health, text string) string {
		return fmt.Sprintf(`<span style="display:inline-block;padding:2px 8px;border-radius:10px;background:%s;color:#fff;font-weight:600">%s</span>`,
			healthColour[h], e(text))
	}
	fmt.Fprintf(&b, `<div style="font-family:Segoe UI,Arial,sans-serif;font-size:13px">`)
	fmt.Fprintf(&b, `<p style="margin:0 0 8px 0">%s &nbsp; <b>%s</b></p>`, badge(r.Health, string(r.Health)), e(r.Summary))

	const th = `<th style="text-align:left;padding:4px 8px;border-bottom:2px solid #ccc;white-space:nowrap">`
	const td = `<td style="padding:4px 8px;border-bottom:1px solid #e0e0e0;vertical-align:top">`
	b.WriteString(`<table style="border-collapse:collapse;width:100%">`)
	b.WriteString(`<tr>` + th + `User</th>` + th + `OneDrive</th>` + th + `Accounts / libraries</th>` + th + `Folders not syncing</th>` +
		th + `Ignored alerts</th>` + th + `Auto-restart</th>` + th + `Last fix</th>` + th + `Checked</th></tr>`)

	shown := 0
	for i, u := range r.Users {
		row := renderRow(u, loc, td, e, tsp)
		if b.Len()+len(row) > MaxHTMLBytes-2048 {
			fmt.Fprintf(&b, `<tr>%s<i>... and %d more users</i></td></tr>`, td, len(r.Users)-i)
			break
		}
		b.WriteString(row)
		shown++
	}
	if len(r.Users) == 0 {
		fmt.Fprintf(&b, `<tr>%s<i>No user signed in</i></td></tr>`, strings.Replace(td, "<td ", `<td colspan="8" `, 1))
	}
	b.WriteString(`</table>`)

	for _, n := range r.Notes {
		fmt.Fprintf(&b, `<p style="margin:6px 0 0 0;color:#c62828">%s</p>`, e(n))
	}
	inst := ""
	if r.OneDriveInstall.Version != "" {
		inst = fmt.Sprintf(" &nbsp;|&nbsp; OneDrive %s (%s)", e(r.OneDriveInstall.Version), e(or(r.OneDriveInstall.Type, "install type unknown")))
	}
	fmt.Fprintf(&b, `<p style="margin:8px 0 0 0;color:#616161;font-size:12px">%s &nbsp;|&nbsp; OneDrive Helper %s%s &nbsp;|&nbsp; reported %s</p>`,
		e(r.Hostname), e(r.HelperVersion), inst, ts(r.GeneratedAt))
	b.WriteString(`</div>`)
	return b.String()
}

func renderRow(u User, loc *time.Location, td string, e func(string) string, tsp func(*time.Time) string) string {
	var b strings.Builder
	tint := healthTint[u.Health]
	cell := func(s string) {
		b.WriteString(strings.Replace(td, "<td ", fmt.Sprintf(`<td style="background:%s;" `, tint), 1) + s + `</td>`)
	}
	b.WriteString(`<tr>`)

	// User
	name := e(u.Name)
	if u.Health != Healthy {
		name = fmt.Sprintf(`<span style="color:%s;font-weight:600">%s</span>`, healthColour[u.Health], name)
	}
	userCell := name + fmt.Sprintf(`<br><span style="color:#616161;font-size:11px">session %d</span>`, u.Session)
	if u.Problem != "" {
		userCell += `<br><span style="font-size:11px">` + e(u.Problem) + `</span>`
	}
	cell(userCell)

	s := u.Snapshot
	if s == nil || u.Agent != AgentOK {
		for i := 0; i < 6; i++ {
			cell("–")
		}
		cell(tsp(u.UpdatedAt))
		b.WriteString(`</tr>`)
		return b.String()
	}

	// OneDrive
	od := s.OneDrive
	var odText string
	switch {
	case !od.Configured:
		odText = "Not set up"
	case od.Running:
		odText = `<span style="color:#2e7d32">Running</span>`
	case od.Updating:
		odText = "Updating"
	default:
		odText = `<span style="color:#c62828;font-weight:600">Not running</span>`
		if od.MissingSince != nil {
			odText += "<br>since " + tsp(od.MissingSince)
		}
	}
	if od.Version != "" {
		odText += `<br><span style="color:#616161;font-size:11px">` + e(od.Version) + `</span>`
	}
	cell(odText)

	// Accounts / libraries
	var acc []string
	for _, a := range s.Accounts {
		label := a.Key
		if a.Email != "" {
			label = a.Email
		}
		acc = append(acc, fmt.Sprintf("%s (%s)", e(label), plural(len(a.Libraries), "library")))
	}
	cell(or(strings.Join(acc, "<br>"), "–"))

	// Folders not syncing
	var orphans []string
	for _, o := range s.Orphans {
		line := e(o.Path)
		switch {
		case o.Protected:
			line += ` <span style="color:#616161;font-size:11px">(contains Desktop/Documents)</span>`
		case o.CanResync:
			line += ` <span style="color:#616161;font-size:11px">(can re-sync)</span>`
		}
		orphans = append(orphans, line)
	}
	if s.Truncated && len(s.Orphans) >= MaxOrphans {
		orphans = append(orphans, "<i>list truncated</i>")
	}
	cell(or(strings.Join(orphans, "<br>"), "none"))

	// Ignored alerts
	var ig []string
	for _, x := range s.Ignored {
		label := map[string]string{"notRunning": "OneDrive not running", "orphans": "folders not syncing"}[x.Alert]
		if label == "" {
			label = x.Alert
		}
		until := "until restart"
		if x.Until != nil {
			until = "until " + tsp(x.Until)
		}
		ig = append(ig, e(label)+" "+until)
	}
	cell(or(strings.Join(ig, "<br>"), "–"))

	// Auto-restart
	ar := "–"
	if s.AutoRestart.Attempts > 0 || s.AutoRestart.Last != nil {
		ar = fmt.Sprintf("%s<br>last %s", plural(s.AutoRestart.Attempts, "attempt"), tsp(s.AutoRestart.Last))
		if s.AutoRestart.GaveUp {
			ar += `<br><span style="color:#c62828">gave up</span>`
		}
	}
	cell(ar)

	// Last fix
	fix := "–"
	if f := s.LastFix; f != nil {
		verdict := `<span style="color:#2e7d32">OK</span>`
		if !f.OK {
			verdict = `<span style="color:#c62828;font-weight:600">FAILED</span>`
		}
		fix = fmt.Sprintf("%s %s<br>%d/%d removed, %d files saved", tsp(&f.At), verdict, f.Removed, f.Folders, f.FilesSaved)
		if f.Errors > 0 {
			fix += fmt.Sprintf(", %d errors", f.Errors)
		}
	}
	cell(fix)

	cell(tsp(u.UpdatedAt))
	b.WriteString(`</tr>`)
	return b.String()
}

// WriteTable prints the report as text (for "OneDriveHelper.exe status").
func WriteTable(w io.Writer, r Report) error {
	loc := r.GeneratedAt.Location()
	ts := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return t.In(loc).Format(timeFmt)
	}
	fmt.Fprintf(w, "%s  OneDrive Helper %s  reported %s\n", r.Hostname, r.HelperVersion, r.GeneratedAt.In(loc).Format(timeFmt))
	fmt.Fprintf(w, "Health: %s  (%s)\n", r.Health, r.Summary)
	if r.OneDriveInstall.Version != "" {
		fmt.Fprintf(w, "OneDrive client: %s %s\n", r.OneDriveInstall.Version, r.OneDriveInstall.Type)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "USER\tSESSION\tHEALTH\tONEDRIVE\tACCOUNTS\tNOT SYNCING\tCHECKED\tPROBLEM")
	for _, u := range r.Users {
		od, acc, orph := "-", "-", "-"
		if s := u.Snapshot; s != nil && u.Agent == AgentOK {
			switch {
			case !s.OneDrive.Configured:
				od = "not set up"
			case s.OneDrive.Running:
				od = "running"
			case s.OneDrive.Updating:
				od = "updating"
			default:
				od = "NOT RUNNING"
			}
			acc = fmt.Sprintf("%d", len(s.Accounts))
			orph = fmt.Sprintf("%d", len(s.Orphans))
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", u.Name, u.Session, u.Health, od, acc, orph, ts(u.UpdatedAt), u.Problem)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(r.StaleFolders) > 0 {
		fmt.Fprintf(w, "\nFolders not syncing (%d):\n", len(r.StaleFolders))
		for _, f := range r.StaleFolders {
			fmt.Fprintf(w, "  %s  %s\n", f.User, f.Path)
		}
	}
	return nil
}

// StaleFolderText lists the stale folders one per line as "user: path", for
// a multi-line field or a ticket body.
func StaleFolderText(r Report) string {
	var lines []string
	for _, f := range r.StaleFolders {
		lines = append(lines, f.User+": "+f.Path)
	}
	return strings.Join(lines, "\n")
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
