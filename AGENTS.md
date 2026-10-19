# AGENTS.md

## Project
Go (1.27+) Windows-only tool: `cmd/OneDriveHelper` (LocalSystem service + admin CLI, console subsystem), `cmd/OneDriveHelperAgent` (per-user agent + popups + UAC approval step) and `cmd/OneDriveHelperConfig` (settings window + --import/--export/--validate). The agent and config tool **must be built with `-H=windowsgui`**. Only dependency: `golang.org/x/sys`.

## Commands
Go may not be on PATH in bash: use `"/c/Program Files/Go/bin/go.exe"`, or in PowerShell `$env:Path = "C:\Program Files\Go\bin;$env:Path"`.

- Format: `gofmt -w .`
- Vet: `go vet ./...`
- Test: `go test ./...`
- Full build (vet + test + amd64/arm64 into `dist\`): `.\build.ps1 -Version x.y.z` (PowerShell)
- Safe live checks on a dev box (no admin, no changes): `dist\amd64\OneDriveHelper.exe check`, `... remediate --dry-run`, `dist\amd64\OneDriveHelperAgent.exe test-popup`, `OneDriveHelperAgent.exe agent --standalone`

## Conventions / gotchas
- Never treat `HKCU\Software\SyncEngines\Providers\OneDrive` as proof a library is active (stale entries persist); active = Accounts\*\Tenants, ScopeIdToMountPointPathCache, UserFolder, HKLM SyncRootManager.
- `libraryScope` lines in `%LOCALAPPDATA%\Microsoft\OneDrive\settings\BusinessN\*.ini` are UTF-16LE without BOM; field 8/9/10 = SiteID/WebID/ListID.
- Remediation must never follow junctions/symlinks; use `internal/fsutil.ReadDir` and the handle-holding walker in `internal/remediate/tree.go`.
- Alerts are custom Win32 popups (`internal/popup`), NOT Windows toasts (user requirement). All window work happens on the popup manager's locked UI thread; other goroutines use `Manager.Show/Hide`. Button actions run on new goroutines.
- Settings: add new ones only as fields on `config.Config` with `reg`, `def`, `label`, `help` (and `min`/`max` for ints) tags. The registry, JSON import/export, validation and the config tool window are all driven from those tags. Place new fields in the `layout` in `internal/configui/form.go`.
- The service image path must stay explicitly quoted (`service.QuotePath`). `%ProgramData%\OneDriveHelper` must stay admin-owned (`logx.PrepareProgramDataDir`).
- Remediation: the UAC-elevated step (`remediate-approve`) only approves; file work runs as the user because it writes into the user's profile (`{Profile}\ODHelper\Saved\<date-time>\<Library>`). Never move file operations into the elevated process.
- Status export: the agent writes a `status.Snapshot` to `HKCU\Software\OneDriveHelper\State\Status` after every `tick`; the service reads it from `HKU\<SID>` as **untrusted** input (`status.ParseSnapshot` caps and clamps; user name and SID come from the session token). The service writes `%ProgramData%\OneDriveHelper\status.json`, `HKLM\SOFTWARE\OneDriveHelper\Status`, and the NinjaOne fields. Keep `internal/status` free of `x/sys/windows` imports: it (and `internal/ninja`) are the only packages whose tests run on Linux, so `go test ./internal/status ./internal/ninja` works in any CI; everything else needs Windows (`GOOS=windows go test -c` at least compiles them).
- NinjaOne: only `internal/ninja` runs `ninjarmm-cli.exe`, from its fixed path under `%ProgramData%\NinjaRMMAgent`, never from PATH or the registry. Field names are settings (`NinjaField*`); the service pushes on fingerprint change or every `NinjaPushMinutes`, and pulls `NinjaConfigField` on the same cadence. `ninjaone/` holds the NinjaOne scripts and the tenant setup guide; keep it in step with the settings.
