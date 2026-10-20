# TarkovPilot+

TarkovPilot+ is a Windows companion app for Escape from Tarkov. It extends [TarkovPilot](https://github.com/ggdiam/TarkovPilot) with a separate item scanner window, market prices and character-specific Items Tracker information.

For the original map, screenshot and game-log features, see the [original TarkovPilot README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md). This README describes this fork and its own additions.

The current Plus version is **1.3.0**, based on **TarkovPilot 2.1.1**. Their source code and version numbers are maintained separately. Both app window title bars display **TarkovPilot+**, and the app interface is in English.

## Features

- **Separate item window.** The companion opens with the main app. Closing either window closes both; unsaved Market updates trigger a warning before exit.
- **Local English OCR and search.** Recognize an item's Inspect title automatically or manually, or search by its English name without opening an Inspect window.
- **Market prices and progress.** Compare Flea Market and trader prices, then view the selected character's stock, requirement categories and quest/hideout details.
- **Next Item and completed goals.** Read the expected next requirement category, current goal ratios and completed checkmarks. Copy item or goal names with the small brown icons.
- **Stock controls.** Add or remove one item with buttons or configurable global shortcuts. Eligible changes appear immediately and save to Tarkov Market in the background.
- **Local settings.** The API key is encrypted with Windows DPAPI. Inventory, goal progress and pending stock changes are used in memory rather than saved as a local inventory.

## Installation

1. Obtain this fork's Windows x64 ZIP, named `TarkovPilotPlus-<version>-windows-x64.zip`, from its [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases).
2. Extract the complete ZIP to a writable folder. Keep `TarkovPilotPlus.exe`, `versions.json` and the entire `ocr` folder together.
3. Run `TarkovPilotPlus.exe`. Configure the original Pilot connection key and game folders in the main window if you use its map and log features.
4. In the item window, open **Settings**, enter your Tarkov Market API key and click **Save**. This key is separate from the original Pilot connection key. Tracked Items requires the appropriate API PRO access.
5. Select a character and game mode. Characters use unique IDs, so matching names do not merge their inventories. Selecting a character also selects its PVP, PVE or SEASON mode.
6. Use English item names in the game and windowed or borderless mode for Inspect scanning. Leave the complete `ocr` folder beside the EXE.

The local OCR helper requires Windows PowerShell 5.1, .NET Framework 4.8 and the Microsoft Visual C++ 2015–2022 x64 runtime. OCR DLLs, the English model and their licence files are included in the ZIP; the system runtimes are not.

The saved API key is masked by default. The eye icon explicitly shows or hides it; ordinary frontend state does not contain the key.

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

### Read prices

**Flea Market** shows the current **Best price** and **24-hour average price**.
**Trader** shows the best trader price and the trader's name, using that trader's own currency.
Each available price includes its price per inventory slot in parentheses. Missing or unavailable prices display `—`.
Prices use the selected character's game mode.

Names, prices, images, characters and progress come from the [Tarkov Market API](https://tarkov-market.com/dev/api).
The public catalog is cached locally for five minutes. Requests respect API rate limits and server retry delays; character and inventory responses are not written to that catalog cache.

### Read Items Tracker

The tracker uses the selected character's Tarkov Market data. **Stock** is the item's inventory; the rows beneath it describe requirements that can use that inventory.
The same piece must not be counted independently in every row.

| Row | Meaning | How to read it |
|---|---|---|
| **Stock** | The selected item's available quantity and current collection target. Pending additions and removals are included immediately. | `2/5` means 2 available against a target of 5. Other eligible items can reduce a Shared collection target. |
| **Regular** | Fixed requirements that do not require Found in Raid according to the API. | Uses the confirmed stock left after group reservations and fixed FiR allocation. It is not a separate inventory. |
| **Found in Raid** | Fixed requirements marked FiR by the API. | Uses the applicable confirmed stock after Shared reservations. The label describes the requirement, not a detected quality of the scanned item. |
| **Shared** | Group progress across eligible alternative items without a FiR requirement. | `3/8` is shared group progress; other item types can supply part of the 3. |
| **Shared (FiR)** | Group progress across eligible alternatives for a FiR requirement. | `6/15` is the group's confirmed progress, not necessarily 6 copies of the selected item. |
| **`—/required`** | The requirement amount is known, but available API data does not establish an unambiguous allocation. | Read it as unknown allocation rather than zero stock. |

Shared rows summarize confirmed API group progress, separately for FiR and non-FiR groups.
The collection target credits other eligible items and their pending changes without subtracting the selected item's own stock twice.
Overlapping groups share one allocation of stock rather than claiming the same piece repeatedly.

For fixed rows, the display reserves confirmed stock for Shared progress first, then allocates the remainder to Found in Raid and Regular, in that order.
This display rule does not promise to reproduce every undocumented Market priority for overlapping goals.
Pending stock reaches requirement and Shared counters after confirmation; the Stock row can therefore update before those counters.

When a collection target is covered and no other requirement remains, Stock can show its available quantity as a complete ratio, such as `2/2` or `0/0`, while a Shared row retains its group ratio.
An active row can remain visible at `10/10`: enough collected items does not complete the associated quest or hideout upgrade.

### Read Next Item and goal details

**Next Item** shows the expected category on the right: **Regular**, **Found in Raid**, **Shared** or **Shared (FiR)**.
It comes from a local virtual **+1** calculation over the current API stock and quest/hideout requirements. Calculating it never adds an item or sends an inventory update.
The model uses API availability and checks modeled Shared progress against the current counters, but exact Market horizon depths and priority metadata are unavailable.
Treat the category as an estimate, not a guaranteed ordering or recognition of the item's in-game FiR status.

If the calculation does not identify one of these four categories, the header displays `—` and retains the available goal details.
When all known ratios are covered, no raw requirement gap remains and Add is ineligible, the category becomes blank.
The app does not display a guessed Regular category as a fallback.

Below the header, quest lists appear before hideout module lists.
The headings use **Quest** for one quest and **Quests** for several; **Hideout Module** for one module and **Hideout Modules** for several.
Each name is on the left, with its current ratio in parentheses at the right edge, for example **(0/5)**.
These are the current requirements for this item or its Shared group, not virtual +1 results or the total of every item needed by the whole quest.
A Shared ratio can include other eligible items. Unknown allocations display **(—/5)**.

The lists include the item's mandatory quest and hideout source records, including completed goals.
Canceled, optional and zero-amount sources do not contribute to these ratios.
A small brown checkmark marks a goal reported as completed; its full ratio, such as **(5/5)**, stays visible.
An active requirement that happens to be full does not acquire a completion checkmark.
Other unfinished goals retain their own counters when one goal is completed.

Small brown copy icons appear beside the item name in the header and beside each quest/hideout name.
Click an icon to copy **only that name**, without its ratio, group heading or completion mark.
In a completed goal row, the copy icon appears before the brown checkmark. The controls are recreated from the current names after refreshes.

When every associated target belongs to completed sources, the raw requirement is zero and Add is ineligible, the confirmed section becomes **Completed Item**.
The goal names, copy controls, completed checkmarks and full ratios remain, while the Stock summary, Regular/FiR/Shared rows, gray divider and Add/Remove buttons are hidden.
This is a presentation change: it does not deduct or overwrite stored stock, complete another goal or rewrite stock-action eligibility.
An active filled goal or a mixture of active and completed goals stays under **Next Item**, with its current Stock and stock controls.

New quest, hideout, group and inventory responses automatically rebuild these details and recalculate the category.
The app uses source IDs and returned goal data rather than a hardcoded list of item or quest names.

### Add or remove stock

Click **Add item** to reserve one additional item for the selected character, or **Remove item** to remove one available item.
Both actions change the same shared Market stock. They do not assign Regular/FiR quality, hand an item to a particular quest or mark a goal complete.
Add stops when no eligible active requirement can use another item. Remove requires a supported, unambiguous item with positive available stock; it does not require an unfinished goal.
Unsupported or temporarily unavailable actions are disabled. Completed Item presentation hides both buttons as described above.

### Set the stock shortcuts

By default, **F1** adds one item and **F2** removes one, using the same underlying stock checks as the buttons.
In **Settings**, focus a shortcut field and press the desired key or key combination. Use **Disable** to release that shortcut.
The two actions require different shortcuts; capturing a shortcut temporarily suspends stock actions.

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
Stock actions pause until refresh finishes. The last known counters remain with an error if refreshing fails; unavailable forecast details may be hidden.
Changing the item, item ID, character or mode clears unrelated forecast details rather than showing another profile's data.

Stock actions reserve their change immediately, then a background worker reads fresh stock and its revision before sending documented absolute updates in order.
Pending pieces participate in collection limits and cannot be removed below zero.
The scanner status and main Events log heading show the pending count, for example **(3 items waiting for API save)**.

A definite rejection removes the rejected pending change and shows the error. A network outage keeps pending work in memory.
If a write outcome is uncertain, the app checks Market through read-back before any retry; it does not blindly repeat a possibly successful update.
A successful acknowledgement remains valid if another client changes stock before the next read, and the app does not apply its saved change a second time.

Pending updates keep their order within each API-key, character and game-mode profile. An inaccessible profile remains pending while other profiles can continue saving.
Closing either window with unsaved work shows a warning. Confirming exit discards changes that have not reached Market.
Inventory, requirements and the pending queue are not written to disk; unsaved work is not restored or replayed after restart.

### Same-ID variants and separate FiR inventory

Variants sharing an item ID appear in separate tracker panels with their own API names, stock and requirement types.
Their confirmed stock contributes to the correct Shared groups according to their source records, including when another candidate is selected.
The documented stock endpoint accepts only the common item ID, so Add and Remove are disabled when that ID cannot identify the intended variant.
The app does not combine these inventories merely to make an update possible.

If the API reports separate FiR inventory, unsupported stock updates are also disabled instead of inventing undocumented quality fields.
Existing stock and supported requirement details can still be displayed. Ambiguous variants and unsupported inventory layouts can omit the Next Item forecast.

## Building

Build on Windows x64 with Go 1.25.x and Wails CLI v2.13.0:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
cd app
.\build.ps1
```

The frontend consists of static HTML, CSS and JavaScript. A Node/npm frontend build is not required.

The build prepares OCR dependencies from pinned official sources and verifies their SHA256 hashes. Every build creates the complete `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` package, containing `TarkovPilotPlus.exe`, the matching `ocr` folder and `versions.json`. It also creates `TarkovPilotPlus-<version>-windows-x64.zip` and its `.sha256` file. The current package uses version `1.3.0`. OCR does not download models or call an external OCR service at runtime.

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

Stock and quest/hideout progress are saved only on Tarkov Market. Character inventory, requirements, completion responses and additions awaiting a Market save are used in memory while the app runs. Local files contain the encrypted API key, scanner settings and item-price catalog cache. Previous local stock files and operation journals are not loaded or uploaded.

The copy controls write only the selected displayed name to the Windows clipboard when clicked. Copying a name does not submit an inventory or goal update.

## Updates and licences

Install Plus updates manually from this fork's [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases), keeping the new EXE and its matching `ocr` folder together. The original automatic EXE updater is disabled for this fork so an upstream release cannot replace the Plus app.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the bundled OCR components and licences. Keep the licence files in `ocr` when distributing the app.
