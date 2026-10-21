# TarkovPilot v2 (Go + Wails)

TarkovPilot+ extends the original Tarkov Pilot desktop app with an Item Scanner
window. The original settings window and tray icon watch EFT screenshots and
logs and send events to the Tarkov Market website via webhooks. The browser does
not connect to the app. Closing either application window closes both.

The original project documentation is available in the
[upstream README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md).

## Build

Build on Windows x64 with Go 1.25.x and Wails CLI v2.13.0. Install the CLI and
make sure `go` and `wails` are available on `PATH`:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
cd app
.\build.ps1        # complete build/bin/TarkovPilotPlus-<version>-windows-x64/ package
```

The main frontend is maintained in `frontend/dist`, and the Item Scanner frontend
is maintained in `plus/frontend/dist`. Both use static HTML/CSS/JavaScript; no
Node.js or npm build is required. Wails regenerates `frontend/wailsjs`, which is
ignored by Git. The frontends call `window.go` and `window.runtime` directly, so
generated bindings are not maintained as source files.

The build script runs `plus/ocr/prepare.ps1` to download pinned OCR dependencies
and verify their SHA256 hashes. It copies the prepared runtime, models and
license notices beside the EXE. OCR uses those local files and does not download
a runtime or models when the application starts. The native OCR libraries require
the Microsoft Visual C++ 2015–2022 x64 runtime.

To run the full test suite, including tests that use the local OCR engine, prepare
the runtime first. Run these commands from the `app` directory:

```powershell
.\plus\ocr\prepare.ps1
go test ./...
go vet ./...
```

OCR engine tests are skipped if the prepared runtime is missing. The preparation
script uses a local download cache under `.local/tools/ocr`; repository-local Go
or Wails tools are optional and are not required for the standard commands above.

## Release

The original application version remains in `version.go` (`2.1.1`), while the
independent Plus version is in `plus/version.go` (`1.4.1`). The build script can
increment only the Plus version; it leaves the original version unchanged. It
builds the EXE and local OCR components and writes both versions to
`versions.json`. The upstream repository, commit and version are recorded in
`plus/upstream.json`. Package separation and upstream maintenance are documented
in [plus/README.md](plus/README.md).

Every build creates `build/bin/TarkovPilotPlus-<version>-windows-x64/`, containing
`TarkovPilotPlus.exe`, `versions.json` and the complete `ocr` directory. The build
also produces `build/bin/TarkovPilotPlus-<version>-windows-x64.zip` with the same
distribution files and its `.sha256` file. The current package version is `1.4.1`.

The default compiler output filename is `TarkovPilotPlus.exe`. A custom
`-OutputFilename` changes the compiler output name, but the executable is moved
into the complete package as `TarkovPilotPlus.exe`. This canonical name is also
used in the ZIP. No extra standalone compiler EXE remains in `build/bin` after a
successful build.

TarkovPilot+ is updated manually from its own
[GitHub releases](https://github.com/ColorsASD/TarkovPilotPlus/releases). The Plus
update policy disables automatic upstream version checks and EXE replacement in
the combined application; upstream releases do not produce an update button or
notification. Keep the matching `ocr` directory beside the EXE when updating.

## Structure

- `main.go` — Wails bootstrap, single instance (mutex), systray (fyne.io/systray)
- `autostart.go` — Windows Run entry; autostart uses `--hidden` to launch directly into the tray
- `app.go` — application state, frontend bindings, heartbeat (60 seconds), connection state `connState` and watcher events; the update action opens the Plus release page
- `internal/config` — settings in `%APPDATA%\TarkovPilot\settings.json`
- `internal/detect` — game folder detection (BSG registry / Steam vdf / default) and screenshots
- `internal/watcher` — screenshots (fsnotify) and logs (1 sec polling, tail from the end)
- `internal/api` — webhooks `/api/be/pilot/script/webhook` (hookId)
- `internal/updater` — unchanged original updater code; the Plus update policy prevents it from replacing the combined EXE
- `frontend/dist` — English main window with a separate EN/RU website region selection; the title bar shows only `TarkovPilot+`, and both component versions appear separately in the content

## Item Scanner

- `plus/item_window.go` — separate Wails window, encrypted API key with explicit reveal, local catalog and scan state; one automatic scan toggle, independent manual scanning and search by name
- `plus/item_tracking.go` — character selection by unique ID, API stock totals and quest/hideout requirements; four typed stock actions share quantity checks with their buttons and shortcuts, while ambiguous same-ID variant API writes remain blocked
- `plus/item_quality.go` and `plus/typed_tracking_view.go` — persistent local Found in Raid/Regular classification, Uncategorized remainders, separate variant identities and fixed/Shared quality summaries alongside independent goal coverage and quality breakdowns
- `plus/item_quality_backup.go` — native JSON import/export for the selected character and game mode's classifications, with current-stock bounds and no API total changes or exported credentials
- `plus/item_hotkey.go`, `plus/item_quality_hotkey.go` and `plus/tracked_hotkey*.go` — default F1 Add Found in Raid, F2 Add Regular, F3 Remove Found in Raid and F4 Remove Regular shortcuts, each configurable with a supported key or Ctrl/Alt/Shift combination or Disabled; dedicated keyboard hook threads consume matching key events and send bound requests to the stock worker without waiting for network requests
- `plus/paired_windows.go` — paired startup and shutdown; each window runs in its own Wails process
- `plus/internal/market` — Tarkov Market API only, five-minute catalog cache, full Inspect title matching by edit distance and separate manual name search; private character and inventory data use a separate API rate limit and are not cached to disk
- `plus/internal/nextitem` — local quality-aware allocation over the existing goal-priority engine, current quest/hideout ratios and explicit completion flags; pure typed +1 checks determine Add eligibility without submitting an extra item
- `plus/internal/scanner` and `plus/ocr/name-scan.ps1` — Inspect window detection after a left click, followed by local English title OCR with the bundled Tesseract engine; OCR runs only for a detected Inspect window and can run again after another click
- `plus/ocr/prepare.ps1` — pinned OCR dependency downloads and SHA256 verification at build time
- `plus/frontend/dist/item.*` — compact English Item Scanner with a "How it works?" section, prices, four stock controls and current Next Item/Completed Item goal ratios without a header category; the standalone Items Tracker editor above Settings shows item images, name-copy buttons, a full-width Refresh inventory button and an Import/Export row

Automatic scanning checks for an Inspect window 200 milliseconds after a left
click; it does not continuously OCR the full screen. Manual scanning allows three
seconds to switch back to the game and reads an open Inspect window. The frontend
uses the `window.go.plus.ItemWindowApp` binding and receives `item-state` events.

Items Tracker uses the selected character's Market total stock and goal progress,
with a local FiR/Regular split. Existing and externally added pieces remain
Uncategorized until classified, and do not cover goals. Each Next Item quest or
hideout goal separately evaluates the classified inventory: a Regular requirement
uses Regular pieces first and eligible FiR pieces for its remainder, while a
FiR-only requirement accepts only FiR. A goal cannot reuse a physical piece
between its own fixed and Shared needs; different goals can each show that piece.
The Stock summary separately reserves FiR pieces for FiR-only needs, then uses
Regular pieces and eligible FiR surplus for Regular/Shared needs. Both views use
the local classification rather than treating the API's untyped group counter as
item quality. With one FiR and one Regular ComTac, Stock stays `2/17` with Regular
`1/2` and Shared (FiR) `1/15`, while Next Item shows Hot Delivery `2/2`
`(FiR: 1, NonFiR: 1)` and Scavenger `1/15` `(FiR: 1, NonFiR: 0)`. Adding another
Regular piece replaces the FiR contribution to Hot Delivery with Regular stock.
The existing global priority calculation still controls collection targets and
typed Add eligibility; the independent goal display does not change either.

Full tracker refresh reads the selected character's entire quest and hideout
state with inventory. Only completion flags and minimal item identities persist,
separately for each API-key, character and game-mode profile. An observed
unfinished-to-completed transition makes all related remaining stock Uncategorized,
including every Shared or Shared (FiR) candidate. API totals remain unchanged.
First observations establish a baseline; repeated completed states do not reset
new classifications. Uncategorized pieces contribute only to Stock, never to
category or goal coverage.

Confirmed quality and a before-send quality recovery journal persist locally.
The pending stock queue stays in memory and is not replayed after restart;
uncertain or acknowledged stock writes are checked through GET without another
PATCH. Temporary rate limits and requests canceled before sending retain their
queued intent. Last valid same-item/profile stock remains usable during a
background refresh, with normal quantity and quality limits; first loading,
mismatches, errors and unrelated busy actions remain gated.

The inventory editor's Import/Export buttons use native file dialogs and include
only the selected character and game mode's local classifications and row
identities. Imports reject another character or mode, skip unmatched identities
and conservatively bound quality by current API stock. Totals stay unchanged;
credentials, profile hashes and pending recovery records are not exported. Wait
for inventory loading and pending stock saves before transferring classifications.
The searchable list puts items with Uncategorized stock first, then fully
classified items, with each group alphabetized by displayed name.

Refresh inventory requests complete Market progress before reloading the editor.
The open list follows successful progress observations and completion resets,
coalesces pending-save bursts and keeps the search filter. Inventory editing waits
during that refresh without pausing eligible stock buttons or shortcuts.

Completed Item is a display state based on explicit source completion and empty
stock, rather than merely a filled ratio; it does not change stored stock or
shortcut eligibility. Distinct same-ID variants retain independent local quality
and goal ratios while their ambiguous API stock writes remain disabled.
Goal headings use Quest/Quests and Hideout Module/Hideout Modules according to
the number of displayed entries. Allocation, completion, refresh and native
clipboard behavior are documented in [plus/README.md](plus/README.md#market-progress).

Keep the `ocr` directory beside the built application. The API key is protected
with Windows DPAPI, and the Item Scanner stores its key, settings and local item
quality under
`%APPDATA%\TarkovPilotPlus`. Original Pilot settings remain under
`%APPDATA%\TarkovPilot`.
