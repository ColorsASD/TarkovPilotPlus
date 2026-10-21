# TarkovPilot+

TarkovPilot+ is a Windows companion app for Escape from Tarkov. It extends [TarkovPilot](https://github.com/ggdiam/TarkovPilot) with a separate item scanner window, market prices and character-specific Items Tracker information.

For the original map, screenshot and game-log features, see the [original TarkovPilot README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md). This README describes this fork and its own additions.

The current Plus version is **1.4.2**, based on **TarkovPilot 2.1.1**. Their source code and version numbers are maintained separately. Both app window title bars display **TarkovPilot+**, and the app interface is in English.

## Features

- **Separate item window.** The companion opens with the main app. Closing either window closes both; unsaved Market updates trigger a warning before exit.
- **Local English OCR and search.** Recognize an item's Inspect title automatically or manually, or search by its English name without opening an Inspect window.
- **Market prices and progress.** Compare Flea Market and trader prices, then view the selected character's stock, requirement categories and quest/hideout details.
- **Next Items and completed goals.** Read independent quest/hideout coverage, FiR/NonFiR breakdowns and completed checkmarks from your locally recorded item quality. Other Stock Details identifies contributions from other items. Collection limits keep the existing target priorities. Copy item or goal names with the small brown icons.
- **Stock controls.** Add or remove Found in Raid and Regular pieces separately with four buttons or configurable F1–F4 shortcuts. Eligible changes appear immediately and save the total to Tarkov Market in the background.
- **Items Tracker editor and backups.** Classify existing stock in the separate Items Tracker panel above Settings. Import or export the selected character and game mode's local classification as JSON without changing API totals.
- **Local settings and quality.** The API key is encrypted with Windows DPAPI. Confirmed Found in Raid/Regular classifications and a small quality recovery journal are saved locally; total stock remains authoritative on Tarkov Market.

## Installation

1. Obtain this fork's Windows x64 ZIP, named `TarkovPilotPlus-<version>-windows-x64.zip`, from its [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases).
2. Extract the complete ZIP to a writable folder. Keep `TarkovPilotPlus.exe`, `versions.json` and the entire `ocr` folder together.
3. Run `TarkovPilotPlus.exe`. Configure the original Pilot connection key and game folders in the main window if you use its map and log features.
4. In the item window, open **Settings**, enter your Tarkov Market API key and click **Save**. This key is separate from the original Pilot connection key. Tracked Items requires the appropriate API PRO access.
5. Select a character and game mode. Characters use unique IDs, so matching names do not merge their inventories. Selecting a character also selects its PVP, PVE or SEASON mode.
6. Use English item names in the game and windowed or borderless mode for Inspect scanning. Leave the complete `ocr` folder beside the EXE.
7. Open **Items Tracker** above Settings to classify existing stock as Found in Raid or Regular. Unclassified pieces remain Uncategorized and do not cover requirements.

The local OCR helper requires Windows PowerShell 5.1, .NET Framework 4.8 and the Microsoft Visual C++ 2015–2022 x64 runtime. OCR DLLs, the English model and their licence files are included in the ZIP; the system runtimes are not.

The saved API key is masked by default. The eye icon explicitly shows or hides it; ordinary frontend state does not contain the key.

For a normal session, select the correct character, scan or search for an item, then use the matching Found in Raid or Regular stock action for each piece you collect. Use the separate Items Tracker editor to classify stock that was already on Market. Keep the app open until its pending saves finish.

## How it works?

The in-app help provides a short control reference. The sections below explain the counters and the differences between collected stock, an active requirement and a completed goal.

### Find an item

Name scanning always uses English. Open the item's **Inspect** window in the game and keep its title visible.
With **Automatic scanning** enabled, the app checks the foreground game window after a left-click and reads the detected Inspect title with bundled Tesseract.
It does not use Windows OCR or continuously search the whole screen for names.

For a manual scan, click **Scan now**, switch back to the game within **3 seconds** and keep Inspect open.
Manual scanning remains available when Automatic scanning is off.
To search without Inspect, enter the English item name and click **Search**; this also works with Automatic scanning off.

**Scan details** shows the detected Inspect windows, recognized titles and result.
The catalog timestamp uses local `YYYY.MM.DD. HH:mm:ss` formatting, for example `2026.10.11. 00:30:23`.
If recognition misses a short title or the wrong item is selected, inspect these details or use manual name search.

### Settings

| Setting or control | What it changes |
|---|---|
| **Automatic scanning** | Checks the foreground game's Inspect window after a left-click. Turning it off leaves Scan now and name search available. |
| **Game mode / Tracked Items character** | Selects which Market prices and character inventory to use. Choosing a character also selects its own mode; inventories with matching names stay separate. |
| **Four stock shortcuts** | Changes or disables the individual Add/Remove keys. Focus a field and press the desired shortcut; enabled shortcuts must be different. |
| **Tarkov Market API key** | Save or clear the item window's API credential. The eye icon reveals or hides it. This is separate from the main Pilot connection key. |
| **Refresh progress** | Reloads the selected character's inventory, quests and hideout progress from Market. It does not add stock or complete a goal. |

Changing the character or mode also changes which local classifications are used. Check this selection before editing stock or importing a classification backup.

### Read prices

**Flea Market** shows the current **Best price** and **24-hour average price**.
**Trader** shows the best trader price and the trader's name, using that trader's own currency.
Each available price includes its price per inventory slot in parentheses. Missing or unavailable prices display `—`.
Prices use the selected character's game mode.

Names, prices, images, characters and progress come from the [Tarkov Market API](https://tarkov-market.com/dev/api).
The public catalog is cached locally for five minutes. Requests respect API rate limits and server retry delays; character and inventory responses are not written to that catalog cache.

### Read Items Tracker

The tracker uses the selected character's Tarkov Market requirements, completion and total inventory, together with locally recorded Found in Raid/Regular quality. **Stock** shows the item's total; the rows beneath it describe requirements that can use classified pieces.
Existing stock and external increases start as **Uncategorized** until classified. Uncategorized pieces do not cover requirements. The Stock summary and Next Items goals are separate views of the same classified inventory. The summary reserves FiR pieces for FiR-only requirements and counts each physical piece once; each Next Items goal separately shows how much of its requirements the inventory could cover.

| Row | Meaning | How to read it |
|---|---|---|
| **Stock** | The selected item's available quantity and current collection target. Pending additions and removals are included immediately. | `2/5` means 2 available against a target of 5. Other eligible items can reduce a Shared collection target. |
| **Regular** | Fixed requirements that accept Regular or FiR pieces. | Uses Regular pieces first, then FiR pieces left after FiR-only requirements. A small gray **FiR ×1** label identifies any FiR contribution. |
| **Found in Raid** | Fixed requirements marked FiR by the API. | Uses only locally classified FiR pieces allocated to those requirements. Scanning does not detect the physical item's quality. |
| **Shared** | Group progress across eligible alternative items without a FiR requirement. | `3/8` comes from the Stock summary's quality allocation; other eligible item types can supply part of the 3. |
| **Shared (FiR)** | Group progress across eligible alternatives for a FiR requirement. | `6/15` uses classified FiR pieces across the group, not necessarily 6 copies of the selected item. |
| **`—/required`** | The requirement amount is known, but available API data does not establish an unambiguous allocation. | Read it as unknown allocation rather than zero stock. |

The Stock summary first allocates FiR pieces to Found in Raid and Shared (FiR) requirements in goal-priority order. Regular and Shared requirements then use the remaining inventory, with non-FiR pieces before eligible FiR pieces. Shared rows include classified stock actually allocated across eligible items. The collection target and typed Add eligibility retain their existing global goal-priority calculation. The API identifies groups, required amounts and eligible items; its untyped group counters do not establish local item quality.
The collection target credits other eligible items and their pending changes without subtracting the selected item's own stock twice.
Within this summary, overlapping fixed and Shared requirements share one allocation rather than claiming the same piece repeatedly.
Fixed and Shared FiR requirements keep their existing goal order within the FiR allocation; fixed requirements do not automatically take priority over a Shared requirement. Shared denominators retain the full group requirement, while the Stock target can already credit other items. Adding the displayed requirement denominators is therefore not a way to reconstruct the Stock target.
For one FiR Pile of meds with 20 FiR requirements and one Regular requirement, the summary shows **Stock 1/21**, **Found in Raid 1/20** and **Regular 0/1**. A separate Regular quest can still show its own coverage under Next Items. If all FiR requirements are covered and a remaining FiR piece fills Regular, the Regular row includes the gray **FiR ×1** label.

Next Items evaluates each quest and hideout goal against the classified inventory separately. A Regular requirement uses non-FiR pieces first and eligible FiR pieces only for the remainder; a FiR-only requirement accepts only FiR. Within a single goal, overlapping fixed and Shared needs cannot count the same physical piece twice. Different goals can each show the same eligible piece, so their numerators must not be added together as inventory.
For example, a Zibbo with no own stock can show **Stock 0/3**, **Regular 0/1** and **Shared (FiR) 1/3** when one FiR FireKlean is allocated to Fuel Shortage. The target is one fixed Zibbo plus the two remaining Shared pieces. **Other Stock Details** identifies the FireKlean contribution. Other eligible FiR items can cover the Shared requirement; it does not require three additional Zibbos.
Pending stock immediately participates in the Stock display, local allocation and quantity limits. The last coherent same-item goal details remain visible during background refreshes and saves.

When a collection target is covered and no other requirement remains, Stock can show its available quantity as a complete ratio, such as `2/2` or `0/0`, while a Shared row retains its group ratio.
An active row can remain visible at `10/10`: enough collected items does not complete the associated quest or hideout upgrade.

### Classify existing stock

Open the separate **Items Tracker** panel above Settings to edit classifications. Items with Uncategorized stock appear first, followed by fully classified items; both groups are alphabetized by displayed name, and search covers both. Search for an item, check its image and name, then enter Found in Raid and Regular quantities. Their sum cannot exceed the read-only **API total**; the remainder is Uncategorized. **Save classification locally** changes only this local split. Distinct same-ID variants can be classified separately; indistinguishable rows and unsupported separate FiR inventory remain read-only.

For example, an API total of `4`, Found in Raid `1` and Regular `2` leaves `1` Uncategorized. Saving that split keeps total stock at `4`. To increase the total after collecting another item, use the appropriate Add button or shortcut instead of changing the classification.

A lower API total can make the remaining quality uncertain. If another client reduces one FiR and one Regular piece to a single total piece, the app keeps one Uncategorized piece because the API does not identify which quality disappeared. The app's own four typed stock actions preserve the selected quality exactly.

Uncategorized pieces count only toward Stock. For two unclassified ComTac pieces, Stock remains `2/17`, while Regular is `0/2`, Shared (FiR) is `0/15`, and Next Items shows Hot Delivery `0/2` and Scavenger `0/15`. Classify them before they cover any requirement.

**Refresh inventory** first reloads Market progress, then refreshes the editor. An open editor also follows successful background refreshes and completion resets while keeping its search filter. Classification inputs wait while the inventory is loading; eligible stock controls can continue from the last valid same-item data.

### Completion and remaining stock

Tracker refresh loads all quests and hideout upgrade levels for the selected character, together with inventory. Local completion flags and minimal item identities are saved separately for each account, character and game mode. When a previously unfinished quest or upgrade becomes completed, every related item's remaining stock becomes Uncategorized without changing its API total. Shared and Shared (FiR) include every eligible item type, even those that contributed no allocated pieces. The first observation establishes a baseline; an already completed goal does not repeatedly clear a later manual classification.

A new completion returns affected items to the Uncategorized-first group in the inventory list. Check what you still have and classify it again. A full `x/x` ratio or a canceled goal does not trigger this reset by itself.

This local reset does not send a stock update. If Market reports a changed total after a quest or upgrade is completed elsewhere, that reported total is loaded separately. A completed checkmark, removal of an active requirement and a change in total Stock are separate observations.

### Import and export classifications

**Refresh inventory** occupies the full first button row; **Import** and **Export** appear beneath it. Export saves only the selected character and game mode's classifications to a JSON file chosen in the native save dialog. Import accepts a matching character and game mode, matches current item/variant identities and leaves every API total unchanged. Saved quantities are bounded conservatively by current stock: external decreases can make some quality uncertain, and stock added since export remains Uncategorized. Rows without a matching current identity are skipped, while an invalid or mismatched backup is rejected. The backup contains no API key, profile hash, pending stock writes or recovery journal.

Wait for inventory loading and pending stock saves before importing or exporting. A successful import reloads the local view and preserves the search filter. Canceling either file dialog leaves the classification unchanged.

### Read Next Items and goal details

**Next Items** lists the current item's quest and hideout goals. There is no category at the right of its header.
Goal ratios use locally classified stock independently for each goal. They can differ from the Stock summary, which reserves FiR pieces before allocating the remaining inventory to Regular and Shared requirements.
The pure virtual **+1** checks determine whether another FiR or Regular piece can fill an eligible need; these checks never add an item or send an inventory update.

Below the header, quest lists appear before hideout module lists.
The headings are **Quests** and **Hideout**. Small gray section labels separate the lists; item and goal names are white. The main Stock ratio is white, while requirement and goal ratios are brown.
Each name is on the left, with its current ratio at the right edge, for example **0/5**.
An active quest or hideout goal with known quality shows only positive quantities in small gray text after its name: **FiR ×2**, **NonFiR ×1**, or **FiR ×1 · NonFiR ×1**. If both quantities are zero, the entire quality label is omitted. The breakdown uses non-FiR pieces before FiR for Regular needs and updates when the classified inventory changes. Historical completed ratios and unknown allocations do not invent a quality breakdown.
These are the current requirements for this item or its Shared group, not virtual +1 results or the total of every item needed by the whole quest.
A Shared ratio can include other eligible items. Unknown allocations display **—/5**.

**Other Stock Details** appears below a Shared row, quest or hideout goal only when another item contributes allocated pieces to that displayed requirement. The brown disclosure lists a small item icon, name, quality and contributed quantity, such as **#FireKlean gun lube · FiR ×1**. A small brown copy button immediately after each item name copies only that name. The selected item, zero contributions and eligible but unused items are omitted. When both qualities contribute, their quantities remain separate. No disclosure appears when only the selected item's own stock is used.

These quantities are the pieces used by that displayed requirement, not the contributor's entire stock. Another ingredient needed elsewhere in the same quest is not listed unless it can satisfy the current item's requirement. Same-ID variants of the selected item are also treated as its own stock and are omitted from this disclosure.

The lists include the item's mandatory quest and hideout source records, including completed goals.
Canceled, optional and zero-amount sources do not contribute to these ratios.
A small brown checkmark marks a goal reported as completed; its full ratio, such as **5/5**, stays visible.
An active requirement that happens to be full does not acquire a completion checkmark.
Other unfinished goals retain their own counters when one goal is completed.

Small brown copy icons appear beside the item name in the header and beside each quest/hideout name.
Click an icon to copy **only that name**, without its ratio, group heading or completion mark.
In a completed goal row, the copy icon appears before the brown checkmark. Unchanged goal rows retain their controls during state updates; changed names update the copied text.

When every associated target belongs to completed sources, the raw requirement is zero, both Add actions are ineligible and no stock remains, the confirmed section becomes **Completed Item**.
The goal names, copy controls, completed checkmarks and full ratios remain, while the Stock summary, Regular/FiR/Shared rows, gray divider and four stock buttons are hidden.
This is a presentation change: it does not deduct or overwrite stored stock, complete another goal or rewrite stock-action eligibility.
An active filled goal or a mixture of active and completed goals stays under **Next Items**, with its current Stock and stock controls.

New quest, hideout, group and inventory responses update these details. The item classification changes which pieces each requirement can use without replacing the existing goal-priority engine.
The app uses source IDs and returned goal data rather than a hardcoded list of item or quest names.

### Add or remove stock

The two-column controls add or remove one piece of the selected quality. The upper row contains **Add Found in Raid item** and **Add Regular item**; the lower row contains **Remove Found in Raid item** and **Remove Regular item**.
Each action changes the API total and preserves its FiR/Regular classification locally. It does not hand an item to a particular quest or mark a goal complete.
Each Add action stops when no eligible active requirement can use another piece of that quality. Each Remove action requires positive stock of its exact quality and a supported, unambiguous API target; it does not require an unfinished goal.
Unavailable actions are gray and disabled. Completed Item presentation hides the four buttons when no stock remains, as described above.

### Set the stock shortcuts

By default, **F1** adds a Found in Raid item, **F2** adds a Regular item, **F3** removes a Found in Raid item and **F4** removes a Regular item. Each shortcut uses the same checks as its button.
In **Settings**, focus a shortcut field and press the desired key or key combination. Use **Disable** to release that shortcut.
All enabled actions require different shortcuts; capturing a shortcut temporarily suspends stock actions.

Supported keys are **F1–F12**, letters, numbers, **Space**, **Insert**, **Delete**, **Home**, **End**, **PageUp** and **PageDown**, optionally combined with **Ctrl**, **Alt** and **Shift**.
Each accepted physical press binds to the loaded item and character and changes stock at most once. Holding the key does not repeat the action.
Further presses can reserve eligible changes while earlier saves run in the background; item, profile or settings changes invalidate unrelated shortcut requests.

Enabled shortcuts are consumed globally for normal Windows keyboard input, including when the stock action is unavailable.
Other keys and modifier combinations keep their normal behavior. Games using direct keyboard input or another higher-priority input handler may still receive the key.
The app does not guarantee exclusive interception of every game's input path.

### Refresh and background saves

Use **Settings → Refresh progress** after changing inventory or goal completion on Tarkov Market.
Refresh reloads the current Market state; it does not add or remove stock by itself.
Loaded counters and the latest same-item goal details remain visible while tracking data refreshes, including an existing Completed Item section.
Stock buttons and F1–F4 remain available from the last valid inventory for the same item and profile, with their usual quantity and quality limits. Initial loading, missing or mismatched data, errors and unrelated busy actions still disable them. The last known counters remain with an error if refreshing fails; unavailable goal details may be hidden.
Changing the item, item ID, character or mode clears unrelated forecast details rather than showing another profile's data.

Stock actions reserve their change immediately, then a background worker reads fresh stock and its revision before sending documented absolute updates in order.
Pending pieces participate in collection limits and cannot be removed below zero.
The scanner status and main Events log heading show the pending count, for example **(3 items waiting for API save)**.

A permanent rejection removes the rejected pending change and shows the error. Temporary rate limits, read outages and requests canceled before sending keep typed changes queued with backoff and automatic continuation.
If a write outcome is uncertain, only read-back verification retries; the possibly successful PATCH is never replayed.
A successful acknowledgement remains valid if another client changes stock before the next read, and the app does not apply its saved change a second time.

Pending updates keep their order within each API-key, character and game-mode profile. An inaccessible profile remains pending while other profiles can continue saving.
Closing either window with unsaved work shows a warning. Confirming exit discards changes that have not reached Market.
Total inventory and requirements are refreshed from Market; the pending stock queue remains in memory and is not replayed after restart. Confirmed classifications and a small before-send quality recovery journal are saved locally. On restart, GET establishes the current total without another PATCH; uncertain quality left by an interrupted write can become Uncategorized for manual classification.

### Same-ID variants and separate FiR inventory

Variants sharing an item ID appear in separate tracker panels with their own API names, stock and requirement types.
Their locally classified stock contributes to the correct Shared groups according to their source records, including when another candidate is selected.
The documented stock endpoint accepts only the common item ID, so Add and Remove are disabled when that ID cannot identify the intended variant.
The app does not combine these inventories merely to make an update possible.

If the API reports separate FiR inventory, unsupported stock updates are also disabled instead of inventing undocumented quality fields.
Distinct variant identities retain separate local classifications and goal ratios, and can be edited in Items Tracker. Indistinguishable identities and unsupported separate FiR inventory keep local editing disabled. Existing stock and supported requirement details remain visible where available.

### If a counter or control looks unexpected

| What you see | What to check |
|---|---|
| Stock is positive, but category and goal coverage are zero. | Open Items Tracker and check whether the pieces are Uncategorized. The API total alone does not establish their quality. |
| Shared progress is higher than this item's own stock. | Open Other Stock Details. Eligible alternative items can supply the displayed requirement. |
| A quest ratio differs from the Stock category rows. | Next Items evaluates each goal separately; Stock reserves FiR pieces first across all requirements and counts each piece once. |
| A goal is full but has no completed checkmark. | A covered requirement remains active until Market reports completion. The app does not turn a full ratio into a completed quest. |
| Add Regular is unavailable while a FiR requirement is still short. | A Regular piece cannot satisfy a FiR-only requirement. Add actions also stop when another piece cannot fill an eligible active need. |
| Remove is unavailable despite positive total Stock. | The chosen quality may be empty or Uncategorized, or the API target may be an unsupported same-ID variant or separate FiR inventory. |
| An import is rejected or some rows are skipped. | Select the backup's character and mode, refresh inventory and wait for pending saves. Only matching current item identities can receive classifications. |
| A scan finds the wrong item or no item. | Keep the English Inspect title visible, check Scan Details and use name search when needed. The extracted `ocr` folder must remain beside the EXE. |

## Building

Build on Windows x64 with Go 1.25.x and Wails CLI v2.13.0:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
cd app
.\build.ps1
```

The frontend consists of static HTML, CSS and JavaScript. A Node/npm frontend build is not required.

The build prepares OCR dependencies from pinned official sources and verifies their SHA256 hashes. Every build creates the complete `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` package, containing `TarkovPilotPlus.exe`, the matching `ocr` folder and `versions.json`. It also creates `TarkovPilotPlus-<version>-windows-x64.zip` and its `.sha256` file. The current package uses version `1.4.2`. OCR does not download models or call an external OCR service at runtime.

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

Original Pilot settings remain in `%APPDATA%\TarkovPilot`. Item scanner settings, the encrypted API key, public catalog cache and `item-quality.json` are stored separately in `%APPDATA%\TarkovPilotPlus`. The API key is excluded from normal frontend state; showing it requires an explicit eye-icon request. Complete Market character, inventory and completion responses remain in memory; minimal completion flags and related item identities are saved with local quality, and the selected character ID is saved as a setting.

Market supplies authoritative total stock and quest/hideout progress. The local quality file stores confirmed FiR/Regular quantities by profile and variant identity, together with quality recovery records and minimal completion history; it contains no API key. The pending stock queue remains in memory and is never replayed after restart. Classification backups contain only the selected character and game mode's item classifications and identifying row data. They contain no credentials, internal profile hashes, pending writes or recovery journal, and importing them does not upload quality fields or change Market totals.

The copy controls write only the selected displayed name to the Windows clipboard when clicked. Copying a name does not submit an inventory or goal update.

## Updates and licences

Install Plus updates manually from this fork's [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases), keeping the new EXE and its matching `ocr` folder together. The original automatic EXE updater is disabled for this fork so an upstream release cannot replace the Plus app.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the bundled OCR components and licences. Keep the licence files in `ocr` when distributing the app.
