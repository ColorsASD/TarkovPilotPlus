# TarkovPilot+ extension

The additional features live in the `plus` Go package. This package does not
import the original application's `main` package and embeds its own frontend and
OCR helper files in the EXE. The original project's documentation is available
in the [upstream README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md).

## Structure

- `item_window.go` — Item Scanner Wails window, encrypted API key, settings, English title OCR and manual search
- `item_tracking.go` — character selection, Items Tracker and inventory updates
- `item_quality.go` — persistent local Found in Raid/Regular classification, the inventory editor and stock-write quality recovery
- `typed_tracking_view.go` — fixed and Shared summaries from the same local typed allocation as the goal ratios
- `local_progress.go` — coherent Market quest/hideout progress loading and the existing Items Tracker view
- `local_tracking_view.go`, `variant_tracking_view.go` and `shared_tracking_items.go` — display projections for fixed requirements, separately identified variants and Shared group details
- `item_hotkey.go` and `item_quality_hotkey.go` — typed stock availability, immutable item/character targets and the worker shared with the four stock buttons
- `tracked_hotkey.go` and `tracked_hotkey_windows.go` — configurable stock shortcuts, global Windows key consumption, physical press/release tracking and dedicated keyboard hook lifecycle
- `inspect_clicks_windows.go` — automatic scan trigger after a click
- `paired_windows.go` — companion window startup, display and shutdown; a callback requests shutdown of the main application
- `internal/market` — catalog, prices, characters, requirements and the documented inventory update operation
- `internal/localprogress` — pure Market requirement allocation and alternative-group calculations
- `internal/nextitem` — local typed allocation, current goal ratios, typed +1 eligibility and source-based completion flags
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
threads. The four stock buttons use the following default shortcuts:

- F1 — Add Found in Raid item
- F2 — Add Regular item
- F3 — Remove Found in Raid item
- F4 — Remove Regular item

The buttons form a two-column grid, with additions above removals. An unavailable
action is gray and disabled: adding needs an eligible unfilled requirement, and
removing needs stock of that exact quality. Settings can change or disable
each shortcut separately; all enabled actions require different shortcuts. They support Ctrl, Alt and Shift with F1-F12, A-Z, 0-9,
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
corresponding eligible button. The confirmed Completed Item display hides all four
stock buttons when no stock remains; their visibility is separate from the stock worker and shortcut
eligibility.

Windows hook consumption does not guarantee priority over every input path.
Applications using direct input, protected system input or another input handler
may still observe the key. Automated tests cover key-event suppression decisions,
target validation and API updates with controlled fixtures; actual physical
keyboard behavior in the game requires a live trial.

## Market progress

Items Tracker uses the selected character's Tarkov Market requirements,
completion and total inventory, together with locally recorded item quality.
The tracked-items, quests and hideout reads must have
matching character, game mode and revision before the combined snapshot is used.
The snapshot remains in memory. Inspect changes can reuse this confirmed graph;
Refresh progress reloads the selected character's current Market state.

Refreshing an already loaded item keeps its panel and counters visible while
the matching snapshot loads in the background. Stock actions pause during the
refresh. A failed refresh retains the last known values with an error; switching
items or profiles cannot reuse another target's displayed inventory.

Tarkov Market remains authoritative for each item's total stock. The local save
distinguishes Found in Raid, Regular and Uncategorized quantities within that
total, separately for each API-key, character and game-mode profile. Existing
stock and external increases start as Uncategorized; those pieces do not cover
any goal until the user classifies them in the separate Items Tracker panel.

The searchable Items Tracker inventory editor is a separate collapsible panel
above Settings. It lists items with positive stock, with a thumbnail and a small
brown name-copy button after each displayed item name. Failed or unavailable
images use the existing placeholder. The API total
is read-only. Found in Raid and Regular accept nonnegative whole numbers whose
sum cannot exceed that total; the remainder is Uncategorized. Save classification
locally changes only the local quality split and does not make a Market stock
request. Profile and expected-total checks reject stale edits, and pending saves
make that item's editor row read-only. Refresh inventory reloads the available
rows without changing Market stock.

When a fresh Market response reports less stock after an external change, the
app preserves only the minimum quantity of each quality that is still certain.
If total stock falls by `decrease`, confirmed FiR becomes
`max(0, oldFiR - decrease)` and confirmed Regular becomes
`max(0, oldRegular - decrease)`; the rest of the new total is Uncategorized. For
example, one FiR and one Regular piece becoming one total piece leaves zero
confirmed FiR, zero confirmed Regular and one Uncategorized piece, because the
API does not identify which piece disappeared. The inventory editor can classify
the remaining stock. The app's own four stock actions preserve the selected
quality exactly. Confirmed classifications survive restart. The local `item-quality.json` file contains
quantities, hashed profile identifiers and quality recovery records, without the
API key. It is runtime data and is not part of the release package.

Each Add action accepts only an eligible active Market requirement that an
additional item of its quality can cover. Each Remove action accepts a unique,
supported item with stock of that quality above zero, including items without
active requirements. Each button and shortcut shares one short reservation: the displayed
quantity changes immediately and pending changes participate in allocation,
preventing overcollection and removal below zero. The
Market worker saves these changes in order in the background using fresh stock and
revision checks. Successful updates settle the reservation; definite rejections remove the
rejected reservation and expose the error. Network outages retain pending pieces
in memory. Uncertain writes are verified through GET before any retry, including
when Refresh progress resumes the pending work. Queued stock deltas remain in
memory and are not replayed after restart. Before sending a typed stock write,
the app persists its quality recovery record. On restart, a fresh GET establishes
the current total without sending another PATCH. A changed total or revision
cannot prove that an interrupted write caused the change, so an unresolved
quality record leaves the affected stock Uncategorized for the user to classify.
Confirmed classifications remain saved. This journal protects quality bookkeeping and is not a
disk queue for replaying stock updates.

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
append the pending item count in parentheses, for example `(3 items waiting for API save)`.
Typed Add actions remain available while prior saves are pending if another eligible
requirement still needs a piece of that quality. Closing either window with unsaved work shows
a native warning; confirming exit discards updates that have not reached Market.

For active or mixed goals, the Items Tracker card and its Found in Raid, Regular,
Shared (FiR) and Shared rows remain visible when stock covers their requirements,
for example 10/10. Each Add button and shortcut stops when no eligible
requirement can use another item of its quality.
Completing or canceling the associated goal removes its active requirement.
Once the collection target is covered and no other requirement remains,
Stock displays the item's available quantity as a complete ratio, such as 2/2
or 0/0, while Shared can remain 8/8. This display convention changes neither Market
stock nor Add eligibility.

The compact Next Item section lists every requirement for the current item,
including completed goals, under separate quest and hideout headings, in that
order. Its header has no category on the right. One quest uses Quest; multiple
quests use Quests. One hideout entry uses Hideout Module; multiple entries use
Hideout Modules. Each name is on the left and its current available/required
ratio is in parentheses at the right edge. These are current item-specific
ratios, not virtual +1 values or the whole goal's item total. Shared ratios can
include other eligible items. Completed goals retain their complete `x/x` ratios
instead of an aggregate completed-items row under Stock. An unknown allocation
displays `—/required`.

A gray divider separates this section from the summary rows. When every target
belongs only to completed sources, both Add actions are ineligible, the raw
requirement is zero and no stock remains, the confirmed display becomes
Completed Item. Stock, the divider, the four stock buttons and the requirement
summary rows are hidden, while the quest/hideout list retains its complete
`x/x` ratios. Filled active or mixed goals still use Next Item with their current
Stock. Remaining classified stock can still be removed when no active goal
requires it. A refresh retains the complete last display state until the new
data is confirmed.

The backend's `Target.Completed` flag is true only when the displayed target's
requirements come from completed sources, a completed quest, a completed or
prebuilt module, or an explicitly completed Shared group. Completed fixed sources
use their original `Amount/Amount`; Shared sources use the explicit group amount
once per group. Repeated fixed API sources retain their reported total. A mixed
goal stays active even at `10/10`, and a positive source amount with zero remaining
quantity does not by itself prove completion. Optional and canceled sources do
not contribute to these details. These flags affect rendering only; they do not
complete quests, consume inventory or clear Market stock.

Completed goal names have a small brown checkmark without hover help. Small brown
copy buttons follow the item, quest and hideout names; completed goal rows place
the copy button before the checkmark. Each button copies only its displayed name
through the native clipboard, including during progress refreshes. The desktop
frontend calls `window.runtime.ClipboardSetText(name)` directly and awaits its
`Promise<boolean>`; a failed call shows feedback. It uses no extra Go binding and
never reads the clipboard. Browser-only previews can use
`navigator.clipboard.writeText`; the desktop path uses the Wails runtime.

The latest result stays visible during refreshes and stock updates for the same
item, item ID, character and game mode. Switching that context, an error, separate
API FiR inventory or an indistinguishable variant clears it. Distinct variants
use independent row calculations and do not share cached goal details. A finished calculation with
no target details also clears it. Completed targets remain visible while adding
is unavailable. New quest, hideout and inventory responses automatically update
the section.

`internal/nextitem.CalculateTyped` uses the complete confirmed requirement graph
and the locally classified stock, including canonical quest IDs, their aliases
and module upgrade keys. Newly returned goals need no item-specific rules.
Found in Raid requirements can use only FiR pieces. FiR-only needs reserve those
pieces before Regular requirements; Regular needs use Regular pieces first and
can also use surplus FiR pieces. Uncategorized pieces cannot satisfy either
quality. Fixed requirements, Shared requirements and the goal list use this same
allocation, so one physical piece is credited only once across overlapping needs.

For example, with one Regular vodka, Booze shows `0/10` and A Bitter Victory
shows `1/4`; the summary shows Found in Raid `0/10` and Regular `1/4`. Classifying
the same single piece as FiR gives Booze `1/10` and Regular `0/4` without changing
the API total. Leaving it Uncategorized gives both goals zero allocated pieces.
The API still supplies required quantities and completion; the app supplies
quality-aware allocation from the local split.

Shared progress uses the [Tarkov Market API](https://tarkov-market.com/dev/api)
`groups` and `questGroup` sources to identify requirements and eligible items.
The displayed Shared and Shared (FiR) numerators come from locally classified
inventory across those eligible items, rather than the API group's untyped stock
counter. Other eligible items and their pending typed changes participate in the
same allocation. Groups are summarized in one Shared (FiR) row and one Shared row
while preserving their individual identities and eligible items. Covered groups
stay visible until their goals are completed. Missing or inconsistent allocation
metadata displays an unknown numerator instead of reusing an API counter as
local quality progress.

The pure typed +1 calculation checks whether one more FiR or Regular piece would
cover an unmet eligible requirement. That result controls the matching Add
button and shortcut; it never submits its virtual piece. Remove availability
uses the selected quality's current stock. Pending changes immediately update
the total, local quality projection, requirement rows and goal ratios, while the
Market worker confirms the total in the background. Display allocation does not
complete goals, consume additional inventory or change stock by itself.

The concise How it works? section describes the visible controls and local
classification. The allocation engine and DTO fields are implementation details,
not additional in-game item statuses or stock operations. The former estimated
category is no longer displayed next to Next Item or Completed Item.

The encrypted API key, scanner settings, item-price catalog cache and local item
quality are stored separately. Quest/hideout snapshots remain in memory; total
stock is refreshed from Market. Quality records are never uploaded as inventory
fields. Confirmed quality and the small write-recovery journal survive restart,
but unsent stock deltas do not. Older unrelated local stock formats are not
loaded or uploaded.

Multiple variants sharing one item ID keep unsupported API stock updates disabled,
while their local quality is editable independently. Stable row keys use the
item ID, variant name and source identifiers; source order, quantities and goal
progress do not identify a variant. Each distinct row retains its own API total,
local classification and typed quest/Shared ratios after reordering or restart.
Rows with indistinguishable identities remain read-only, as does the API's
separate FiR inventory. The app does not invent undocumented update fields.
