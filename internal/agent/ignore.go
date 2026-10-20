package agent

import (
	"fmt"
	"html"
	"net/url"
	"os"
	"strings"

	"github.com/geekbrownbear/OneDriveHelper/internal/config"
	"github.com/geekbrownbear/OneDriveHelper/internal/elog"
	"github.com/geekbrownbear/OneDriveHelper/internal/onedrive"
	"github.com/geekbrownbear/OneDriveHelper/internal/winapi"
)

// requestIgnore is the "I want to keep this folder" link under a folder in
// the alert. It warns, then starts an e-mail to support asking for the
// folder to be put on the ignore list. The user only has to press Send.
// Nothing changes on the PC: the decision stays with the technician, who
// adds the folder with "OneDriveHelper.exe ignore add" (or the NinjaOne
// script).
func (a *Agent) requestIgnore(o onedrive.Orphan) {
	cfg := config.Load()
	addr := cfg.SupportEmailAddress()
	ctrl := a.ui.CtrlHeld() // Ctrl+click: copy the mailto: link instead of opening the mail program
	text := "Ignoring a folder that has stopped syncing can cause unforeseen issues: anything saved in it is NOT backed up, " +
		"and nobody will be warned about it again.\n\n" +
		"But keeping it can be a valid business need, for example a copy of an archived site that you still use for reference.\n\n" +
		"Folder:\n" + o.Path + "\n\n"
	if addr == "" {
		winapi.Info(cfg.Title(), text+"To have this folder excluded from monitoring, "+strings.TrimSuffix(cfg.SupportContact(), ".")+
			" and give them the folder path above.")
		return
	}
	if !winapi.Confirm(cfg.Title(), text+"Click OK to start an e-mail to "+addr+" asking for this folder to be excluded from monitoring. "+
		"You just need to add why you need the folder and press Send.") {
		return
	}
	host, _ := os.Hostname()
	subject := "Please stop monitoring a OneDrive folder on " + host
	plain := fmt.Sprintf("Hello,\r\n\r\nOneDrive Helper reports that this folder has stopped syncing, but I want to keep it:\r\n\r\n"+
		"    %s\r\n\r\nWhy I need it: (please fill in)\r\n\r\n"+
		"I understand that files saved in this folder are not backed up to OneDrive.\r\n\r\n"+
		"Computer: %s\r\nUser: %s\r\n\r\n"+
		"--- For the technician ---\r\n"+
		"Run the NinjaOne script \"Ignore OneDrive folder\" on this device with:\r\n"+
		"  folder: %s\r\n  note:   <ticket number>, approved by <who>\r\n"+
		"or, from a remote shell:\r\n"+
		"  \"C:\\Program Files\\OneDriveHelper\\OneDriveHelper.exe\" ignore add \"%s\" --note \"<ticket>\"\r\n",
		o.Path, host, a.fullName, o.Path, o.Path)
	htmlBody := "<p>Hello,</p><p>OneDrive Helper reports that this folder has stopped syncing, but I want to keep it:</p>" +
		"<p style=\"margin-left:2em\"><b>" + html.EscapeString(o.Path) + "</b></p>" +
		"<p>Why I need it: <i>(please fill in)</i></p>" +
		"<p>I understand that files saved in this folder are not backed up to OneDrive.</p>" +
		"<p>Computer: " + html.EscapeString(host) + "<br>User: " + html.EscapeString(a.fullName) + "</p>" +
		"<hr><p style=\"color:#666\"><b>For the technician:</b> run the NinjaOne script <i>Ignore OneDrive folder</i> on this device with " +
		"<code>folder</code> = <code>" + html.EscapeString(o.Path) + "</code> and a <code>note</code> (ticket number, who approved), " +
		"or from a remote shell:<br><code>\"C:\\Program Files\\OneDriveHelper\\OneDriveHelper.exe\" ignore add \"" + html.EscapeString(o.Path) + "\" --note \"&lt;ticket&gt;\"</code></p>"

	a.log.Printf("user asked support to ignore %s", o.Path)
	a.ev.Info(elog.IgnoreRequested, fmt.Sprintf("%s asked support to stop monitoring %s (they want to keep the folder). An e-mail to %s was started.", a.fullName, o.Path, addr))

	link := "mailto:" + addr + "?subject=" + url.QueryEscape(subject) + "&body=" + url.QueryEscape(plain)
	link = strings.ReplaceAll(link, "+", "%20")
	if ctrl {
		a.copyMailto(cfg, link)
		return
	}
	method := strings.ToLower(cfg.SupportEmailMethod)
	if method == "draft" || (method != "link" && winapi.HasFileHandler(".eml")) {
		path, err := writeSupportEML(emlDraft(addr, subject, plain, htmlBody))
		if err == nil {
			if err = winapi.ShellOpen(path); err == nil {
				return
			}
		}
		a.log.Printf("ignore request draft: %v; falling back to a mailto: link", err)
	}
	if err := winapi.ShellOpen(link); err != nil {
		a.log.Printf("ignore request mailto: %v", err)
		winapi.Info(cfg.Title(), "No e-mail program could be opened. Please e-mail "+addr+" and ask for this folder to be excluded from monitoring:\n\n"+o.Path)
	}
}
