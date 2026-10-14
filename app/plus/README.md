# TarkovPilot+ extension

The additional features live in the `plus` Go package. This package does not
import the original application's `main` package and embeds its own frontend and
OCR helper files in the EXE. The original project's documentation is available
in the [upstream README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md).

## Structure

- `item_window.go` — Item Scanner Wails window, encrypted API key, settings, English title OCR and manual search
- `item_tracking.go` — character selection, Items Tracker and inventory updates
- `progress_cache.go`, `progress_add.go` and `progress_work.go` — confirmed in-memory progress, immediate durable stock operations and serialized background Market synchronization
- `item_stock_editor.go` and `stock_edit_journal.go` — current Items Tracker settings, separate source corrections and durable verification of pending Market edits
- `market_fallback.go`, `market_fallback_sync.go` and `market_fallback_local_first.go` — separate supplementary Market stock, per-item upload intents, fair recovery and durable readback-only verification
- `item_handover.go` and `stock_usage.go` — explicit typed local-first hand-ins and immutable verified Market usage receipts
- `local_progress.go` — quest/hideout requirements in the existing Items Tracker and Add routing between Market and local stock
- `item_hotkey.go` — Add availability, immutable item/character targets and a worker shared with the Add button's stock operation
- `tracked_hotkey.go` and `tracked_hotkey_windows.go` — configurable Regular/FiR shortcuts, global Windows key consumption, physical press/release tracking and dedicated keyboard hook lifecycle
- `inspect_clicks_windows.go` — automatic scan trigger after a click
- `paired_windows.go` — companion window startup, display and shutdown; a callback requests shutdown of the main application
- `internal/market` — catalog, prices, characters, requirements and the documented inventory update operation
- `internal/tarkovdev` — read-only public task/hideout catalogs and separate game-mode caches
- `internal/localprogress` — pure requirement merge, alternative groups and atomic local-only stock persistence
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
version is maintained independently in `app/plus/version.go` (`1.1.2`). A Plus
build can increment only its own version; releasing the extension does not change
the original version. The Pilot heartbeat sends the original version to the
website. The UI labels both versions separately, while the release package's
`versions.json` also records the upstream commit. Wails and Windows EXE version
metadata use the Plus product name and version.

Every build creates `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` and the
matching ZIP archive. Both contain `TarkovPilotPlus.exe`, `versions.json` and the
complete `ocr` directory. The executable name stays `TarkovPilotPlus.exe` even
when the build is given a custom compiler output filename. The current package
version is `1.1.2`.

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
Closing either app window stops the companion process and its hook. No keyboard
hook is installed in the game process, and game memory is not accessed.

The configured shortcut's down, repeat and up events are consumed for
normal Windows keyboard input while it is enabled, even when the
tracker is unavailable. Other keys and modifier combinations continue normally.
Holding the shortcut does not repeat Add. Injected input does not trigger stock
changes, and injected releases cannot reset a physical key hold.

The keydown callback captures an immutable eligible item/character target with
atomic operations and a nonblocking send. It does not run OCR, make API requests,
call Wails or take application locks. The worker drops extra presses while busy,
rejects stale targets, and uses the same cached requirement validation and durable
local save as the Add button. Market additions wait in the separate supplementary
pool; the background worker checks live stock and its revision before uploading.
The Add button remains usable when the shortcut is disabled or
its hook cannot start.

Windows hook consumption does not guarantee priority over every input path.
Applications using direct input, protected system input or another input handler
may still observe the key. Automated tests cover key-event suppression decisions,
target validation and API updates with controlled fixtures; actual physical
keyboard behavior in the game requires a live trial.

## Local Progress

Local Progress is always active and uses the same character selection, scan results
and Add worker. Three Market reads (tracked items, quests and hideout) must have matching
character, game mode and revision before dev requirements are merged. Market
progress determines which goals are complete. The dev catalog cannot create
personal goals that are absent from the Market character.

The combined snapshot exists only in memory and appears in the existing Items
Tracker card. Market remains the authority for its stock quantity. The local
file stores the character, game mode, additional local item-ID/stock quantities
and their Regular/FiR counts, plus immutable receipts for explicitly handed-in
local quantities. Collected totals remain saved; usable stock excludes those receipts.
It also stores Regular/FiR annotations for confirmed Market additions and removals,
with a stock fingerprint to detect changed quantities. These
annotations never add to the Market quantity. Market responses, complete Market
stock counts, requirements, goal states and progress revisions are excluded.

After the first confirmed load, Inspect changes and Add use the cached requirement
graph without foreground progress requests. Add, Hand in Items and stock edits
are persisted locally before their updated stock appears. Market changes use
durable versioned intents and a serialized background worker. Refresh requests
join that queue without canceling an upload or dropping pending stock. Settings
shows Pending uploads with operation and item counts, item names and reasons
for waiting. A restart continues saved work, while uncertain writes are checked
without automatic replay. Each item has a separate upload intent; an unresolved
item does not prevent other items from uploading. Rejected uploads wait before
another attempt, allowing the queue to advance.

An upload captures only its own typed packet. Immutable transfer receipts remove
that packet once, preserve additions received during the request and do not count
an upload as a hand-in. The complete Market inventory remains in memory. Existing
item catalogs can also be read while their expired prices refresh in the background.

The Items Tracker card and its Found in Raid, Regular, Any (FiR) and Any rows
remain visible when stock covers their requirements, for example 10/10. Add
buttons and shortcuts cannot add another piece unless an eligible requirement
still needs it. Completing or canceling the associated goal removes its active
requirement. Stock remains available in the settings editor. A raw stock total
cannot satisfy an unmet FiR requirement with known Regular stock.
If a fresh Market response removes an item or exposes multiple variants sharing
its ID, its previous unique stock is invalidated immediately. Only that item is
protected until the complete requirements refresh; its saved local stock remains.
Changes to its required quantity or source rules receive the same protection,
including a changed FiR requirement with an unchanged total. Raw source comparisons
stay in memory; source ordering and stock-only revisions do not block local additions.

Regular and Found in Raid both fill the current Market requirement first,
then save additional quantities locally. Items absent from Market use local stock
directly. The Stock row always shows an available/required ratio, combining live
Market and local quantities. Once all Any groups are covered and no other
collection requirement remains, Stock displays the item's available quantity
as a complete ratio, such as 2/2 or 0/0, while Any remains 8/8. This display
convention does not change the stored collection requirement or Add eligibility.
The existing Regular and Found in Raid rows show allocated stock against fixed
requirements.
Any requirements appear in their own rows without being counted again in the
fixed requirement rows.
Known Regular stock cannot satisfy FiR requirements. FiR stock satisfies FiR
requirements first and can cover Regular requirements with the remainder. Each
piece is allocated once. Regular additions require an eligible unfilled Regular
requirement; Found in Raid additions can fill an eligible FiR or Regular requirement.
The buttons and shortcuts use the backend's type-specific availability, and the
backend rechecks the full allocation before saving. A type is rejected when no
remaining requirement can use another piece; summarized Any display totals do
not determine Add availability. Earlier unclassified stock keeps the manually accepted
suitable-stock behavior, without being relabelled as known FiR.

Market-bound additions always enter the separate `market-fallback` store before
upload, retaining their Regular/FiR type. This source contains only pieces not yet
uploaded; it never copies live Market inventory or dev local stock. It counts
toward the Market quota and the combined Stock ratio. An explicit Market no-op or
definite rejection keeps that same saved packet; an uncertain write does not
create another piece and retains readback-only behavior. A confirmed Market write
whose local settlement fails keeps its durable intent for verification.

Saved stock changes wake the background worker, and a one-minute timer refreshes
progress and checks waiting work. Failed packets have a one-minute cooldown;
other eligible items can continue immediately. Settings Refresh joins the same
queue. The worker saves a separate item upload intent before one absolute PATCH,
then requires fresh stock at the target quantity with a new revision. Verified
settlement removes only the uploaded supplementary available stock and carries
its types into the Market annotation; recorded usage is preserved. This transfer
does not change combined stock or Used. Each small `market-fallback-sync` intent
stores no API key or complete inventory and is removed after settlement. Pending
intents protect only the affected Market and supplementary stock and resume
through GET only, including after restart or a local settlement failure. Definite rejection or confirmed
no-op retains the supplementary stock for a later attempt.

For a local stock update, Add commits the file before displaying the increment.
Corrupt or unwritable files are preserved and local stock changes stop. Public
dev requirements can use their valid cached catalog during a service outage.
If a temporary Market read failure occurs after a valid combined snapshot has
loaded, an addition for Market can use the last confirmed requirements and save
only the new piece in supplementary stock. The same character, mode and settings
revision must still be active; the UI identifies the cached requirements. Cold
startup, changed settings, invalid identity/schema and unavailable dev or local
storage still stop stock changes; the app does not switch to a Market-only mode.
Duplicate item-ID variants retain their existing write restrictions. Separate
FiR inventory blocks Market writes; independent local stock can still be used
once the Market requirement is met, including mixed Market/dev items. When an
external Market quantity differs from the recorded fingerprint, including a reset
to zero, only its Market Regular/FiR annotation becomes unclassified. Additional
local stock, its Regular/FiR counts, local used quantities and hand-in receipts
remain unchanged. Zero Market stock plus one available local piece still contributes
one piece to Stock. Market supplementary stock and its receipts also remain
independent of an external Market reset. Hand-in consumes suitable dev local
stock first, then supplementary Market stock, then live Market stock.

Alternative candidates belong to shared Any groups. Mandatory requirements
reserve suitable stock first, then a deterministic maximum-flow allocation shares
the remainder between overlapping groups. The existing tracker shows at most
one Any (FiR) row and one Any row, summing active group progress by type even
when their eligible item sets differ. Covered groups remain in these display
totals until their associated goals are completed. This display summary does not change stored stock,
individual goal identities, eligible items or allocations. Groups with the same
candidate set and FiR condition can already share progress before display, including
covered active groups. Covering a group stops further collection for all
alternatives without completing the quest, and its filled Any row remains
visible. Other mandatory requirements remain visible. Completing the associated
Market goal closes its dev additions. Completed Any goals show their required
group item count once, without identifying which alternatives were handed in.
Ambiguous Market objective associations keep
their original requirements instead of guessing an Any relationship.

## Hand in Items

The main window's Hand in Items button is above Events and opens the hand-in
dialog in the Item Scanner. The Item Scanner also has a hand-in button above its
scan status. An API key and a selected character are required.
Every new list starts empty, independently of the item currently shown in the
scanner; no previous scan is required. A scan or search started before opening
the list cannot populate it. A saved pending hand-in reopens its explicit list
for verification and resume.
Scan now, Automatic scanning and Search are available inside the dialog. Its
automatic scanning setting is temporary and leaves the normal scanner setting
unchanged. Each recognized item type enters the list once; repeated Inspect
windows and OCR variants do not imply more physical pieces. Quantities can be
edited, and each row selects Regular or Found in Raid. Add other type permits
both qualities of the same item; the trash button removes a mistaken entry.

Confirm hand-in validates the entire list, records matching dev local stock first,
then matching supplementary Market stock,
then saves the remaining Market deductions for one background absolute PATCH
per item ID.
Known types are kept separate; older unclassified stock can use the selected
type. The app never chooses which alternative items were physically handed in.
The selected type also reduces the matching local Market annotation: a Regular
hand-in preserves the remaining Found in Raid counts, and vice versa.
Quest completion only closes requirements and does not deduct stock again.
The user hands the items in in the game and marks the quest or hideout goal
complete on Tarkov Market after recording stock.

The sources cannot commit one joint transaction. The short durable hand-in commit
protects character changes; it checks the captured profile again before publishing.
A private pending-operation
file records the explicit hand-in list, both local source receipts and remaining Market
write intents before any deduction. It contains no API key or complete Market
snapshot and is deleted when finished. An open hand-in draft protects its list;
a committed local-first hand-in protects only its affected items while the
remaining Market changes are pending. Closing a pending dialog keeps the journal; Resume hand-in
verifies completed steps and never repeats a recorded local deduction.
An uncertain Market request is checked against its saved absolute target.
A request that might already have been sent is verified by reading Market
stock, without an automatic repeat. An explicit retry requires the saved
baseline and revision to remain valid; unresolved changes retain the journal.

The Market client paces progress reads and writes within a shared 300-request
per-minute gate, with a separate 60-attempt-per-minute PATCH gate. Multiple pieces
of the same item are grouped into one PATCH, and server retry delays are honored.

## Items Tracker settings and backups

Settings lists positive combined available stock in an Item, Stock, Used and Edit
table. Edit expands per-source quantity and quality controls below that item.
Zero available stock hides the item without deleting its saved hand-in history.
The source labels are Tarkov-Market Progress and Local Progress; supplementary
Market stock is labelled Tarkov-Market Progress — pending upload.
Pending supplementary uploads protect only that item's Market and supplementary
stock. Independent Local Progress stock and other items remain usable, including
in Hand in Items. The complete hand-in list is checked before any deduction;
saved operations retain their original source allocation when resumed.
Retry saved upload explicitly checks and retries the saved absolute target when
its original revision and baseline still match. A fresh matching read with an
unchanged baseline, a different revision and the complete supplementary amount
still present clears only the obsolete journal, without another write or any
stock or usage change. Other changes keep the pending journal; invalid journals
keep stock protected. Normal refresh and startup also check existing intents.
The explicit retry and background worker share one network queue and never wait
for network results while holding the stock-operation lock. Background updates
preserve unsaved editor input; a changed stock generation requires refreshing
the list before saving that draft.
Closing Settings during a scan or stock operation defers closing the editor
until that operation ends, so the hidden editor cannot keep Add or Hand in Items
blocked.

Import and Export use one `.tpp-local-backup.json` file for every character and
game mode. It includes local-progress, market-fallback, stock-usage, handovers,
stock-edits and market-fallback-sync files, preserving quantities, quality,
usage and valid pending operations. It excludes credentials, settings, catalogs
and live Market inventory. Native confirmation precedes replacing all six local
directories; previous saves stay in `.local-progress-backups`. Validation happens
before replacement, and the startup recovery journal handles interrupted imports.
Precisely identified temporary files left by an interrupted app save are excluded
from export. Import retains them with the previous saves; unrelated files and
invalid canonical save files continue to fail validation.
Import itself does not send an API request. The background worker later checks
restored pending operations: unsent intents require a fresh matching baseline,
and previously sent or uncertain requests are verified without being repeated.
