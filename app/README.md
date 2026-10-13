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
independent Plus version is in `plus/version.go` (`1.1.0`). The build script can
increment only the Plus version; it leaves the original version unchanged. It
builds the EXE and local OCR components and writes both versions to
`versions.json`. The upstream repository, commit and version are recorded in
`plus/upstream.json`. Package separation and upstream maintenance are documented
in [plus/README.md](plus/README.md).

Every build creates `build/bin/TarkovPilotPlus-<version>-windows-x64/`, containing
`TarkovPilotPlus.exe`, `versions.json` and the complete `ocr` directory. The build
also produces `build/bin/TarkovPilotPlus-<version>-windows-x64.zip` with the same
distribution files and its `.sha256` file. The current package version is `1.1.0`.

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
- `plus/item_tracking.go` — character selection by unique ID, separate Found in Raid and Regular Tracked Items requirements for the recognized item, completed quest and hideout sources reported by the API, and an explicit action that adds one item to the latest shared stock; revision conflicts trigger a refresh, uncertain writes trigger verification only, and variants with a shared BSG ID retain separate stock data while ambiguous additions are blocked
- `plus/item_hotkey.go` and `plus/tracked_hotkey*.go` — default F1 global Add shortcut, configurable F1-F12 or Disabled, with the same target checks and API operation as the retained Add button; a dedicated keyboard hook thread consumes matching key events and sends a bound request to the stock worker without waiting for network requests
- `plus/paired_windows.go` — paired startup and shutdown; each window runs in its own Wails process
- `plus/internal/market` — Tarkov Market API only, five-minute catalog cache, full Inspect title matching by edit distance and separate manual name search; private character and inventory data use a separate API rate limit and are not cached to disk
- `plus/internal/scanner` and `plus/ocr/name-scan.ps1` — Inspect window detection after a left click, followed by local English title OCR with the bundled Tesseract engine; OCR runs only for a detected Inspect window and can run again after another click
- `plus/ocr/prepare.ps1` — pinned OCR dependency downloads and SHA256 verification at build time
- `plus/frontend/dist/item.*` — compact English Item Scanner with a "How it works" section, Flea Market and Trader prices, per-slot values in parentheses, an Items Tracker panel shown only for active required items, stock progress with separate Found In Raid and Regular requirement rows, explicit additions and details of the recognized title

Automatic scanning checks for an Inspect window 200 milliseconds after a left
click; it does not continuously OCR the full screen. Manual scanning allows three
seconds to switch back to the game and reads an open Inspect window. The frontend
uses the `window.go.plus.ItemWindowApp` binding and receives `item-state` events.

Keep the `ocr` directory beside the built application. The API key is protected
with Windows DPAPI, and the Item Scanner stores its key and settings under
`%APPDATA%\TarkovPilotPlus`. Original Pilot settings remain under
`%APPDATA%\TarkovPilot`.
