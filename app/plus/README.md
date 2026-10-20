# TarkovPilot+ extension

The additional features live in the `plus` Go package. This package does not
import the original application's `main` package and embeds its own frontend and
OCR helper files in the EXE. The original project's documentation is available
in the [upstream README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md).

## Structure

- `item_window.go` — Item Scanner Wails window, encrypted API key, settings, English title OCR and manual search
- `item_tracking.go` — character selection, Items Tracker and inventory updates
- `local_progress.go` — coherent Market quest/hideout progress loading and the existing Items Tracker view
- `local_tracking_view.go`, `variant_tracking_view.go` and `shared_tracking_items.go` — display projections for fixed requirements, separately identified variants and Shared group details
- `item_hotkey.go` — Add availability, immutable item/character targets and a worker shared with the Add button's stock operation
- `tracked_hotkey.go` and `tracked_hotkey_windows.go` — configurable Add item shortcut, global Windows key consumption, physical press/release tracking and dedicated keyboard hook lifecycle
- `inspect_clicks_windows.go` — automatic scan trigger after a click
- `paired_windows.go` — companion window startup, display and shutdown; a callback requests shutdown of the main application
- `internal/market` — catalog, prices, characters, requirements and the documented inventory update operation
- `internal/localprogress` — pure Market requirement allocation and alternative-group calculations
- `internal/nextitem` — pure virtual +1 allocation estimates, current goal ratios and source-based completion flags
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
version is maintained independently in `app/plus/version.go` (`1.3.0`). A Plus
build can increment only its own version; releasing the extension does not change
the original version. The Pilot heartbeat sends the original version to the
website. The UI labels both versions separately, while the release package's
`versions.json` also records the upstream commit. Wails and Windows EXE version
metadata use the Plus product name and version.

Every build creates `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` and the
matching ZIP archive. Both contain `TarkovPilotPlus.exe`, `versions.json` and the
complete `ocr` directory. The executable name stays `TarkovPilotPlus.exe` even
when the build is given a custom compiler output filename. The current package
version is `1.3.0`.

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

## Stock shortcuts

The Item Scanner installs Windows keyboard hooks on dedicated message
threads. Add item defaults to F1 and Remove item to F2. Settings can change or disable
each shortcut separately; the two actions require different shortcuts. They support Ctrl, Alt and Shift with F1-F12, A-Z, 0-9,
Space, Insert, Delete, Home, End, PageUp and
PageDown. Unmodified supported keys can also be selected. Modifier order is
canonicalized to Ctrl+Alt+Shift.
Closing either app window stops the companion process and its hook after the
pending-update warning, when one is required. No keyboard
hook is installed in the game process, and game memory is not accessed.

The configured shortcut's down, repeat and up events are consumed for
normal Windows keyboard input while it is enabled, even when the
tracker is unavailable. Other keys and modifier combinations continue normally.
Holding a shortcut does not repeat its action. Injected input does not trigger stock
changes, and injected releases cannot reset a physical key hold.

The keydown callback captures an immutable eligible item/character target with
atomic operations and a nonblocking send. It does not run OCR, make API requests,
call Wails or take application locks. The worker rejects stale targets and uses the same short in-memory reservation
as the corresponding button. The displayed stock changes immediately. A separate Market
worker checks fresh stock and its revision, sends the update and verifies the
result. Pending additions and removals count toward stock and Add eligibility without blocking
further eligible changes while the network request runs.
Disabling a shortcut or failing to start its hook does not disable the
corresponding eligible button. The confirmed Completed Item display hides both
stock buttons; their visibility is separate from the stock worker and shortcut
eligibility.

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

Refreshing an already loaded item keeps its panel and counters visible while
the matching snapshot loads in the background. Stock actions pause during the
refresh. A failed refresh retains the last known values with an error; switching
items or profiles cannot reuse another target's displayed inventory.

Add accepts only an eligible active Market requirement. Remove accepts a unique,
supported shared-stock item with stock above zero, including items without active
requirements. Each button and shortcut shares one short reservation: the displayed
quantity changes immediately and pending changes participate in allocation,
preventing overcollection and removal below zero. The
Market worker saves these changes in order in the background using fresh stock and
revision checks. Successful updates settle the reservation; definite rejections remove the
rejected reservation and expose the error. Network outages retain pending pieces
in memory. Uncertain writes are verified through GET before any retry, including
when Refresh progress resumes the pending work. Pending work is
held only in memory, without operation journals or a disk queue. No operation
is replayed after restart.

A successful write acknowledgement proves the app's change independently of
later stock changes from another client. A follow-up read with a later revision
can settle that change even when the current stock differs from the submitted
target; uncertain writes still require read-back verification without another
PATCH. Shortcut targets retain their identity across unrelated state events,
but item, character, settings and eligibility changes invalidate them. The
worker preserves FIFO order separately for each API-key, character and game-mode
profile. A blocked profile remains pending without preventing other profiles
from saving, and each blocked head is attempted at most once per worker run.

While the Market worker is saving, the scanner status and main Events log heading
append the pending item count in parentheses, for example `(3 items waiting for API save)`. Ordinary
Add actions remain available while prior saves are pending if another eligible
requirement still needs a piece. Closing either window with unsaved work shows
a native warning; confirming exit discards updates that have not reached Market.

For active or mixed goals, the Items Tracker card and its Found in Raid, Regular,
Shared (FiR) and Shared rows remain visible when stock covers their requirements,
for example 10/10. The Add item button and shortcut stop when no eligible
requirement needs another item.
Completing or canceling the associated goal removes its active requirement.
Once the collection target is covered and no other requirement remains,
Stock displays the item's available quantity as a complete ratio, such as 2/2
or 0/0, while Shared can remain 8/8. This display convention changes neither Market
stock nor Add eligibility.

The compact Next Item section shows the backend's estimated category on the right,
followed by every requirement for the current item, including completed goals,
under separate quest and hideout headings, in that order. One quest uses Quest;
multiple quests use Quests. One hideout entry uses Hideout Module; multiple entries
use Hideout Modules. Each name is on the left and its current
available/required ratio is in parentheses at the right edge. These are current
item-specific ratios, not virtual +1 values or the whole goal's item total. Shared
ratios can include other eligible items. Completed goals retain their complete
`x/x` ratios here instead of an aggregate completed-items row under Stock. An
unknown allocation displays `—/required`.
A gray divider separates this section from the summary rows. Requirement rows and
the Next Item category have no descriptive hover help; Add/Remove and copy-button
tooltips and help elsewhere remain.
If the calculation does not identify one of the four stock categories, the header
shows `—` while retaining the current requirement details.
When every target has a known complete ratio, no raw requirement gap remains and
Add is no longer eligible, the category is blank while the headings and goal list
remain. A refresh retains the previous category until that final state is confirmed.
When every target belongs only to completed sources, Add is ineligible and the raw
requirement is zero, the confirmed display becomes Completed Item. The Stock
summary, divider, Add/Remove buttons and Regular, Found in Raid and Shared summary rows are hidden, and the
quest/hideout list retains its complete `x/x` ratios. This changes only the display;
raw stock and Remove eligibility remain unchanged. Filled active or mixed goals
still use Next Item with their current Stock. A refresh retains the complete last
display state until the new data is confirmed.
The backend's `Target.Completed` flag is true only when the displayed target's
requirements come from completed sources, a completed quest, a completed or
prebuilt module, or an explicitly completed Shared group. Completed fixed sources
use their original `Amount/Amount`; Shared sources use the explicit group amount
once per group. Repeated fixed API sources retain their reported total. A mixed
goal stays active even at `10/10`, and a positive source amount with zero remaining
quantity does not by itself prove completion. Optional and canceled sources do
not contribute to these details. These flags affect rendering only; they do not
complete quests, consume inventory or clear Market stock.
Completed goal names have a small brown checkmark without hover help.
Small brown copy buttons follow the item, quest and hideout names; completed goal
rows place the copy button before the checkmark. Each button copies only its
displayed name through the native clipboard, including during progress refreshes.
The desktop frontend calls `window.runtime.ClipboardSetText(name)` directly and
awaits its `Promise<boolean>`; a failed call shows feedback. It uses no extra Go
binding and never reads the clipboard. Browser-only previews can use
`navigator.clipboard.writeText`; the desktop path uses the Wails runtime.
The latest result stays visible during refreshes and stock updates for the same
item, item ID, character and game mode. Switching that context, an error, separate
FiR inventory or an ambiguous variant clears it. A finished calculation with no
target details also clears it. Completed targets remain visible while Add is
unavailable.
New quest, hideout and inventory responses automatically update the section. The
category comes from a local virtual +1 calculation using API availability; missing
Market horizon and priority metadata mean it remains an estimate. The calculation
never submits its virtual +1 and changes neither stock, action eligibility nor the
Add and Remove shortcuts. Requirement categories do not identify the item's
in-game Found in Raid status.

`internal/nextitem.Calculate` uses the complete confirmed inventory and its
current quest and hideout sources, including canonical quest IDs, their aliases
and module upgrade keys. Newly returned goals need no item-specific rules. The
engine compares allocations before and after a virtual one-item addition and
filters candidate priority models against the current explicit API Shared
counters. The first matching model supplies the estimate. Alternatives test
near/future assignments and stable order within the component connected by the
selected item and positive stock; up to six unknown goals receive all binary
assignments in both tested orders, while larger components use bounded probes.
The API's complete horizon, stage and tie order remain unavailable, so a matching
baseline is not a guarantee of Market's next allocation.

The forecast DTO carries `status`, `category`, `possibleCategories`, `goalName`,
net goal/category `changes`, explanatory `reason` and the current `targets`.
Target ratios include only needs accepting the selected physical item, aggregated
once per goal; other item needs still influence the allocation engine. Fixed
current ratios come from the selected model's before allocation; Shared ratios
use the confirmed API group counters. If no model reproduces those counters,
fixed target allocations are marked unknown while explicit Shared and completed
ratios remain available. Existing stock can move between needs after +1, so the
backend can report `Reallocated`; an unused addition is `Stock only`. The compact
frontend renders only Regular, Found in Raid, Shared and Shared (FiR) as category
labels, using `—` for other unresolved outcomes while retaining available target
details. Missing or inconsistent metadata, pending stock changes, separate FiR
inventory and duplicate target item IDs prevent a new supported estimate.

The concise How it works? section describes the visible controls. The allocation
models, DTO fields and refresh-cache rules above are implementation details; they
are not additional in-game item statuses or stock operations.

Shared collection targets use the explicit API group progress and other eligible
items. Other items' confirmed contribution is capped by reported group availability;
their pending additions and removals also affect the target. The selected item's
own stock is not subtracted from its target a second time. A deterministic
maximum-flow calculation prevents another item's stock from being credited twice
across overlapping groups. Legacy inferred groups retain their local allocation
fallback. Groups are summarized in one Shared (FiR) row and one Shared row while
preserving their individual identities and eligible items. Covered groups stay
visible until their goals are completed. Shared rows report confirmed API group
progress. For fixed requirement rows, confirmed Shared group progress reserves
stock first. The remaining confirmed stock goes to Found in Raid and then Regular;
each piece is counted only once across these rows and Shared groups. Where the API
data cannot establish an unambiguous allocation, the fixed row shows `—/required`.
This display rule does not claim
to reproduce Market's unspecified priorities for complex overlapping goals. Stock
includes pending changes immediately; requirement rows update after confirmation.
The display allocation changes neither raw Market stock, collection limits nor Add
eligibility. Stock actions change only the shared Market stock; the app does not
assign or keep item-quality annotations.

Shared progress uses the [Tarkov Market API](https://tarkov-market.com/dev/api)
`groups` and `questGroup` sources, including separately identified variants.
A variant's confirmed stock reduces other candidates' collection
targets when its own source identifies the same group, even while its stock updates
remain disabled. The same stock is credited only once across overlapping groups.

The encrypted API key, scanner settings and item-price catalog cache are the
remaining local files. Inventory, quest/hideout progress and pending stock are
not persisted locally. Previous local stock files and operation journals are
not loaded or uploaded. Separate FiR inventory and multiple variants sharing
one item ID keep unsupported Market updates disabled. Each variant still displays
its own stock and Found in Raid, Regular, Shared (FiR) or Shared requirements,
including confirmed group progress when its own collection target is already covered.
Their ambiguous item IDs also prevent Next Item and Completed Item details from
being displayed; the variant stock and requirement rows remain the supported view.
The app does not invent undocumented update fields.
