# TarkovPilot+

TarkovPilot+ is a Windows companion app for Escape from Tarkov. It extends [TarkovPilot](https://github.com/ggdiam/TarkovPilot) with a separate item scanner window, market prices and character-specific Items Tracker information.

For the original map, screenshot and game-log features, see the [original TarkovPilot README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md). This README describes this fork and its own additions.

The current Plus version is **1.2.1**, based on **TarkovPilot 2.1.1**. Their source code and version numbers are maintained separately. Both app window title bars display **TarkovPilot+**, and the app interface is in English.

## Features

- **Item scanner window.** The item window opens with the main app. Closing either window closes both; unsaved Market updates trigger a warning before exit.
- **Local English name scanning.** The bundled Tesseract engine reads the title of an open item Inspect window. It does not use Windows OCR. Automatic scanning checks the foreground game window after a left-click; it does not continuously search the whole screen for item names.
- **Manual scanning and search.** Click **Scan now**, switch back to the game within 3 seconds and keep the Inspect window open. Alternatively, enter an English item name and click **Search**. Both actions remain available when **Automatic scanning** is off.
- **Tarkov Market prices.** Flea Market shows **Best price** and **24-hour average price**. Trader shows **Best price** and the trader's name. Each price includes its per-slot value in parentheses, and trader prices keep their own currency. Missing or unavailable prices display `—`.
- **Items Tracker.** Quest and hideout requirements, goal completion and stock come from Tarkov Market. Select a character to see **Found in Raid**, **Regular**, **Shared (FiR)** and **Shared** requirements. Requirement rows show the latest confirmed API progress. Shared progress includes other eligible items. The **Stock** target credits those items without subtracting the selected item's own stock twice: for example, water can progress from `0/4` to `4/4` while Operation Aquarius progresses from `1/3` to `3/3`. When the collection target is covered and no other requirement remains, Stock shows a complete ratio, such as `2/2` or `0/0`. Filled requirements remain visible until the associated goal is completed. Add stops when no eligible requirement needs another item. Found in Raid and Regular describe the API requirements; stock changes do not assign item quality.
- **Explicit stock updates.** **Add item** increases stock by one; **Remove item** decreases it by one while stock is above zero. Both update the displayed Stock immediately and save to Tarkov Market in the background. Pending changes count toward stock and collection limits. A definite rejection removes the rejected change and displays the error. Network outages keep changes pending in memory; uncertain writes are checked by reading Market before any retry. Character selection uses unique IDs, so characters with the same name remain separate. Choosing a character also selects its PVP, PVE or SEASON game mode.
- **Global stock shortcuts.** By default, press **F1** to add an item or **F2** to remove one without leaving the game. Both buttons remain available. In Settings, configure or disable each shortcut separately with a key or a Ctrl/Alt/Shift combination. Supported keys include F1-F12, letters, numbers, Space, Insert, Delete, Home, End, PageUp and PageDown. The two actions require different shortcuts. Each shortcut binds its request to the loaded item and character and changes stock at most once per physical press. Holding a key does not repeat the action. Further presses can reserve eligible changes while earlier saves run in the background. Enabled shortcuts are consumed globally for normal Windows keyboard input, even when their action is unavailable; other keys and modifier combinations keep their usual behavior. Applications using direct keyboard input or another higher-priority input handler may still receive the key.
- **Completion information.** While an active requirement remains, completed quest and hideout sources appear separately, for example **Quest completed — 4 items completed**. Optional completions are labelled. These values do not increase stock or active requirements.
- **Pending Market updates.** While background saves run, the scanner status and the main Events log heading include the unsaved item count in parentheses. Pending work exists only in memory. The app warns before closing with unsaved items; exiting discards updates that have not reached Market.
- **Protected API key.** The item scanner's API key is stored with Windows DPAPI encryption for the current Windows user. The field is masked by default; the eye icon explicitly shows or hides the saved key.
- **Current help and scan details.** **How it works** explains the controls. **Scan details** shows detected Inspect windows, recognized titles and the result. The catalog timestamp appears without a source label, in local `YYYY.MM.DD. HH:mm:ss` format, for example `2026.10.11. 00:30:23`.

Item names, prices, images, characters and tracking data come from the [Tarkov Market API](https://tarkov-market.com/dev/api). The catalog is cached locally for five minutes, and requests respect rate limits and server retry delays. Character and inventory responses are not written to the catalog cache. Tracked Items requires the appropriate API PRO access.

Completing a quest or hideout upgrade on Market removes its active requirements. Other unfinished goals keep their own requirements. Alternative candidates belong to the same **Shared requirement: N of M eligible items** supplied by Market.

After a confirmed load, Inspect changes and stock actions use the Market requirement graph in memory. Add and Remove reserve their stock change immediately, then a background worker checks fresh Market stock and its revision before sending each documented absolute update in order. The scanner status and main Events log heading track the pending item count. A network outage retains those in-memory changes; a definite rejection removes the rejected change. Refresh verifies uncertain writes without automatically repeating them. Inventory, progress and pending updates are not written to disk, and unsaved work is not restored after restart. The app does not keep item-quality annotations. **Refresh progress** reloads the current Market state.

A successful write acknowledgement remains valid when another client changes stock before the follow-up read. The app displays the later confirmed stock without applying its saved change again. Queued shortcut requests survive unrelated state updates while their item, character and settings remain eligible. Pending updates keep their order within each API-key, character and game-mode profile; an inaccessible profile stays visible as pending while other profiles can continue saving.

Tracker refreshes run in the background while the loaded panel and its counters remain visible. Stock actions pause until the refresh finishes. If the request fails, the last known counters stay visible with an error. Changing the item or profile clears unrelated data.

When the API reports separate FIR inventory, the app does not invent separate stock fields or send undocumented updates. Market stock updates are disabled in that case. Variants sharing the same item ID appear in separate Items Tracker panels, each with its own API name, stock, requirements and completion details. Panels with no active required quantity stay hidden. The documented update endpoint accepts only the shared item ID, so Add remains disabled for these variants until the API supports targeting them individually.

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

The build prepares OCR dependencies from pinned official sources and verifies their SHA256 hashes. Every build creates the complete `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` package, containing `TarkovPilotPlus.exe`, the matching `ocr` folder and `versions.json`. It also creates `TarkovPilotPlus-<version>-windows-x64.zip` and its `.sha256` file. The current package uses version `1.2.1`. OCR does not download models or call an external OCR service at runtime.

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

The Add item button and its default F1 shortcut accept only an eligible active Market requirement and increase the shared stock. Remove item and F2 decrease available stock even when no active requirement remains. Shared collection targets use the API's reported group progress and other eligible items, including their pending changes. Own stock is counted once. Active group progress is summarized in one Shared (FiR) row and one Shared row. Regular and Found in Raid use the source requirements and confirmed API stock; pending changes appear immediately in Stock and reach requirement rows after confirmation. Covered groups remain visible until their goals are completed or canceled. This display summary does not change Market stock.

Shared row tooltips show each group's quest, category and progress from the documented [Tarkov Market API](https://tarkov-market.com/dev/api) `groups` and `questGroup` sources.

## Updates and licences

Install Plus updates manually from this fork's [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases), keeping the new EXE and its matching `ocr` folder together. The original automatic EXE updater is disabled for this fork so an upstream release cannot replace the Plus app.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the bundled OCR components and licences. Keep the licence files in `ocr` when distributing the app.
