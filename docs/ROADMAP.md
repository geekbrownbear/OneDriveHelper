# Roadmap

Things considered and not built yet, with the thinking so far, so nobody has to rediscover it.

## Hung OneDrive detection

Today the tool knows two states: OneDrive.exe is running, or it isn't. A third one exists: OneDrive.exe is running but hung, or its Files On-Demand provider is disconnected, so opening an online-only file fails with "the cloud file provider is not running". It happens while OneDrive ingests the metadata of a newly added large library, and a restart resumes the ingest.

[Jos Lieben's scripts](https://github.com/jflieben/assortedFunctionsV2/tree/main/O4B) show how to detect it, per account:

1. **Explorer's own status** through the Windows 11 storage-provider status API: enumerate the user's `OneDrive!<SID>!…` sync roots from `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager`, activate each root's `StorageProviderStatusUISourceFactory` CLSID, call `GetStatusUISource(id).GetStatusUI()` and read `ProviderState` plus the sync status label ("Up to date", "Syncing", "Paused", "Error"). The call is relayed to OneDrive.exe, so no answer within 10 seconds while OneDrive.exe is running means hung.
2. **Cloud Files provider status**: `cldapi.dll!CfGetSyncRootInfoByPath(path, CF_SYNC_ROOT_INFO_PROVIDER)` on the user folder and each mount point. 0 / 0xC0000001 / 0xC0000002 = disconnected / terminated / error.
3. **A download probe**: read one byte of the smallest online-only placeholder (attribute `FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS` set, not pinned, under 5 MB) with a 65-second deadline, then set `FILE_ATTRIBUTE_UNPINNED` so OneDrive frees it again. Unhealthy results: time-out, or HRESULT 0x8007016A, 0x80070194, 0x800701AA, 0x800701DB.

Exclusions the scripts apply and this should too: accounts OneDrive hasn't signed in since it started, personal accounts blocked by policy, sync roots still connecting less than 10 minutes after OneDrive started, and Paused / Offline / Error states (report, don't restart).

Plan for this project: implement the three checks in the agent (`internal/onedrive`, check 1 needs hand-written COM/WinRT over `x/sys/windows`, and is the risky part), run the probe at most every 15 minutes, treat a sustained hang or disconnect like "not running" so the existing rate-limited automatic restart handles it (with a graceful `/shutdown` before killing), add the Explorer label and provider status to the status snapshot so the NinjaOne table shows "Up to date" / "Syncing" / "Paused" / "Error", and add settings `HangCheckEnabled`, `HangGraceSeconds`, `HydrationProbeEnabled`, `HydrationProbeMinutes`. New event IDs 3007 (hung, restarting), 3008 (healthy after restart), 3009 (still unhealthy).

## Technician-run folder removal from NinjaOne

For users who keep ignoring the popup. A `remediate remove --user … --folder … --by "<tech/ticket>" [--resync]` command run as SYSTEM would write a signed request into the admin-only `%ProgramData%\OneDriveHelper` folder; the user's agent would pick it up within a minute and run the existing backup-and-remove in the user's session (the file work must stay there, see AGENTS.md), with no popup. A NinjaOne script would wrap it and refresh the fields afterwards. Not built because the user-driven UAC fix plus the ticket covers the normal case.
