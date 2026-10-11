package plus

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
	"github.com/ggdiam/TarkovPilot/app/plus/internal/scanner"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

//go:embed ocr/name-scan.ps1
var nameScanScript []byte

//go:embed all:frontend/dist
var itemAssets embed.FS

const itemScanLanguage = "en"

type ItemOptions struct {
	Enabled      bool   `json:"enabled"`
	Automatic    bool   `json:"automatic"`
	Language     string `json:"language"`
	Mode         string `json:"mode"`
	CharacterUID string `json:"characterUid"`
	AddHotkey    string `json:"addHotkey"`
	revision     uint64
}

type ItemState struct {
	ItemOptions
	HasAPIKey         bool               `json:"hasAPIKey"`
	Busy              bool               `json:"busy"`
	Status            string             `json:"status"`
	Error             string             `json:"error"`
	OCRLanguages      []string           `json:"ocrLanguages"`
	CachedAt          string             `json:"cachedAt"`
	Stale             bool               `json:"stale"`
	Item              *market.Item       `json:"item"`
	ScanDetails       *ItemScanDetails   `json:"scanDetails"`
	Characters        []market.Character `json:"characters"`
	CharactersLoading bool               `json:"charactersLoading"`
	CharactersError   string             `json:"charactersError"`
	Tracking          *ItemTrackingState `json:"tracking"`
	AddHotkeyReady    bool               `json:"addHotkeyReady"`
	AddHotkeyError    string             `json:"addHotkeyError"`
}

type ItemScanDetails struct {
	InspectCount int      `json:"inspectCount"`
	Titles       []string `json:"titles"`
	Outcome      string   `json:"outcome"`
}

type itemNameScanner interface {
	Probe(context.Context, bool) (scanner.Result, error)
	Scan(context.Context, string, bool) (scanner.Result, error)
	Close() error
}

// ItemWindowApp a második ablak saját folyamatában fut. Az API-kulcs nincs
// a felület állapotában; csak a külön megjelenítési kérés adja vissza.
type ItemWindowApp struct {
	mu                  sync.Mutex
	scanMu              sync.Mutex
	trackingMu          sync.Mutex
	hotkeyConfigMu      sync.Mutex
	addHotkey           *trackedAddHotkey
	addHotkeyRequests   chan *trackedAddHotkeyTarget
	addHotkeyTarget     atomic.Pointer[trackedAddHotkeyTarget]
	addHotkeyPending    atomic.Bool
	trackingWriteCancel context.CancelFunc
	tracker             trackingAPI
	trackingRequests    chan struct{}
	charactersAt        time.Time
	charactersRevision  uint64
	trackedSnapshot     market.TrackedSnapshot
	trackedAt           time.Time
	trackedRevision     uint64
	state               ItemState
	apiKey              string
	dir                 string
	client              *market.Client
	ocr                 itemNameScanner
	wailsCtx            context.Context
	ctx                 context.Context
	cancel              context.CancelFunc
}

func NewItemWindowApp() (*ItemWindowApp, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "TarkovPilotPlus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	a := &ItemWindowApp{dir: dir, ocr: scanner.New(nameScanScript)}
	a.state.ItemOptions = ItemOptions{Enabled: true, Automatic: true, Language: itemScanLanguage, Mode: "pvp", AddHotkey: defaultTrackedAddHotkey}
	a.state.OCRLanguages = []string{itemScanLanguage}
	a.state.Status = "Enter your Tarkov Market API key in settings."
	if data, err := os.ReadFile(filepath.Join(dir, "item-settings.json")); err == nil {
		var saved ItemOptions
		if json.Unmarshal(data, &saved) == nil {
			a.state.ItemOptions = normalizeSavedItemOptions(saved)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "api-key.bin")); err == nil {
		if key, err := unprotectAPIKey(data); err == nil {
			a.apiKey = string(key)
		} else {
			a.state.Error = "The saved API key could not be read. Enter it again on this Windows account."
		}
	}
	a.state.HasAPIKey = a.apiKey != ""
	if a.state.HasAPIKey {
		if a.state.Automatic {
			a.state.Status = "Waiting for an item Inspect window."
		} else {
			a.state.Status = "Automatic scanning is off."
		}
	}
	a.client = market.NewClient(filepath.Join(dir, "item-cache"))
	a.tracker = a.client
	a.trackingRequests = make(chan struct{}, 1)
	return a, nil
}

func normalizeItemOptions(o ItemOptions) ItemOptions {
	// A kompatibilis language mező megmarad, de a tárgyjelző mindig angol.
	o.Language = itemScanLanguage
	o.AddHotkey = normalizeTrackedAddHotkey(o.AddHotkey)
	o.Mode = strings.ToLower(o.Mode)
	o.CharacterUID = strings.TrimSpace(o.CharacterUID)
	if len(o.CharacterUID) > 128 || strings.ContainsAny(o.CharacterUID, "/\\\r\n\x00") {
		o.CharacterUID = ""
	}
	if o.Mode != "pve" && o.Mode != "season" {
		o.Mode = "pvp"
	}
	return o
}

func itemOCRLanguages(languages []string) []string {
	for _, language := range languages {
		if language == itemScanLanguage {
			return []string{itemScanLanguage}
		}
	}
	return nil
}

func normalizeSavedItemOptions(o ItemOptions) ItemOptions {
	o = normalizeItemOptions(o)
	// A korábbi két kapcsoló együttes állapota határozza meg az automatikát.
	// A kézi beolvasás az új, egyetlen kapcsolótól függetlenül elérhető marad.
	o.Automatic = o.Enabled && o.Automatic
	o.Enabled = true
	return o
}

func (a *ItemWindowApp) startup(ctx context.Context, parent windows.Handle) {
	a.wailsCtx = ctx
	a.ctx, a.cancel = context.WithCancel(context.Background())
	go func() {
		defer windows.CloseHandle(parent)
		_, _ = windows.WaitForSingleObject(parent, windows.INFINITE)
		a.Quit()
	}()
	go a.watchInspectClicks()
	go a.runTracking()
	a.startTrackedAddHotkey()
	a.requestTracking()
}

func (a *ItemWindowApp) shutdown(context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
	a.stopTrackedAddHotkey()
	_ = a.ocr.Close()
}

func (a *ItemWindowApp) GetState() ItemState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.state
	st.Language = itemScanLanguage
	st.OCRLanguages = itemOCRLanguages(a.state.OCRLanguages)
	st.Characters = cloneCharacters(a.state.Characters)
	st.Tracking = cloneTrackingState(st.Tracking)
	if st.Item != nil {
		item := *st.Item
		st.Item = &item
	}
	if st.ScanDetails != nil {
		details := *st.ScanDetails
		details.Titles = append([]string(nil), details.Titles...)
		st.ScanDetails = &details
	}
	return st
}

func (a *ItemWindowApp) emit() {
	a.publishTrackedAddHotkey()
	if a.wailsCtx != nil {
		wailsRuntime.EventsEmit(a.wailsCtx, "item-state", a.GetState())
	}
}

func (a *ItemWindowApp) RevealAPIKey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.apiKey
}

func (a *ItemWindowApp) SetOptions(options ItemOptions) ItemState {
	options = normalizeItemOptions(options)
	a.mu.Lock()
	// A kiválasztott karakter egyedi azonosítója határozza meg a játékmódot.
	// Kézi módváltáskor a korábbi, más módban lévő karaktert elengedjük.
	for _, character := range a.state.Characters {
		if character.CharUID != options.CharacterUID {
			continue
		}
		mode := strings.ToLower(character.GameMode)
		if mode != "pvp" && mode != "pve" && mode != "season" {
			break
		}
		if options.CharacterUID != a.state.CharacterUID {
			options.Mode = mode
		} else if options.Mode != mode {
			options.CharacterUID = ""
		}
		break
	}
	data, _ := json.MarshalIndent(options, "", "    ")
	options.revision = a.state.ItemOptions.revision + 1
	a.cancelTrackingWriteLocked()
	changed := a.state.Mode != options.Mode || a.state.Language != options.Language
	hotkeyChanged := a.state.AddHotkey != options.AddHotkey
	a.state.ItemOptions = options
	if hotkeyChanged {
		a.state.AddHotkeyReady = false
		a.state.AddHotkeyError = ""
	}
	a.state.Tracking = nil
	a.state.CharactersError = ""
	a.state.Error = ""
	if changed {
		a.state.Item = nil
		a.state.ScanDetails = nil
		a.state.CachedAt = ""
		a.state.Stale = false
	}
	if err := writePrivateFile(filepath.Join(a.dir, "item-settings.json"), data); err != nil {
		a.state.Error = "Settings could not be saved."
	}
	if !options.Enabled {
		a.state.Status = "Name scanning is off."
	} else if !options.Automatic {
		a.state.Status = "Automatic scanning is off."
	} else {
		a.state.Status = "Waiting for an item Inspect window."
	}
	a.mu.Unlock()
	a.configureTrackedAddHotkey()
	a.emit()
	a.requestTracking()
	return a.GetState()
}

func (a *ItemWindowApp) SaveAPIKey(key string) ItemState {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		a.setError("Enter a valid API key.")
		return a.GetState()
	}
	sealed, err := protectAPIKey([]byte(key))
	if err == nil {
		err = writePrivateFile(filepath.Join(a.dir, "api-key.bin"), sealed)
	}
	if err != nil {
		a.setError("The API key could not be encrypted and saved.")
		return a.GetState()
	}
	a.mu.Lock()
	a.apiKey = key
	a.state.ItemOptions.revision++
	a.state.HasAPIKey = true
	a.clearTrackingLocked()
	a.state.Item = nil
	a.state.ScanDetails = nil
	a.state.CachedAt = ""
	a.state.Stale = false
	a.state.Error = ""
	a.state.Status = "API key saved. Loading the item catalog…"
	a.mu.Unlock()
	a.emit()
	go a.refreshCatalog()
	a.requestTracking()
	return a.GetState()
}

func (a *ItemWindowApp) ClearAPIKey() ItemState {
	if err := os.Remove(filepath.Join(a.dir, "api-key.bin")); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.setError("The API key could not be cleared.")
		return a.GetState()
	}
	a.mu.Lock()
	a.apiKey = ""
	a.state.ItemOptions.revision++
	a.state.HasAPIKey = false
	a.clearTrackingLocked()
	a.state.Item = nil
	a.state.ScanDetails = nil
	a.state.CachedAt = ""
	a.state.Stale = false
	a.state.Error = ""
	a.state.Status = "API key cleared."
	a.mu.Unlock()
	a.emit()
	return a.GetState()
}

func (a *ItemWindowApp) setError(message string) {
	a.mu.Lock()
	a.state.Error = message
	a.state.Busy = false
	a.mu.Unlock()
	a.emit()
}

func (a *ItemWindowApp) updateRevision(revision uint64, update func(*ItemState)) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.ItemOptions.revision != revision {
		return false
	}
	update(&a.state)
	return true
}

func (a *ItemWindowApp) errorRevision(revision uint64, message string) {
	if a.updateRevision(revision, func(st *ItemState) { st.Error = message }) {
		a.emit()
	}
}

func (a *ItemWindowApp) catalog() (market.Catalog, ItemOptions, error) {
	a.mu.Lock()
	key, opts := a.apiKey, a.state.ItemOptions
	a.mu.Unlock()
	opts.Language = itemScanLanguage
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	catalog, err := a.client.Load(ctx, key, opts.Mode, opts.Language)
	a.mu.Lock()
	if a.state.ItemOptions.revision == opts.revision && !catalog.UpdatedAt.IsZero() {
		a.state.CachedAt = catalog.UpdatedAt.Format(time.RFC3339)
		a.state.Stale = catalog.Stale
	}
	a.mu.Unlock()
	return catalog, opts, err
}

func marketError(err error) string {
	switch {
	case errors.Is(err, market.ErrAPIKeyRequired):
		return "Enter your Tarkov Market API key in settings."
	case errors.Is(err, market.ErrUnauthorized):
		return "Tarkov Market rejected the API key. Check your key and PRO access."
	case errors.Is(err, market.ErrRateLimited):
		return "The Tarkov Market request limit was reached. The app will retry after the waiting period."
	default:
		return "The Tarkov Market item catalog could not be loaded right now."
	}
}

func (a *ItemWindowApp) refreshCatalog() {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.ctx.Err() != nil || !a.GetState().HasAPIKey {
		return
	}
	_, opts, err := a.catalog()
	if err != nil {
		a.errorRevision(opts.revision, marketError(err))
		return
	}
	a.updateRevision(opts.revision, func(st *ItemState) {
		if st.Enabled && st.Automatic {
			st.Status = "Waiting for an item Inspect window."
		} else if st.Enabled {
			st.Status = "Automatic scanning is off."
		}
		st.Error = ""
	})
	a.emit()
}

func (a *ItemWindowApp) ScanNow() ItemState {
	if !a.scanMu.TryLock() {
		return a.GetState()
	}
	a.mu.Lock()
	if a.ctx == nil || a.ctx.Err() != nil || !a.state.Enabled || !a.state.HasAPIKey {
		a.mu.Unlock()
		a.scanMu.Unlock()
		return a.GetState()
	}
	a.state.Busy = true
	a.state.Error = ""
	a.state.Status = "Switch back to the game; scanning starts in 3 seconds…"
	a.mu.Unlock()
	a.emit()
	go func() {
		defer a.scanMu.Unlock()
		a.scanLocked(false, 3*time.Second)
	}()
	return a.GetState()
}

func (a *ItemWindowApp) scan(automatic bool, delay time.Duration) {
	if !a.scanMu.TryLock() {
		return
	}
	defer a.scanMu.Unlock()
	a.scanLocked(automatic, delay)
}

func (a *ItemWindowApp) scanLocked(automatic bool, delay time.Duration) {
	st := a.GetState()
	if !st.Enabled || !st.HasAPIKey || (automatic && !st.Automatic) {
		if !automatic {
			// ScanNow publishes Busy before scheduling this operation. Options
			// may disable scanning before the goroutine gets to this check.
			a.mu.Lock()
			a.state.Busy = false
			a.mu.Unlock()
			a.emit()
		}
		return
	}
	if !automatic {
		a.updateRevision(st.ItemOptions.revision, func(current *ItemState) {
			current.Busy = true
			if delay > 0 {
				current.Status = "Switch back to the game; scanning starts in 3 seconds…"
			}
		})
		a.emit()
	}
	defer func() {
		// scanMu still belongs to this operation even if options changed.
		// Release its busy state so a new revision cannot remain stuck.
		a.mu.Lock()
		a.state.Busy = false
		a.mu.Unlock()
		a.emit()
	}()
	if delay > 0 {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	// A click first checks the Inspect marker using pixels only. Every verified
	// Inspect is read again; an absent Inspect never starts OCR or a catalogue request.
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	probe, err := a.ocr.Probe(ctx, automatic)
	cancel()
	if err != nil {
		a.errorRevision(st.ItemOptions.revision, "The local Inspect window checker could not start. Check the bundled OCR files.")
		return
	}
	if a.scanWaiting(probe, st.ItemOptions) {
		return
	}
	if a.ctx.Err() != nil || a.GetState().ItemOptions.revision != st.ItemOptions.revision {
		return
	}
	a.updateRevision(st.ItemOptions.revision, func(current *ItemState) {
		current.Busy = true
		current.Status = "Inspect title scanning…"
	})
	a.emit()
	// The helper rechecks the marker and foreground window just before title OCR.
	// Pixels and screen text stay in memory and are never sent to the API.
	ctx, cancel = context.WithTimeout(a.ctx, 15*time.Second)
	result, err := a.ocr.Scan(ctx, itemScanLanguage, automatic)
	cancel()
	if err != nil {
		a.errorRevision(st.ItemOptions.revision, "The local name scanner could not start. Check the bundled OCR files.")
		return
	}
	if a.ctx.Err() != nil || a.GetState().ItemOptions.revision != st.ItemOptions.revision {
		return
	}
	a.mu.Lock()
	a.state.OCRLanguages = itemOCRLanguages(result.Languages)
	a.mu.Unlock()
	if result.Error != "" {
		a.errorRevision(st.ItemOptions.revision, result.Error)
		return
	}
	if a.scanWaiting(result, st.ItemOptions) {
		return
	}
	a.setScanDetails(result, st.ItemOptions, "Checking the recognized item name…")
	if len(scanTitleTexts(result)) == 0 {
		outcome := "The Inspect window is open, but its title could not be read."
		a.setScanDetails(result, st.ItemOptions, outcome)
		a.updateRevision(st.ItemOptions.revision, func(current *ItemState) { current.Status = outcome })
		return
	}
	catalog, opts, err := a.catalog()
	if opts.revision != st.ItemOptions.revision {
		return
	}
	if err != nil && len(catalog.Items) == 0 {
		a.errorRevision(opts.revision, marketError(err))
		return
	}
	item, found := matchScan(result, catalog.Items)
	outcome := "No matching item was found in the catalog for the recognized title."
	if found {
		outcome = "Item recognized: " + item.Name
	}
	a.setScanDetails(result, opts, outcome)
	a.acceptItem(item, found, opts, err)
	if !found {
		a.updateRevision(opts.revision, func(current *ItemState) { current.Status = outcome })
	}
}

func (a *ItemWindowApp) setScanDetails(result scanner.Result, opts ItemOptions, outcome string) {
	count := result.InspectCount
	if count == 0 {
		count = len(result.TitleVariants)
	}
	if count == 0 && result.InspectOpen {
		count = max(1, (len(result.Groups)+1)/2)
	}
	titles := scanTitleTexts(result)
	a.updateRevision(opts.revision, func(current *ItemState) {
		current.ScanDetails = &ItemScanDetails{InspectCount: count, Titles: titles, Outcome: outcome}
	})
}

func scanTitleTexts(result scanner.Result) []string {
	titles := make([]string, 0)
	seen := make(map[string]bool)
	for _, variants := range result.TitleVariants {
		for _, title := range variants {
			title = strings.TrimSpace(title)
			if title != "" && !seen[title] {
				seen[title] = true
				titles = append(titles, title)
			}
		}
	}
	for _, group := range result.Groups {
		title := strings.TrimSpace(strings.Join(group, " "))
		if title != "" && !seen[title] {
			seen[title] = true
			titles = append(titles, title)
		}
	}
	if len(titles) == 0 {
		for _, title := range result.Lines {
			title = strings.TrimSpace(title)
			if title != "" && !seen[title] {
				seen[title] = true
				titles = append(titles, title)
			}
		}
	}
	return titles
}

func (a *ItemWindowApp) scanWaiting(result scanner.Result, opts ItemOptions) bool {
	if result.Error != "" {
		a.errorRevision(opts.revision, result.Error)
		return true
	}
	if result.Active && result.InspectOpen {
		return false
	}
	a.updateRevision(opts.revision, func(current *ItemState) {
		current.Busy = false
		current.ScanDetails = nil
		current.Status = "Waiting for an item Inspect window."
		if !result.Active {
			current.Status = "Bring the Escape from Tarkov window to the foreground to scan."
		}
	})
	return true
}

func (a *ItemWindowApp) acceptMatch(lines []string, catalog market.Catalog, opts ItemOptions, loadErr error) {
	item, found := market.Match(lines, catalog.Items)
	a.acceptItem(item, found, opts, loadErr)
}

// Each verified Inspect is matched independently to the nearest full item name.
// OCR variants describe the same title. Multiple Inspect windows are processed
// in detection order; the last recognized window supplies the single item card.
func matchScan(result scanner.Result, items []market.Item) (market.Item, bool) {
	if !result.InspectOpen {
		return market.Item{}, false
	}
	windows := result.TitleVariants
	if len(windows) == 0 && len(result.Groups) > 0 {
		for i := 0; i < len(result.Groups); i += 2 {
			variants := []string{strings.Join(result.Groups[i], "\n")}
			if i+1 < len(result.Groups) {
				variants = append(variants, strings.Join(result.Groups[i+1], "\n"))
			}
			windows = append(windows, variants)
		}
	}
	if len(windows) == 0 {
		item, _, found := market.MatchInspection(result.Lines, items)
		return item, found
	}
	var selected market.Item
	found := false
	for _, variants := range windows {
		best := -1.0
		for _, title := range variants {
			if item, score, ok := market.MatchInspection([]string{title}, items); ok && score > best {
				selected, best, found = item, score, true
			}
		}
	}
	return selected, found
}

func (a *ItemWindowApp) acceptItem(item market.Item, found bool, opts ItemOptions, loadErr error) {
	a.mu.Lock()
	defer func() {
		a.mu.Unlock()
		if found {
			a.requestTracking()
		}
	}()
	// Discard work if the key, mode or recognition language changed meanwhile.
	if !a.state.HasAPIKey || a.state.ItemOptions.revision != opts.revision {
		return
	}
	a.state.Error = ""
	if loadErr != nil {
		a.state.Error = marketError(loadErr)
	}
	if found {
		if a.state.Item == nil || a.state.Item.UID != item.UID {
			a.cancelTrackingWriteLocked()
			a.state.Tracking = nil
		} else if item.BSGID == "" {
			// A régi katalógus még nem tartalmazza a feloldott BSG-azonosítót.
			item.BSGID = a.state.Item.BSGID
		}
		a.state.Item = &item
		a.state.Status = "Item recognized."
	} else {
		a.state.Status = "No unambiguous item name was found. Open its Inspect window or search by name."
	}
}

func (a *ItemWindowApp) SearchItem(name string) (result ItemState) {
	if strings.TrimSpace(name) == "" || len(name) > 512 {
		return a.GetState()
	}
	if !a.scanMu.TryLock() {
		return a.GetState()
	}
	defer a.scanMu.Unlock()
	a.mu.Lock()
	a.state.Busy = true
	a.state.Error = ""
	a.state.Status = "Searching for item…"
	a.mu.Unlock()
	a.emit()
	defer func() {
		a.mu.Lock()
		a.state.Busy = false
		a.mu.Unlock()
		a.emit()
		result = a.GetState()
	}()
	catalog, opts, err := a.catalog()
	if err != nil && len(catalog.Items) == 0 {
		a.errorRevision(opts.revision, marketError(err))
	} else {
		a.acceptMatch([]string{name}, catalog, opts, err)
		a.emit()
	}
	return a.GetState()
}

func (a *ItemWindowApp) OpenItemPage() {
	st := a.GetState()
	url := "https://tarkov-market.com"
	if st.Item != nil {
		if safe := market.SafeItemURL(st.Item.Link); safe != "" {
			url = safe
		}
	}
	wailsRuntime.BrowserOpenURL(a.wailsCtx, url)
}

func (a *ItemWindowApp) Quit() {
	if a.wailsCtx != nil {
		wailsRuntime.Quit(a.wailsCtx)
	}
}

type itemAssetsFS struct{ fs.FS }

func (f itemAssetsFS) Open(name string) (fs.File, error) {
	if name == "index.html" {
		name = "item.html"
	}
	return f.FS.Open(name)
}

func RunItemWindow(parentPID int, hidden bool) error {
	if parentPID <= 0 {
		return errors.New("missing parent process")
	}
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if err != nil {
		return err
	}
	a, err := NewItemWindowApp()
	if err != nil {
		windows.CloseHandle(parent)
		return err
	}
	frontend, err := fs.Sub(itemAssets, "frontend/dist")
	if err != nil {
		windows.CloseHandle(parent)
		return err
	}
	return wails.Run(&options.App{
		Title: WindowTitle, Width: 350, Height: 590,
		MinWidth: 300, MinHeight: 350, Frameless: true, AlwaysOnTop: true,
		StartHidden: hidden, HideWindowOnClose: false,
		BackgroundColour: &options.RGBA{R: 18, G: 18, B: 18, A: 1},
		AssetServer:      &assetserver.Options{Assets: itemAssetsFS{frontend}},
		OnStartup:        func(ctx context.Context) { a.startup(ctx, parent) },
		OnShutdown:       a.shutdown, Bind: []interface{}{a},
	})
}

func writePrivateFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func protectAPIKey(data []byte) ([]byte, error)   { return cryptAPIKey(data, true) }
func unprotectAPIKey(data []byte) ([]byte, error) { return cryptAPIKey(data, false) }

func cryptAPIKey(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty key")
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
