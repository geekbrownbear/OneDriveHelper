# Setting up OneDrive Helper in NinjaOne

This is the step-by-step for a NinjaOne tenant. When it's done, every managed PC reports its OneDrive health into custom fields, a device with folders that stopped syncing opens a ticket in your PSA through a policy condition, and the ticket closes itself when the user fixes it.

How the pieces fit:

```
OneDriveHelperAgent.exe (per user)  --->  HKCU\Software\OneDriveHelper\State\Status
        |                                            |
        | popups, Fix (admin), auto-restart          | read every 30 s
        v                                            v
   the user                            OneDriveHelper service (SYSTEM)
                                                     |
                     +-------------------------------+-------------------------------+
                     v                               v                               v
   C:\ProgramData\OneDriveHelper\status.json   HKLM\SOFTWARE\OneDriveHelper\Status   ninjarmm-cli.exe
                                                                                     |
                                                                        ODErrors, ODHealth,
                                                                        ODStaleFolders, ODLastReport
                                                                                     |
                                                                        policy conditions -> Halo ticket
```

The service also *reads* one field, `ODHelperConfig`, so branding and support contacts are set once per customer in NinjaOne.

## 1. Create the custom fields

**Administration → Devices → Global Custom Fields** (device scope), unless noted. The names must match exactly; they're what the service passes to `ninjarmm-cli`. If you prefer other names, change the matching settings (`NinjaFieldStatus` and friends) in `ODHelperConfig`.

| Name | Label | Type | Permissions | Holds |
|---|---|---|---|---|
| `ODErrors` | OneDrive Status | WYSIWYG | Technician: Read &nbsp;·&nbsp; Scripts: Read/Write | One row per signed-in user: OneDrive state, accounts, folders not syncing, ignored alerts, auto-restarts, last fix |
| `ODHealth` | OneDrive Health | Dropdown | Technician: Read &nbsp;·&nbsp; Scripts: Read/Write | `Healthy`, `Warning`, `Critical`, `Stale`, `NoUser`, `NotInstalled` |
| `ODStaleFolders` | OneDrive Stale Folders | Integer | Technician: Read &nbsp;·&nbsp; Scripts: Read/Write | Number of library folders that stopped syncing, all users |
| `ODLastReport` | OneDrive Last Report | Date/Time | Technician: Read &nbsp;·&nbsp; Scripts: Read/Write | When the service last reported |
| `ODHelperConfig` | OneDrive Helper Settings | Multi-line text, scope **Organization and Device** | Technician: Read/Write &nbsp;·&nbsp; Scripts: Read | Settings JSON for that customer (see step 2) |

Dropdown options for `ODHealth`, exactly as written: `Healthy`, `Warning`, `Critical`, `Stale`, `NoUser`, `NotInstalled`. The service sends the first five; the uninstall script sets `NotInstalled`. The service looks the option's GUID up with `ninjarmm-cli options ODHealth` before setting it, so a missing option shows up in `service.log` as "dropdown ODHealth has no option ...".

`ODHelperConfig` must be created with **both** the Organization and the Device scope ticked. A device reads the closest value set for it, walking device → end user → location → organization, so a field that only exists at organization scope is invisible to `ninjarmm-cli get` on the device and the service sees it as empty. With both scopes, you set the value once on the organization and every device in it inherits it; a device-level value, if you ever set one, wins for that device.

What the values mean:

| Health | Meaning |
|---|---|
| `Healthy` | Every signed-in user has OneDrive running and every library folder on disk is syncing |
| `Warning` | OneDrive is not running for someone and is being restarted, or the restarts failed, or a user is ignoring an alert about a live problem |
| `Critical` | A folder has stopped syncing for someone, or a fix failed in the last 7 days |
| `Stale` | A signed-in user's agent hasn't reported for a while (it isn't running) |
| `NoUser` | Nobody is signed in. Nothing to check |

*Scripts: Read/Write* on the device fields is what lets `ninjarmm-cli` write them. Without it, `set` fails and the service logs the error to `C:\ProgramData\OneDriveHelper\service.log`.

> Check it on one PC: `OneDriveHelper.exe ninja pull --dry-run` from a NinjaOne remote shell prints the JSON the device sees. "Field ODHelperConfig is empty" with content set on the organization means the field is missing the Device scope or the Scripts: Read permission.

## 2. Put each customer's settings in `ODHelperConfig`

Open the organization, find the field, paste a settings file. It's the same JSON the settings tool exports, and it can be partial: anything you leave out keeps its default.

```json
{
  "CompanyName": "AcmeCorp",
  "SupportName": "the AcmeCorp Service Desk",
  "SupportEmail": "servicedesk@acmecorp.example",
  "SupportPhone": "+1 555 0100",
  "SupportURL": "https://help.acmecorp.example",
  "IgnoreOptions": "15m,1h,reboot"
}
```

The service checks the field at start-up and then once an hour (`NinjaPushMinutes`), and applies it when it changed. Invalid JSON is not applied; the reason shows up under the table in `ODErrors` and in `service.log`. To apply a change immediately, run `OneDriveHelper.exe ninja pull` on the device.

While `ODHelperConfig` has content, it is the source of truth: a change made locally in the settings tool is overwritten at the next check. Settings in `HKLM\SOFTWARE\Policies\OneDriveHelper` still win over everything, as before.

## 3. Deploy

Add `Install-OneDriveHelper.ps1` as a script (PowerShell, run as **System**) with these script variables:

| Variable | Type | Value |
|---|---|---|
| `version` | String | Release tag, e.g. `v1.0.0` |
| `hashX64` | String | SHA-256 of `OneDriveHelper-v1.0.0-x64.zip`, from `SHA256SUMS.txt` on the release |
| `hashArm64` | String | SHA-256 of the arm64 zip |
| `downloadBase` | String | Optional. Your own file share or web server holding the zips |
| `settingsJson` | String (multi-line) | Optional. Settings to apply at install for PCs where `ODHelperConfig` isn't used |
| `allowUnverified` | Checkbox | Off. Only tick it to install a build you have no hash for |

Run it once per device (a one-off automation, or an onboarding policy task). Running it again with a newer `version` upgrades in place and keeps the settings. The script prints `ninja check` at the end so you can see the CLI was found.

`Uninstall-OneDriveHelper.ps1` removes it and sets `ODHealth` to `NotInstalled` so no condition fires on the way out.

### ThreatLocker (or any application control)

The user's **Fix (admin)** button runs `OneDriveHelperAgent.exe remediate-approve` elevated, so the user can approve the fix at the UAC prompt themselves. Allow and elevate:

* `C:\Program Files\OneDriveHelper\OneDriveHelper.exe` (service, runs as SYSTEM)
* `C:\Program Files\OneDriveHelper\OneDriveHelperAgent.exe` (per user; needs an elevation policy for the approval step)
* `C:\Program Files\OneDriveHelper\OneDriveHelperConfig.exe` (settings tool, technicians only)

Match on the code-signing certificate if you sign the release (see the release workflow); otherwise by hash, which changes every release.

## 4. Conditions and tickets

**Administration → Policies → *your policy* → Conditions**. Two conditions, kept separate so they can route differently in the PSA:

| Condition | Trigger | Reset | Ticket |
|---|---|---|---|
| **OneDrive folders need attention** | Custom field `ODStaleFolders` **greater than** `0` | Automatically when no longer met | "Verify stale OneDrive folders with the user". Include `ODErrors` in the ticket body for the folder list, user and device |
| **OneDrive Helper critical** | Custom field `ODHealth` **equals** `Critical` | Automatically | Catches a failed fix. Also fires while stale folders exist, so you may prefer to route this one to a channel rather than a ticket |
| **OneDrive Helper not reporting** (optional) | Custom field `ODLastReport` older than 1 day, **and** `ODHealth` not `NotInstalled` | Automatically | A PC where the service died or the NinjaOne CLI stopped working |

With the Halo PSA integration, a condition that *resets* closes its ticket. Set the condition's "notify when reset" so the technician sees it.

What the user sees when a folder stops syncing: a popup with the folder names and **Fix (admin)**. If they click it and approve the UAC prompt, the fix saves every locally stored file to `C:\Users\<user>\ODHelper\Saved\<date>\<Library>`, removes the dead folder and asks OneDrive to sync the library again. `ODStaleFolders` returns to 0 within a minute and the ticket resolves. If they ignore it, the ticket tells you to reach out. If the folder turns out to be something they still need and OneDrive has genuinely dropped the library, the fix is still the right answer: the files are kept in the `Saved` folder and the library is re-added.

**When the user wants to keep the folder.** Sometimes the folder is a copy of an archived SharePoint site, or an old library the user keeps for reference, and the right answer is to leave it alone. The decision stays with a technician; the user can only ask:

1. Under every folder in the alert there is a small link, *I want to keep this folder - ask support to stop reporting it*. Clicking it shows a warning (files in the folder are not backed up, nobody will be warned again, but keeping it can be a valid business need) with OK / Cancel.
2. OK starts an e-mail to your support address (the same draft mechanism as **Contact support**) with the folder path, computer, user, a line for the user to say why they need it, and the exact NinjaOne script and command for the technician. The user adds the reason and presses Send. Event 4003 is logged. Nothing changes on the PC.
3. The technician runs **Ignore OneDrive folder** on the device from NinjaOne, filling in `folder` and `note`.

Add `Ignore-OneDriveFolder.ps1` once as a script (PowerShell, run as System) with these script variables, so it shows up on the device's **Run script** menu with a form:

| Variable | Type | Required | Value |
|---|---|---|---|
| `folder` | String | Yes | Full path from the e-mail, e.g. `C:\Users\jsmith\Contoso\Archive - Documents` |
| `note` | String | Recommended | Ticket number and who approved, e.g. `T12345 approved by J. Smith` |
| `remove` | Checkbox | No | Tick to take the folder off the list (report it again) |

The folder disappears from the alert, the status and `ODStaleFolders` within a minute, so the ticket resolves, and it can never be picked up by a fix. The ignore list is machine-wide under `HKLM\SOFTWARE\OneDriveHelper\IgnoredFolders`, which only administrators can change, and it is listed at the bottom of the `ODErrors` table so it is never forgotten. From a remote shell the equivalent is `OneDriveHelper.exe ignore add|remove|list`.

Event-log conditions still work if you want an instant alert as well: source `OneDriveHelper`, IDs 3006 (restarts failed), 4001 (folders stopped syncing), 5003 (fix failed). The custom-field conditions are the ones that resolve themselves.

## 5. Check it works

From a NinjaOne remote shell (which runs as SYSTEM) or from an elevated prompt on the device:

```
"C:\Program Files\OneDriveHelper\OneDriveHelper.exe" ninja check          # finds ninjarmm-cli, lists the fields and the ODHealth options
"C:\Program Files\OneDriveHelper\OneDriveHelper.exe" status               # the table the service built
"C:\Program Files\OneDriveHelper\OneDriveHelper.exe" ninja push --dry-run # the exact HTML it would send
"C:\Program Files\OneDriveHelper\OneDriveHelper.exe" ninja push           # send now (SYSTEM only)
"C:\Program Files\OneDriveHelper\OneDriveHelper.exe" ninja pull --dry-run # what ODHelperConfig holds
```

Then look at the device in NinjaOne: `ODErrors` shows the table, `ODHealth` and `ODStaleFolders` match it, `ODLastReport` is now.

To test the ticket: on a test PC, copy a synced library folder next to itself inside the organisation folder (e.g. copy `C:\Users\test\Corp\Projects - Documents` to `C:\Users\test\Corp\Projects - Documents - old`). Within a minute the popup appears, `ODStaleFolders` becomes 1 and the condition fires. Click **Fix (admin)**, approve, and watch it return to 0.

## If the service can't write the fields

`ninjarmm-cli.exe` must run as SYSTEM, which the service is. If `ninja push` from a SYSTEM shell reports an error from the CLI in your tenant, use the fallback: set `NinjaEnabled` to `0` in `ODHelperConfig` (so the service stops trying), and schedule `Report-OneDriveStatus.ps1` hourly as System. It reads the service's `status.json`, renders the same table through `OneDriveHelper.exe status --html`, and sets the same fields with `Ninja-Property-Set`. Conditions work the same way; the only difference is up to an hour of delay.

## Where things are on the PC

| What | Where |
|---|---|
| Machine report (JSON) | `C:\ProgramData\OneDriveHelper\status.json` |
| Headline values | `HKLM\SOFTWARE\OneDriveHelper\Status`: `Health`, `Summary`, `StaleFolderCount`, `StaleFolders` (multi-string `user|path`), `Users`, `UpdatedAt` |
| Service log (includes every NinjaOne push and pull) | `C:\ProgramData\OneDriveHelper\service.log` |
| Per-user agent log | `%LOCALAPPDATA%\OneDriveHelper\agent.log` |
| Per-user snapshot | `HKCU\Software\OneDriveHelper\State\Status` |
| Settings | `HKLM\SOFTWARE\OneDriveHelper` (`OneDriveHelper.exe config show` tells you where each value came from) |
