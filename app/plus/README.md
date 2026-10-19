# TarkovPilot+ extension

The additional features live in the `plus` Go package. This package does not
import the original application's `main` package and embeds its own frontend and
OCR helper files in the EXE. The original project's documentation is available
in the [upstream README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md).

## Structure

- `item_window.go` — Item Scanner Wails window, encrypted API key, settings, English title OCR and manual search
- `item_tracking.go` — character selection, Items Tracker and inventory updates
- `local_progress.go` — coherent Market quest/hideout progress loading and the existing Items Tracker view
- `item_hotkey.go` — Add availability, immutable item/character targets and a worker shared with the Add button's stock operation
- `tracked_hotkey.go` and `tracked_hotkey_windows.go` — configurable Regular/FiR shortcuts, global Windows key consumption, physical press/release tracking and dedicated keyboard hook lifecycle
- `inspect_clicks_windows.go` — automatic scan trigger after a click
- `paired_windows.go` — companion window startup, display and shutdown; a callback requests shutdown of the main application
- `internal/market` — catalog, prices, characters, requirements and the documented inventory update operation
- `internal/localprogress` — pure Market requirement allocation and alternative-group calculations
- `internal/scanner` and `ocr` — local Tesseract engine, Inspect detection and OCR
- `frontend/dist` — Item Scanner frontend only; its Wails binding is `window.go.plus.ItemWindowApp`
- `update.go` — Plus release page and the update policy that prevents upstream EXE replacement
- `version.go` — independent Plus version and the shared `TarkovPilot+` window title
- `upstream.json` — original repository, commit and version used as the base

The frontend calls the exported Plus window methods through `window.go` and
receives updates through the `item-state` runtime event. Its HTML, CSS and
JavaScript are maintained directly in `frontend/dist`. The main frontend remains
in `app/frontend/dist`. Generated `app/frontend/wailsjs` bindings are ignored;
neither frontend imports them.

Paths beginning with `app/` in this document are relative to the repository root.
Build and test commands are described in [app/README.md](../README.md).

## Versions

The original TarkovPilot version remains in `app/version.go` (`2.1.1`). The Plus
version is maintained independently in `app/plus/version.go` (`1.2.0`). A Plus
build can increment only its own version; releasing the extension does not change
the original version. The Pilot heartbeat sends the original version to the
website. The UI labels both versions separately, while the release package's
`versions.json` also records the upstream commit. Wails and Windows EXE version
metadata use the Plus product name and version.

Every build creates `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` and the
matching ZIP archive. Both contain `TarkovPilotPlus.exe`, `versions.json` and the
complete `ocr` directory. The executable name stays `TarkovPilotPlus.exe` even
when the build is given a custom compiler output filename. The current package
version is `1.2.0`.

The base is pinned in `app/plus/upstream.json` to
`ggdiam/TarkovPilot` commit `d04567384dd38dd32ad4b6a4283e2f21ee25c423`, version
`2.1.1`. Update this manifest and the original version together when adopting a
new upstream release.

## Upstream updates

The exact original source can be retrieved from the Git commit in `upstream.json`.
The original API, configuration, detection and log watcher packages, as well as
the original updater, are unchanged from that pinned base and remain separate
from the Plus package. The Plus update policy disables automatic upstream update
checks and prevents replacing the combined application with the original EXE.
Users update manually from the
[Plus releases](https://github.com/ColorsASD/TarkovPilotPlus/releases).

The combined application still needs integration changes: `app/main.go` starts
and displays the companion window; `app/app.go` handles its shutdown, the two
versions, the shared log date format and the update policy. The main frontend has
English labels, shared closing and separate version displays. Wails build
settings use the Plus name and version. These integration points differ from
upstream; the complete main application is not an unchanged upstream copy.

When adopting a new upstream release:

1. Save the working tree changes in a separate commit first.
2. Confirm that the `upstream` remote points to `https://github.com/ggdiam/TarkovPilot`, run `git fetch upstream`, and merge the selected upstream commit on a separate development branch. Keep `app/plus` as the extension's own source.
3. Preserve the companion window lifecycle, the `plusVersion` state field, the English UI and the Plus update policy at the integration points.
4. Update `app/version.go` to the new original version and update `app/plus/upstream.json` with the matching upstream commit and version. Change the Plus version independently.
5. From `app`, run `plus/ocr/prepare.ps1` before `go test ./...` and `go vet ./...` so the OCR engine tests run. Check frontend rendering, Inspect recognition and paired window closing, then use `build.ps1` to create the combined package. Verify both versions in `versions.json` and the release archive's SHA256 file.

The OCR preparation script downloads pinned dependencies and verifies their
hashes at build time; the application uses the bundled engine and models at
runtime. Keep the matching `ocr` directory and its license notices with the EXE.

Every merge needs review. Separating the extension reduces conflicts but does
not replace checking the upstream changes and the integration points.

## Add shortcut

The Item Scanner installs its own Windows keyboard hook on a dedicated message
thread. The defaults are F1 for Found in Raid and F2 for Regular. Settings can
configure each shortcut independently or disable it. Shortcuts support Ctrl,
Alt and Shift with F1-F12, A-Z, 0-9, Space, Insert, Delete, Home, End, PageUp and
PageDown. Unmodified supported keys can also be selected. Modifier order is
canonicalized to Ctrl+Alt+Shift; the two actions cannot share the same enabled chord.
Closing either app window stops the companion process and its hook after the
pending-update warning, when one is required. No keyboard
hook is installed in the game process, and game memory is not accessed.

The configured shortcut's down, repeat and up events are consumed for
normal Windows keyboard input while it is enabled, even when the
tracker is unavailable. Other keys and modifier combinations continue normally.
Holding the shortcut does not repeat Add. Injected input does not trigger stock
changes, and injected releases cannot reset a physical key hold.

The keydown callback captures an immutable eligible item/character target with
atomic operations and a nonblocking send. It does not run OCR, make API requests,
call Wails or take application locks. The worker rejects stale targets and uses the same short in-memory reservation
as the Add button. The displayed stock increases immediately. A separate Market
worker checks fresh stock and its revision, sends the update and verifies the
result. Pending reservations count toward Add eligibility without blocking
further eligible pieces while the network request runs.
The Add button remains usable when the shortcut is disabled or
its hook cannot start.

Windows hook consumption does not guarantee priority over every input path.
Applications using direct input, protected system input or another input handler
may still observe the key. Automated tests cover key-event suppression decisions,
target validation and API updates with controlled fixtures; actual physical
keyboard behavior in the game requires a live trial.

## Market progress

Items Tracker uses only the selected character's Tarkov Market requirements,
completion and inventory. The tracked-items, quests and hideout reads must have
matching character, game mode and revision before the combined snapshot is used.
The snapshot remains in memory. Inspect changes can reuse this confirmed graph;
Refresh progress reloads the selected character's current Market state.

Add accepts only an eligible active Market requirement. The button and shortcut
share one short reservation: the displayed quantity increases immediately and
the pending piece participates in allocation, preventing overcollection. The
Market worker saves these additions in the background using fresh stock and
revision checks. Successful updates settle the reservation; definite rejections remove the
rejected reservation and expose the error. Network outages retain pending pieces
in memory. Uncertain writes are verified through GET before any retry, including
when Refresh progress resumes the pending work. Pending work is
held only in memory, without operation journals or a disk queue. No operation
is replayed after restart.

While the Market worker is saving, the scanner status and main Events log heading
append the pending item count in parentheses, for example `(3 items waiting for API save)`. Ordinary
Add actions remain available while prior saves are pending if another eligible
requirement still needs a piece. Closing either window with unsaved work shows
a native warning; confirming exit discards updates that have not reached Market.

The Items Tracker card and its Found in Raid, Regular, Any (FiR) and Any rows
remain visible when stock covers their requirements, for example 10/10. Add
buttons and shortcuts stop when no eligible requirement needs another item.
Completing or canceling the associated goal removes its active requirement.
Once all Any groups are covered and no other collection requirement remains,
Stock displays the item's available quantity as a complete ratio, such as 2/2
or 0/0, while Any can remain 8/8. This display convention changes neither Market
stock nor Add eligibility.

Mandatory requirements reserve suitable stock before shared Any groups;
a deterministic maximum-flow allocation distributes the remaining pieces among
overlapping groups. Each piece is allocated once. Any groups are summarized in
one Any (FiR) row and one Any row without changing their individual goal
identities, eligible items or allocations. Covered groups stay visible until
their goals are completed. Quality eligibility follows the Market requirements;
no local item-quality annotations are saved.

Any row tooltips preserve each group's quest, category and progress from the
[Tarkov Market API](https://tarkov-market.com/dev/api) `groups` and `questGroup` sources.

The encrypted API key, scanner settings and item-price catalog cache are the
remaining local files. Inventory, quest/hideout progress and pending stock are
not persisted locally. Previous local stock files and operation journals are
not loaded or uploaded. Separate FiR inventory and multiple variants sharing
one item ID keep unsupported Market updates disabled; the app does not invent
undocumented update fields.
