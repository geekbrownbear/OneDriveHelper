// OneDriveHelper.exe is the LocalSystem service and the admin command line.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/sys/windows/svc"

	"github.com/geekbrownbear/OneDriveHelper/internal/config"
	"github.com/geekbrownbear/OneDriveHelper/internal/ninja"
	"github.com/geekbrownbear/OneDriveHelper/internal/onedrive"
	"github.com/geekbrownbear/OneDriveHelper/internal/remediate"
	"github.com/geekbrownbear/OneDriveHelper/internal/service"
	"github.com/geekbrownbear/OneDriveHelper/internal/status"
	"github.com/geekbrownbear/OneDriveHelper/internal/version"
	"github.com/geekbrownbear/OneDriveHelper/internal/winapi"
)

const usage = `OneDrive Helper %s

Usage: OneDriveHelper.exe <command>

Admin commands (run from an elevated prompt):
  install [--config <file.json>]
                          Copy to Program Files, register and start the service,
                          applying the settings file if given
  uninstall [--purge]     Remove the service (--purge also deletes configuration)
  start | stop            Start or stop the service
  config show             Show effective settings and where each comes from
  config defaults         Write any missing default settings to the registry
  config export <file>    Export HKLM\SOFTWARE\OneDriveHelper to JSON
  config import <file>    Validate and import settings from JSON
  config validate <file>  Check a settings file without applying it

  ignore add <folder> [--note "<why>"]
                          Keep a folder that stopped syncing: stop reporting it
                          and never include it in a fix (e.g. an archived site)
  ignore remove <folder>  Report the folder again
  ignore list             Show the ignore list
  ninja push [--dry-run]  Send the machine status to the NinjaOne custom fields now
  ninja pull [--dry-run]  Apply the settings held in the NinjaOne settings field now
  ninja check             Show whether the NinjaOne agent's command-line tool is usable

Diagnostics (run as the user to check):
  status [--json|--html]  Show the machine status the service reports to NinjaOne
  check                   Show OneDrive status and orphaned library folders
  remediate --dry-run     Show what the fix would do (no changes)
  version
`

func main() {
	if isSvc, err := svc.IsWindowsService(); err == nil && isSvc {
		if err := service.Run(); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Printf(usage, version.Version)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func logf(f string, a ...any) { fmt.Printf(f+"\n", a...) }

func needAdmin() error {
	if !winapi.IsElevated() {
		return fmt.Errorf("this command must be run from an elevated (Run as administrator) prompt")
	}
	return nil
}

func run(cmd string, args []string) error {
	switch strings.ToLower(cmd) {
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		cfgFile := fs.String("config", "", "JSON settings file to apply (from OneDriveHelperConfig.exe)")
		_ = fs.Parse(args)
		if err := needAdmin(); err != nil {
			return err
		}
		return service.Install(*cfgFile, logf)
	case "uninstall":
		fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
		purge := fs.Bool("purge", false, "also delete configuration")
		_ = fs.Parse(args)
		if err := needAdmin(); err != nil {
			return err
		}
		return service.Uninstall(*purge, logf)
	case "start":
		return service.Start()
	case "stop":
		return service.Stop()
	case "config":
		return configCmd(args)
	case "status":
		return statusCmd(args)
	case "ignore":
		return ignoreCmd(args)
	case "ninja":
		return ninjaCmd(args)
	case "check":
		return check()
	case "remediate":
		fs := flag.NewFlagSet("remediate", flag.ExitOnError)
		dry := fs.Bool("dry-run", false, "show the plan only")
		sid := fs.String("sid", "", "target another logged-on user (requires admin)")
		_ = fs.Parse(args)
		if !*dry {
			return fmt.Errorf("only --dry-run is supported here; users fix folders via the alert's 'Fix' button")
		}
		return dryRun(*sid)
	case "version", "--version", "-v":
		fmt.Println(version.Version)
		return nil
	case "help", "--help", "-h", "/?":
		fmt.Printf(usage, version.Version)
		return nil
	}
	return fmt.Errorf("unknown command %q (try 'help')", cmd)
}

func configCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"show"}
	}
	switch args[0] {
	case "show":
		_, settings := config.LoadDetailed()
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "SETTING\tVALUE\tSOURCE\tDESCRIPTION")
		for _, s := range settings {
			fmt.Fprintf(w, "%s\t%v\t%s\t%s\n", s.Name, s.Value, s.Source, s.Help)
		}
		return w.Flush()
	case "defaults":
		if err := needAdmin(); err != nil {
			return err
		}
		return config.WriteDefaults()
	case "export", "import", "validate":
		if len(args) < 2 {
			return fmt.Errorf("usage: config %s <file.json>", args[0])
		}
		switch args[0] {
		case "export":
			return config.Export(args[1])
		case "validate":
			if _, _, err := config.ReadFile(args[1]); err != nil {
				return err
			}
			fmt.Println("Settings file is valid.")
			return nil
		}
		if err := needAdmin(); err != nil {
			return err
		}
		return config.Import(args[1])
	}
	return fmt.Errorf("unknown config command %q", args[0])
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the raw status.json")
	asHTML := fs.Bool("html", false, "print the HTML table for the NinjaOne WYSIWYG field")
	_ = fs.Parse(args)
	if *asJSON {
		b, err := os.ReadFile(service.StatusFile())
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	}
	r, err := service.ReadReport()
	if err != nil {
		return err
	}
	if *asHTML {
		_, err = fmt.Print(status.RenderHTML(r, time.Local))
		return err
	}
	return status.WriteTable(os.Stdout, r)
}

func ignoreCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch strings.ToLower(args[0]) {
	case "list":
		list := onedrive.IgnoredFolders()
		if len(list) == 0 {
			fmt.Println("No folders are ignored.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "FOLDER\tNOTE")
		for _, f := range list {
			fmt.Fprintf(w, "%s\t%s\n", f.Path, f.Note)
		}
		return w.Flush()
	case "add":
		// Accept "--note" before or after the folder (scripts put the folder first).
		var folder, note string
		rest := args[1:]
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			switch {
			case strings.HasPrefix(a, "--note=") || strings.HasPrefix(a, "-note="):
				note = a[strings.Index(a, "=")+1:]
			case a == "--note" || a == "-note":
				if i+1 < len(rest) {
					note, i = rest[i+1], i+1
				}
			case folder == "":
				folder = a
			default:
				return fmt.Errorf("usage: ignore add <folder> [--note \"<why>\"] (got extra argument %q)", a)
			}
		}
		if folder == "" {
			return fmt.Errorf("usage: ignore add <folder> [--note \"<why>\"]")
		}
		if err := needAdmin(); err != nil {
			return err
		}
		if err := onedrive.AddIgnoredFolder(folder, note); err != nil {
			return err
		}
		fmt.Printf("Ignoring %s. It will disappear from alerts and the status within a minute.\n", folder)
		return nil
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: ignore remove <folder>")
		}
		if err := needAdmin(); err != nil {
			return err
		}
		if err := onedrive.RemoveIgnoredFolder(args[1]); err != nil {
			return err
		}
		fmt.Printf("%s is reported again.\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown ignore command %q (add, remove or list)", args[0])
}

func ninjaCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ninja push|pull|check")
	}
	cfg := config.Load()
	sub := strings.ToLower(args[0])
	fs := flag.NewFlagSet("ninja "+sub, flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "show what would be sent or applied without doing it")
	_ = fs.Parse(args[1:])
	cli, cliErr := ninja.Find()
	switch sub {
	case "check":
		if cliErr != nil {
			return cliErr
		}
		fmt.Printf("NinjaOne CLI: %s\nReporting enabled: %v\nPush at least every: %s\nFields:\n", cli.Path, cfg.NinjaEnabled, cfg.NinjaPush())
		for _, purpose := range []string{"status", "health", "staleCount", "lastReport", "config"} {
			if name, ok := cfg.NinjaFields()[purpose]; ok {
				fmt.Printf("  %-11s %s\n", purpose, name)
			}
		}
		if health := cfg.NinjaFields()["health"]; health != "" {
			opts, err := cli.Options(context.Background(), health)
			switch {
			case err != nil:
				fmt.Printf("Dropdown options of %s: not readable here (%v)\n", health, err)
			case len(opts) == 0:
				fmt.Printf("Dropdown options of %s: none; create Healthy, Warning, Critical, Stale, NoUser and NotInstalled\n", health)
			default:
				fmt.Printf("Dropdown options of %s:", health)
				for _, h := range status.AllHealth {
					if _, ok := opts[string(h)]; ok {
						fmt.Printf(" %s", h)
					} else {
						fmt.Printf(" %s(MISSING)", h)
					}
				}
				fmt.Println()
			}
		}
		if !winapi.IsElevated() {
			fmt.Println("Note: ninjarmm-cli only works for SYSTEM; run this from a NinjaOne remote shell to see field details. The service runs as SYSTEM.")
		}
		return nil
	case "push":
		r, err := service.ReadReport()
		if err != nil {
			return err
		}
		if *dry {
			fmt.Printf("Would set %d field(s) from %s (health %s, %d folder(s) not syncing):\n", len(cfg.NinjaFields())-boolToInt(cfg.NinjaConfigField != ""), service.StatusFile(), r.Health, r.StaleFolderCount)
			for purpose, name := range cfg.NinjaFields() {
				if purpose != "config" {
					fmt.Printf("  %-11s -> %s\n", purpose, name)
				}
			}
			fmt.Println("\nHTML that would go to the status field:")
			fmt.Println(status.RenderHTML(r, time.Local))
			return nil
		}
		if cliErr != nil {
			return cliErr
		}
		res, err := service.PushReport(context.Background(), cli, cfg, r)
		for name, v := range res.Fields {
			fmt.Printf("set %s = %s\n", name, v)
		}
		return err
	case "pull":
		if cliErr != nil {
			return cliErr
		}
		name := cfg.NinjaFields()["config"]
		if name == "" {
			return fmt.Errorf("NinjaConfigField is blank; nothing to pull")
		}
		if *dry {
			raw, err := cli.Get(context.Background(), name)
			if err != nil {
				return err
			}
			if strings.TrimSpace(raw) == "" {
				fmt.Printf("Field %s is empty; local settings would be kept.\n", name)
				return nil
			}
			c, present, err := config.ParseBytes([]byte(raw), config.LoadStored())
			if err == nil {
				err = c.Validate()
			}
			if err != nil {
				return fmt.Errorf("field %s: %w", name, err)
			}
			fmt.Printf("Field %s holds valid settings for: %s\n%s\n", name, strings.Join(present, ", "), strings.TrimSpace(raw))
			return nil
		}
		if err := needAdmin(); err != nil {
			return err
		}
		res, err := service.PullConfig(context.Background(), cli, cfg, "")
		if err != nil {
			return err
		}
		switch {
		case !res.Found:
			fmt.Printf("Field %s is empty; local settings kept.\n", name)
		default:
			fmt.Printf("Applied %d setting(s) from field %s: %s\n", len(res.Applied), name, strings.Join(res.Applied, ", "))
		}
		return nil
	}
	return fmt.Errorf("unknown ninja command %q (push, pull or check)", sub)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func check() error {
	h := onedrive.CurrentUserHive()
	session := winapi.CurrentSessionID()
	accts := onedrive.Accounts(h)
	fmt.Printf("Session %d\n\nAccounts:\n", session)
	for _, a := range accts {
		if a.Configured() {
			fmt.Printf("  %-10s %s  (%s)\n", a.Key, a.UserEmail, a.UserFolder)
			for _, f := range a.SyncedFolders {
				fmt.Printf("               synced: %s\n", f)
			}
		}
	}
	exe := onedrive.OneDriveExe(h)
	fmt.Printf("\nOneDrive.exe: %s\nRunning in this session: %v\nUpdate in progress: %v\n",
		exe, onedrive.OneDriveState(session), onedrive.UpdateInProgress())

	libs := onedrive.ReadLibraries(os.Getenv("LOCALAPPDATA"), accts)
	fmt.Printf("\nLibraries known to OneDrive (with re-sync IDs): %d\n", len(libs))
	for _, l := range libs {
		fmt.Printf("  %s - %s  ->  %s\n", l.SiteTitle, l.ListTitle, l.MountPoint)
	}

	profile, _ := os.UserHomeDir()
	in := onedrive.Gather(h, profile)
	orphans := onedrive.FindOrphans(in, onedrive.OSFS)
	if stale := onedrive.StaleNavEntries(h, in, nil); len(stale) > 0 {
		fmt.Printf("\nExplorer side-panel entries pointing at missing folders: %d (the agent removes these after 5 minutes)\n", len(stale))
		for _, e := range stale {
			fmt.Printf("  %s -> %s\n", e.Name, e.Target)
		}
	}
	fmt.Printf("\nOrphaned library folders: %d\n", len(orphans))
	for _, o := range orphans {
		fmt.Printf("  %s\n", o.Path)
		switch {
		case len(o.KnownFolders) > 0:
			fmt.Printf("    contains the user's %s folder - will not be fixed automatically\n", strings.Join(o.KnownFolders, "/"))
		case o.EmptyOrg:
			fmt.Printf("    empty leftover organisation folder (nothing to re-sync)\n")
		case o.OldOneDrive:
			fmt.Printf("    old work OneDrive folder that no signed-in account syncs (nothing to re-sync)\n")
		case o.CanResync():
			fmt.Printf("    re-sync: %s\n", o.Library.ResyncURL())
		case o.WebURL != "":
			fmt.Printf("    re-sync IDs unknown; site: %s\n", o.WebURL)
		default:
			fmt.Printf("    re-sync IDs unknown\n")
		}
	}
	if len(in.ReadErrors) > 0 {
		fmt.Printf("\nOneDrive data that could not be read (no folder will be fixed until this is resolved):\n")
		for _, e := range in.ReadErrors {
			fmt.Printf("  %s\n", e)
		}
	}
	if list := remediate.ListInterrupted(profile); len(list) > 0 {
		fmt.Printf("\nUnfinished fixes: %d\n", len(list))
		for _, i := range list {
			for _, l := range strings.Split(strings.TrimSpace(i.Text()), "\n") {
				fmt.Printf("  %s\n", l)
			}
		}
	}
	return nil
}

func dryRun(sid string) error {
	t, err := remediate.CurrentUserTarget()
	if sid != "" {
		t, err = remediate.TargetForSID(sid)
	}
	if err != nil {
		return err
	}
	plan, err := remediate.BuildPlan(t, config.Load())
	if err != nil {
		return err
	}
	if len(plan.Items) == 0 && len(plan.Excluded) == 0 && len(plan.Interrupted) == 0 {
		fmt.Println("No orphaned library folders found.")
		return nil
	}
	fmt.Print(plan.Summary())
	fmt.Printf("Enough space: %v\n", plan.EnoughSpace())
	for _, it := range plan.Items {
		for _, e := range it.Stats.Errors {
			fmt.Printf("  read error: %s\n", e)
		}
	}
	return nil
}
