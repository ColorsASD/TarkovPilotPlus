# TarkovPilot+

TarkovPilot+ is a Windows companion app for Escape from Tarkov. It extends [TarkovPilot](https://github.com/ggdiam/TarkovPilot) with a separate item scanner window, market prices and character-specific Items Tracker information.

For the original map, screenshot and game-log features, see the [original TarkovPilot README](https://github.com/ggdiam/TarkovPilot/blob/master/README.md). This README describes this fork and its own additions.

The current Plus version is **1.2.0**, based on **TarkovPilot 2.1.1**. Their source code and version numbers are maintained separately. Both app window title bars display **TarkovPilot+**, and the app interface is in English.

## Features

- **Item scanner window.** The item window opens with the main app. Closing either window closes both; unsaved Market updates trigger a warning before exit.
- **Local English name scanning.** The bundled Tesseract engine reads the title of an open item Inspect window. It does not use Windows OCR. Automatic scanning checks the foreground game window after a left-click; it does not continuously search the whole screen for item names.
- **Manual scanning and search.** Click **Scan now**, switch back to the game within 3 seconds and keep the Inspect window open. Alternatively, enter an English item name and click **Search**. Both actions remain available when **Automatic scanning** is off.
- **Tarkov Market prices.** Flea Market shows **Best price** and **24-hour average price**. Trader shows **Best price** and the trader's name. Each price includes its per-slot value in parentheses, and trader prices keep their own currency. Missing or unavailable prices display `—`.
- **Items Tracker.** Quest and hideout requirements, goal completion and stock come from Tarkov Market. Select a character to see shared stock and separate **Found in Raid**, **Regular**, **Any (FiR)** and **Any** requirements. The **Stock** row always shows available stock / required quantity, such as `2/5`. Once all Any groups are covered and no other collection requirement remains, Stock shows the item's available quantity as a complete ratio, such as `2/2` or `0/0`, while Any remains `8/8`. Filled requirements remain visible, such as `10/10`, until the associated goal is completed. Add buttons and shortcuts stop adding when no eligible requirement needs another item; the Market requirement determines which item quality can be added.
- **Explicit stock updates.** **Add Found in Raid item** and **Add Regular item** increase the displayed stock immediately and save the change to Tarkov Market in the background. Pending additions count toward the collection limit. A definite rejection restores the confirmed Market stock and displays the error. Network outages keep additions pending in memory; uncertain writes are checked by reading Market before any retry. Character selection uses unique IDs, so characters with the same name remain separate. Choosing a character also selects its PVP, PVE or SEASON game mode.
- **Global Add shortcuts.** By default, press **F1** for Found in Raid or **F2** for Regular without leaving the game. The Add button remains available. In Settings, configure each shortcut independently with a key or a Ctrl/Alt/Shift combination, or choose Disabled. Supported keys include F1-F12, letters, numbers, Space, Insert, Delete, Home, End, PageUp and PageDown. Each shortcut works only for a loaded item whose Add action is available, binds the request to that item and character, and adds at most one item per physical press. Holding the key does not repeat Add. Further physical presses can reserve eligible pieces while earlier Market saves run in the background. Enabled shortcuts are consumed globally for normal Windows keyboard input, even when no item can be added; other keys and modifier combinations keep their usual behavior. Applications using direct keyboard input or another higher-priority input handler may still receive the keys.
- **Completion information.** While an active requirement remains, completed quest and hideout sources appear separately, for example **Quest completed — 4 items completed**. Optional completions are labelled. These values do not increase stock or active requirements.
- **Pending Market updates.** While background saves run, the scanner status and the main Events log heading include the unsaved item count in parentheses. Pending work exists only in memory. The app warns before closing with unsaved items; exiting discards updates that have not reached Market.
- **Protected API key.** The item scanner's API key is stored with Windows DPAPI encryption for the current Windows user. The field is masked by default; the eye icon explicitly shows or hides the saved key.
- **Current help and scan details.** **How it works** explains the controls. **Scan details** shows detected Inspect windows, recognized titles and the result. The catalog timestamp appears without a source label, in local `YYYY.MM.DD. HH:mm:ss` format, for example `2026.10.11. 00:30:23`.

Item names, prices, images, characters and tracking data come from the [Tarkov Market API](https://tarkov-market.com/dev/api). The catalog is cached locally for five minutes, and requests respect rate limits and server retry delays. Character and inventory responses are not written to the catalog cache. Tracked Items requires the appropriate API PRO access.

Completing a quest or hideout upgrade on Market removes its active requirements. Other unfinished goals keep their own requirements. Alternative candidates belong to the same **Any N of M eligible items** requirement supplied by Market.

After a confirmed load, Inspect changes and Add use the Market requirement graph in memory. Add reserves a suitable piece immediately so the same deficit cannot be filled twice, then a background worker checks fresh Market stock and its revision before sending the documented absolute update. The scanner status and main Events log heading track the pending item count. A network outage retains those in-memory reservations; a definite rejection removes the rejected reservation. Refresh verifies uncertain writes without automatically repeating them. Inventory, progress, item quality annotations and pending updates are not written to disk, and unsaved work is not restored after restart. **Refresh progress** reloads the current Market state.

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

The build prepares OCR dependencies from pinned official sources and verifies their SHA256 hashes. Every build creates the complete `app/build/bin/TarkovPilotPlus-<version>-windows-x64/` package, containing `TarkovPilotPlus.exe`, the matching `ocr` folder and `versions.json`. It also creates `TarkovPilotPlus-<version>-windows-x64.zip` and its `.sha256` file. The current package uses version `1.2.0`. OCR does not download models or call an external OCR service at runtime.

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

Add buttons and shortcuts accept only an eligible active Market requirement. Their default shortcuts are F2 for Regular and F1 for Found in Raid. Mandatory requirements reserve suitable stock before shared Any groups, so a piece covers one requirement at a time. Active Any group progress is summarized in one Any (FiR) row and one Any row. Covered groups remain visible until their goals are completed or canceled; covered alternatives stop requesting more items. This display summary does not change Market stock.

Any row tooltips show each group's quest, category and progress from the documented [Tarkov Market API](https://tarkov-market.com/dev/api) `groups` and `questGroup` sources.

## Updates and licences

Install Plus updates manually from this fork's [releases](https://github.com/ColorsASD/TarkovPilotPlus/releases), keeping the new EXE and its matching `ocr` folder together. The original automatic EXE updater is disabled for this fork so an upstream release cannot replace the Plus app.

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the bundled OCR components and licences. Keep the licence files in `ocr` when distributing the app.
