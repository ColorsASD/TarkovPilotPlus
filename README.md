# TarkovPilot+

TarkovPilot+ is a Windows companion app for Escape from Tarkov. It extends [TarkovPilot](https://github.com/ggdiam/TarkovPilot) with a separate item scanner window, market prices and character-specific Items Tracker information.

For the original map, screenshot and game-log features, see the [original TarkovPilot README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md). This README describes this fork and its own additions.

The current Plus version is **1.1.0**, based on **TarkovPilot 2.1.1**. Their source code and version numbers are maintained separately. Both app window title bars display **TarkovPilot+**, and the app interface is in English.

## Features

- **Item scanner window.** The item window opens with the main app. Closing either window closes both.
- **Local English name scanning.** The bundled Tesseract engine reads the title of an open item Inspect window. It does not use Windows OCR. Automatic scanning checks the foreground game window after a left-click; it does not continuously search the whole screen for item names.
- **Manual scanning and search.** Click **Scan now**, switch back to the game within 3 seconds and keep the Inspect window open. Alternatively, enter an English item name and click **Search**. Both actions remain available when **Automatic scanning** is off.
- **Tarkov Market prices.** Flea Market shows **Best price** and **24-hour average price**. Trader shows **Best price** and the trader's name. Each price includes its per-slot value in parentheses, and trader prices keep their own currency. Missing or unavailable prices display `—`.
- **Items Tracker.** Select a character to see shared stock and separate **Found in Raid**, **Regular**, **Any (FiR)** and **Any** requirements. The **Stock** row shows available stock / active collection requirement, such as `2/5`. The entire panel is hidden when no typed requirement remains unfilled; a raw stock total cannot hide an unmet FiR requirement. Stock remains available in Settings, and a later hand-in can make the panel visible again.
- **Explicit stock updates.** **Add Found in Raid item** and **Add Regular item** save one piece locally and update the selected character's displayed stock immediately, using the destination described below. Market changes synchronize in the background. Character selection uses unique IDs, so characters with the same name remain separate. Choosing a character also selects its PVP, PVE or SEASON game mode.
- **Global Add shortcuts.** By default, press **F1** for Found in Raid or **F2** for Regular without leaving the game. The Add button remains available. In Settings, configure each shortcut independently with a key or a Ctrl/Alt/Shift combination, or choose Disabled. Supported keys include F1-F12, letters, numbers, Space, Insert, Delete, Home, End, PageUp and PageDown. Each shortcut works only for a loaded item whose Add action is available, binds the request to that item and character, and adds at most one item per physical press. Holding the key or pressing it while an update is running does not queue repeated additions. Enabled shortcuts are consumed globally for normal Windows keyboard input, even when no item can be added; other keys and modifier combinations keep their usual behavior. Applications using direct keyboard input or another higher-priority input handler may still receive the keys.
- **Completion information.** While an active requirement remains, completed quest and hideout sources appear separately, for example **Quest completed — 4 items completed**. Optional completions are labelled. These values do not increase stock or active requirements.
- **Local Progress.** Local Progress is always active and includes missing quest and hideout requirements in the existing **Items Tracker**, with its original labels and layout. Tarkov Market requirements and completion remain primary; the public tarkov.dev catalog fills missing item links for the same goals. The Stock ratio combines the required quantities and stock. Add and F1/F2 fill Market stock up to its required quantity, then save additional quantities locally. Items absent from Market also use local stock.
- **Items Tracker settings.** Open the Items Tracker table in Settings without scanning an item first. It shows only items with currently available Market, Local or pending-upload stock, with Stock and Used columns. Search the list and expand Edit to correct Found in Raid, Regular and Unclassified stock, or clear one source's stock. **Tarkov-Market Progress** and **Local Progress** have separate Save and Clear stock actions. Used shows recorded hand-ins with a type breakdown for each source; correcting stock does not record a hand-in. Items disappear from this table when their combined available stock reaches zero; their saved usage remains intact.
- **Local save backups.** Settings Import and Export save local quantities, Found in Raid/Regular types, hand-in usage and pending operation journals for every character and game mode in one `.tpp-local-backup.json` file. Import validates the complete file and asks before replacing current local saves, which are kept in an automatic backup. API keys, settings, catalogs and current Market inventory are excluded. Import sends no Market request.
- **Background Market synchronization.** Market-bound additions enter separate supplementary stock before any upload. Temporary Market read outages can use the last confirmed requirements for the unchanged character and settings. The tracker includes these pieces immediately; verified uploads move them to Market without changing total stock or Used. Settings shows this source as **Tarkov-Market Progress — pending upload**, and **Pending uploads** lists the affected items, quantities and reasons for waiting. Saved work continues after restart.
- **Protected API key.** The item scanner's API key is stored with Windows DPAPI encryption for the current Windows user. The field is masked by default; the eye icon explicitly shows or hides the saved key.
- **Current help and scan details.** **How it works** explains the controls. **Scan details** shows detected Inspect windows, recognized titles and the result. The catalog timestamp appears without a source label, in local `YYYY.MM.DD. HH:mm:ss` format, for example `2026.10.11. 00:30:23`.

Item names, prices, images, characters and primary tracking data come from the [Tarkov Market API](https://tarkov-market.com/dev/api). The catalog is cached locally for five minutes, and requests respect rate limits and server retry delays. Character and inventory responses are not written to the catalog cache. Tracked Items requires the appropriate API PRO access. Local Progress also reads public task and hideout requirements from [tarkov.dev](https://tarkov.dev/api/); its public cache lasts one hour and keeps PVP, PVE and SEASON separate.

Local Progress matches quest IDs and aliases, and hideout module IDs and levels. It adds only missing goal/item relationships and keeps the Market quantity and FiR condition when a relationship already exists. Completing a quest or hideout upgrade on Market removes all its active requirements, including dev additions. Other unfinished goals still keep their own requirements. Alternative candidates belong to the same **Any N of M eligible items** requirement. General item-use counts are not assumed to mean item quantities; only verified single-use flare objectives are included.

After the first confirmed load, Inspect changes and stock operations use the in-memory requirement graph without foreground progress requests. Add, Hand in Items and Settings stock edits persist their local change before updating the display. A serialized background worker refreshes Market progress and uploads saved changes. Refresh progress joins that queue without canceling an upload or dropping pending stock. Catalog prices can also appear from cache while refreshing in the background.

If a fresh Market response removes an item, exposes multiple variants sharing its ID or changes its requirements, the previous cached requirements for that item are invalidated until a complete matching refresh. Saved local stock remains intact. Other verified items remain usable; stock-only revisions do not block local additions.

When an item's Market stock changes externally, including a reset to zero, only its local Market Regular/FiR annotation is cleared. Additional local stock, its Regular/FiR counts and recorded local usage remain unchanged. Market stock of zero with one available local piece therefore still shows Stock 1.

The app starts background work after a saved stock change and refreshes progress once per minute while running. Each item has a separate durable upload intent; a rejected or unresolved item does not hold up other eligible uploads. Failed packets wait before another attempt. Only a fresh, matching Market readback confirms an upload. An uncertain write protects only that item's Market and pending-upload stock; its independent Local Progress stock and other items remain usable. Refresh and restart verify existing intents without repeating the upload, and Settings still loads readable stock. An invalid journal keeps stock protected until it can be checked. Retry saved upload uses the same network queue and explicitly checks the pending operation before retrying its saved absolute target, with the revision check described below. These safeguards also apply if local settlement fails after Market accepts the quantity.

When the API reports separate FIR inventory, the app does not invent separate stock fields or send undocumented updates. Market stock updates are disabled in that case; independent local stock can still be used once the Market requirement is met, including mixed Market/dev items. Variants sharing the same item ID appear in separate Items Tracker panels, each with its own API name, stock, requirements and completion details. Panels with no active required quantity stay hidden. The documented update endpoint accepts only the shared item ID, so Add remains disabled for these variants until the API supports targeting them individually.

## Installation

1. Obtain this fork's Windows x64 ZIP, named `TarkovPilotPlus-<version>-windows-x64.zip`, from its [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases).
2. Extract the complete ZIP to a writable folder. Keep `TarkovPilotPlus.exe`, `versions.json` and the entire `ocr` folder together.
3. Run `TarkovPilotPlus.exe`. Configure the original Pilot connection key and game folders in the main window if you use its map and log features.
4. In the item window, open **Settings**, enter your Tarkov Market API key and click **Save**. This API key is separate from the original Pilot connection key.
5. Select a character/game mode and use English item names in the game. Use windowed or borderless mode for Inspect scanning.

The local OCR helper requires Windows PowerShell 5.1, .NET Framework 4.8 and the Microsoft Visual C++ 2015–2022 x64 runtime. OCR DLLs, the English model and their licence files are included in the ZIP; the system runtimes are not.

## Building

Build on Windows x64 with Go 1.25.x and Wails CLI v2.13.0:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
cd app
.\build.ps1
```

The frontend consists of static HTML, CSS and JavaScript. A Node/npm frontend build is not required.

The build prepares OCR dependencies from pinned official sources and verifies their SHA256 hashes. Every build creates the complete `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` package, containing `TarkovPilotPlus.exe`, the matching `ocr` folder and `versions.json`. It also creates `TarkovPilotPlus-<version>-windows-x64.zip` and its `.sha256` file. The current package uses version `1.1.0`. OCR does not download models or call an external OCR service at runtime.

The default compiler output is `TarkovPilotPlus.exe`. Even when a custom `-OutputFilename` is supplied, the executable is moved into the complete package under the name `TarkovPilotPlus.exe`; the same name is used inside the ZIP. No extra standalone compiler EXE remains in `app/build/bin` after a successful build.

`build.ps1` reads both version sources and may automatically increment only the Plus patch version. `versions.json` records the original version, its repository and upstream commit, and the independent Plus version.

## Workspace cleanup

[`Clean-Workspace.ps1`](Clean-Workspace.ps1) removes only the explicitly listed obsolete builds, research files, test output and generated Wails bindings. It keeps the source code, both maintained `frontend/dist` folders, the current Go/Wails tools, the OCR runtime and the current version's complete release package.

Run it from the repository root to preview the checked paths and their total size. The default command does not delete anything:

```powershell
.\Clean-Workspace.ps1
```

To delete the files shown in that preview, run:

```powershell
.\Clean-Workspace.ps1 -Apply
```

The script checks repository boundaries and rejects symbolic links or junctions before deleting files. Wails regenerates `app/frontend/wailsjs` during the next build; these generated bindings are ignored and are not maintained frontend source. The current version's extra standalone EXE is removed only when its SHA256 matches the EXE in the complete release package.

## Testing

From `app`, run the Go checks:

```powershell
go test ./...
go vet ./...
```

To prepare the real OCR runtime and run scanner tests without relying on cached test results:

```powershell
.\plus\ocr\prepare.ps1
go test -count=1 -v ./plus/internal/scanner
```

Some scanner tests require the prepared OCR runtime and otherwise skip. The automated checks include the helper protocol, synthetic Inspect geometry and controlled recognition results. They do not capture the user's game window or establish recognition accuracy in a live game. Full OCR validation also requires running the built app against actual English Inspect windows at the intended resolution and UI scale.

For JavaScript syntax checks, use Node.js:

```powershell
node --check frontend/dist/app.js
node --check plus/frontend/dist/item.js
```

## Source and versions

- [`app/`](app/README.md) contains the original Pilot app and the integration that starts the companion window.
- [`app/plus/`](app/plus/README.md) contains the Plus scanner, market client, tracker, OCR helper and item frontend. Its Wails binding is `window.go.plus.ItemWindowApp`.
- `app/version.go` records the original TarkovPilot version; `app/plus/version.go` records the Plus version.
- `app/plus/upstream.json` pins the original repository, commit and version. The Plus developer README describes upstream updates and the integration points that must be preserved.

The main app has integration changes for paired windows, English text, independent version display and the Plus update policy. Keeping the Plus package separate does not make the entire main app an unchanged upstream copy.

## Privacy and local files

The item scanner recognizes text locally from screen captures. It does not read or modify game memory, and it does not upload screenshots or recognized screen text to an OCR service. It checks the foreground window/process to decide whether an automatic scan should run.

The original Pilot features read game logs and watch game-created screenshot files. They send screenshot filenames, map and quest events, and app status to Tarkov Market using the Pilot connection key. App status includes the game and screenshot folder paths. Screenshot filenames can contain in-game position information; the image files themselves are not uploaded by these features. **Clean session screenshots** optionally deletes session PNG files on map change and app exit.

Original Pilot settings remain in `%APPDATA%\TarkovPilot`. Item scanner settings, the encrypted API key and the public catalog cache are stored separately in `%APPDATA%\TarkovPilotPlus`. The API key is excluded from normal frontend state; showing it requires an explicit eye-icon request. Market character, inventory and completion responses remain in memory; the selected character ID is saved as a setting.

`%APPDATA%\TarkovPilotPlus\local-progress` stores additional local item-ID/stock quantities and their Regular/FiR counts, partitioned by character and game mode. A separate annotation records the Regular/FiR type of confirmed Market additions; it does not duplicate the Market stock quantity. A quantity fingerprint detects externally changed Market counts and clears only the corresponding Market type annotations, preserving local stock and hand-in receipts. Market responses, full Market stock counts, completion flags and progress revisions are excluded. `%APPDATA%\TarkovPilotPlus\dev-cache` contains the separate public tarkov.dev catalog.

`%APPDATA%\TarkovPilotPlus\market-fallback` stores only the app's Market pieces awaiting upload, their types and their own hand-in receipts. It is separate from dev local stock and survives an external Market reset. `%APPDATA%\TarkovPilotPlus\market-fallback-sync` holds separate item upload intents with the character, mode, revision, before/target quantities and captured type totals. Each intent stores no API key or complete Market inventory and is deleted after verified settlement. Its transfer receipt consumes only the captured packet once, preserving pieces added during the upload. The upload is a stock transfer, not a hand-in.

Regular and Found in Raid both increase Market stock while its requirement is being filled, then increase only the additional local stock. Regular can be added only while an eligible Regular requirement remains unfilled; Found in Raid can fill either a FiR or Regular requirement. Add buttons and shortcuts stop accepting a type once no remaining requirement can use it. Their default shortcuts are F2 and F1, respectively. The existing tracker combines both quantities and shows Regular/FiR requirement ratios. Mandatory requirements reserve suitable stock before shared Any groups, so a piece covers one requirement at a time. Open Any group progress is summarized in one Any (FiR) row and one Any row, while each quest keeps its own eligible items and allocations. Already covered rows are excluded from these display totals; covered alternatives stop requesting more items. This display summary does not change Stock. Neither additional local stock nor dev requirements are uploaded to Market.

**Hand in Items**, above Events, opens an independent empty list with Scan now, Automatic scanning and Search. Set the quantity and choose Regular or Found in Raid for each entry; Add other type allows both qualities of one item, and the trash icon removes mistakes. Confirm validates the complete list and records matching dev local quantities first, then Market pieces awaiting upload, then the remaining Market quantity and its matching local type annotation. These deductions appear immediately from the saved operation, while Market requests run in the background. The short durable commit protects character changes. Collected local totals remain saved with separate usage receipts, including after an external Market stock reset. A pending operation can be resumed without repeating confirmed deductions. After recording, hand the items in in the game and mark the goal complete on Market; completion removes its requirements without another stock deduction.

The Settings Items Tracker table edits available stock independently of hand-ins. Only positive combined available stock appears; recorded usage or an unfilled requirement alone does not create a row. Edit expands the source controls below the item. Clearing stock preserves recorded usage and quest/hideout requirements. Saves update the display from a durable local correction; a changed Market quantity is sent as one absolute update in the background and verified against fresh stock. An unchanged Market quantity needs only a local type correction. An unverified update remains pending in `%APPDATA%\TarkovPilotPlus\stock-edits`; Refresh checks it without automatically repeating a sent update. Background refreshes preserve unsaved editor input and require a refreshed list before saving a stale draft. Item shortcuts pause while the editor is open so quantity fields can use any configured shortcut key. Closing Settings during a scan or stock operation also closes the editor when that operation finishes, restoring the usual Add and Hand in Items controls.

`%APPDATA%\TarkovPilotPlus\stock-usage` keeps immutable receipts for Market deductions verified by Hand in Items. Used combines these receipts with local hand-in usage. The API's spent quantity appears separately as Market accounted because it can overlap the same hand-ins. Older Market usage has no reconstructable type breakdown; it is not invented or added twice. Neither file stores a copy of current Market stock.

Import and Export are available independently of the open stock list, including without an API key or while an operation awaits verification. The backup preserves all character/game-mode partitions without relabeling them. Import replaces the six local save and journal directories only after complete schema, quantity, receipt and checksum validation. Precisely identified temporary files left by an interrupted app save are excluded from export and kept with previous saves on import; unrelated files and invalid canonical saves still fail validation. Current files are retained under `%APPDATA%\TarkovPilotPlus\.local-progress-backups`. An interrupted replacement rolls back on the next start. Import sends no Market request; background work later verifies restored pending operations. Configure the Market API key separately on another computer and select the same character to use the restored local save.

Retry saved upload first reads current Market stock. An already confirmed target settles the saved transfer without another write. An unchanged baseline and revision permit one retry of the original absolute target. If a fresh matching read shows the baseline quantity unchanged, a different revision and the complete supplementary stock still present, the old upload is obsolete: refresh or retry clears only its journal without sending another write or changing stock or Used. A different quantity, changed supplementary stock or an unresolved result keeps the journal for verification, protecting only the affected Market and supplementary source. An uncertain attempt is never discarded just because a later request reports no change. Hand in Items checks the whole selected list before deducting anything and can still use the affected item's independent Local Progress stock or other items.

## Updates and licences

Install Plus updates manually from this fork's [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases), keeping the new EXE and its matching `ocr` folder together. The original automatic EXE updater is disabled for this fork so an upstream release cannot replace the Plus app.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the bundled OCR components and licences. Keep the licence files in `ocr` when distributing the app.
