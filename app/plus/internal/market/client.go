// Package market a Tarkov Market API helyi katalógusát és a tárgynevek párosítását kezeli.
package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiURL           = "https://api.tarkov-market.app/api/v1/items/all"
	cacheTTL         = 5 * time.Minute
	requestSpacing   = 12 * time.Second
	maxResponseBytes = 32 << 20
)

var (
	ErrAPIKeyRequired = errors.New("A Tarkov Market API key is required")
	ErrUnauthorized   = errors.New("The Tarkov Market API key is invalid or access is denied")
	ErrRateLimited    = errors.New("The Tarkov Market API request limit was reached")
	ErrInvalidCatalog = errors.New("The Tarkov Market catalog is invalid")
	ErrResponseTooBig = errors.New("The Tarkov Market response is too large")
)

// Item a tárgyjelzőhöz szükséges, dokumentált API-mezőket tartalmazza.
type Item struct {
	UID            string  `json:"uid"`
	BSGID          string  `json:"bsgId"`
	Name           string  `json:"name"`
	ShortName      string  `json:"shortName"`
	Price          float64 `json:"price"`
	Avg24hPrice    float64 `json:"avg24hPrice"`
	TraderName     string  `json:"traderName"`
	TraderPrice    float64 `json:"traderPrice"`
	TraderPriceCur string  `json:"traderPriceCur"`
	TraderPriceRub float64 `json:"traderPriceRub"`
	Slots          float64 `json:"slots"`
	Img            string  `json:"img"`
	Icon           string  `json:"icon"`
	Link           string  `json:"link"`
	HaveMarketData bool    `json:"haveMarketData"`
	BannedOnFlea   bool    `json:"bannedOnFlea"`
	Updated        string  `json:"updated"`
}

type Catalog struct {
	Items     []Item    `json:"items"`
	UpdatedAt time.Time `json:"updatedAt"`
	Stale     bool      `json:"stale"`
}

type diskCatalog struct {
	Version   int       `json:"version"`
	Mode      string    `json:"mode"`
	Lang      string    `json:"lang"`
	Items     []Item    `json:"items"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// A kapu minden kliens teljeslista-kérésére közös. A token a teljes HTTP-kérést
// sorosítja; a várakozás az alkalmazás leállításakor megszakítható.
type requestGate struct {
	token    chan struct{}
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
	failures int
}

func newRequestGate(interval time.Duration) *requestGate {
	return &requestGate{token: make(chan struct{}, 1), interval: interval}
}

var fullListRequests = newRequestGate(requestSpacing)

// Az egyedi tárgykérések és a teljes lista közös 300/perc keretből fogyasztanak.
var priceRequests = newRequestGate(time.Minute / 300)

func (g *requestGate) enter(ctx context.Context) error {
	select {
	case g.token <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	g.mu.Lock()
	wait := time.Until(g.next)
	g.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			<-g.token
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		<-g.token
		return err
	}
	g.mu.Lock()
	g.next = time.Now().Add(g.interval)
	g.mu.Unlock()
	return nil
}

func (g *requestGate) leave() { <-g.token }

func (g *requestGate) success() {
	g.mu.Lock()
	g.failures = 0
	g.mu.Unlock()
}

func (g *requestGate) backoff(delay time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if delay <= 0 {
		g.failures++
		delay = 15 * time.Second
		for i := 1; i < g.failures && delay < 5*time.Minute; i++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
	until := time.Now().Add(delay)
	if until.After(g.next) {
		g.next = until
	}
}

type Client struct {
	cacheDir          string
	http              *http.Client
	gate              *requestGate
	priceGate         *requestGate
	progressGate      *requestGate
	progressWriteGate *requestGate
	now               func() time.Time
	mu                sync.Mutex
	loadMu            sync.Mutex
	catalogs          map[string]Catalog
}

func NewClient(cacheDir string) *Client {
	return &Client{
		cacheDir: cacheDir,
		http: &http.Client{
			Timeout: 15 * time.Second,
			// A kulcs átirányításkor még azonos hosztra sem kerül továbbításra.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		gate:              fullListRequests,
		priceGate:         priceRequests,
		progressGate:      progressRequests,
		progressWriteGate: progressWrites,
		now:               time.Now,
		catalogs:          make(map[string]Catalog),
	}
}

// Load friss helyi adatot ad, vagy lekéri a teljes katalógust. Sikertelen
// frissítéskor a régi katalógust Stale=true értékkel és a hibával együtt adja vissza.
// A kulcs sem a gyorsítótárba, sem hibaszövegbe nem kerül bele.
func (c *Client) Load(ctx context.Context, apiKey, mode, lang string) (Catalog, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return Catalog{}, ErrAPIKeyRequired
	}
	mode, lang, err := parameters(mode, lang)
	if err != nil {
		return Catalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	key := mode + "-" + lang
	c.mu.Lock()
	cached, ok := c.catalogs[key]
	if !ok {
		cached, ok = c.readCache(mode, lang)
		if ok {
			c.catalogs[key] = cached
		}
	}
	c.mu.Unlock()
	if ok {
		age := c.now().Sub(cached.UpdatedAt)
		if age >= 0 && age < cacheTTL {
			cached.Stale = false
			return cloneCatalog(cached), nil
		}
	}
	items, err := c.fetch(ctx, apiKey, mode, lang)
	if err != nil {
		cached.Stale = true
		return cloneCatalog(cached), err
	}
	result := Catalog{Items: items, UpdatedAt: c.now()}
	c.mu.Lock()
	c.catalogs[key] = result
	c.mu.Unlock()
	// A lemezcache hibája nem teszi használhatatlanná a sikeres API-választ.
	_ = c.writeCache(mode, lang, result)
	return cloneCatalog(result), nil
}

// CachedCatalog kizárólag helyi adatot olvas. Egy lejárt katalógus azonnal
// használható névpárosításra, amíg a friss árak a háttérben érkeznek.
func (c *Client) CachedCatalog(mode, lang string) (Catalog, bool) {
	mode, lang, err := parameters(mode, lang)
	if err != nil {
		return Catalog{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := mode + "-" + lang
	cached, ok := c.catalogs[key]
	if !ok {
		cached, ok = c.readCache(mode, lang)
		if ok {
			c.catalogs[key] = cached
		}
	}
	if !ok {
		return Catalog{}, false
	}
	age := c.now().Sub(cached.UpdatedAt)
	cached.Stale = age < 0 || age >= cacheTTL
	return cloneCatalog(cached), true
}

func cloneCatalog(catalog Catalog) Catalog {
	catalog.Items = append([]Item(nil), catalog.Items...)
	return catalog
}

func parameters(mode, lang string) (string, string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "pvp"
	}
	if mode != "pvp" && mode != "pve" && mode != "season" {
		return "", "", errors.New("Unsupported Tarkov Market game mode")
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = "en"
	}
	switch lang {
	case "en", "ru", "ch", "cz", "de", "es", "fr", "hu", "it", "jp", "kr", "mx", "pl", "po", "ro", "sk", "tr":
		return mode, lang, nil
	default:
		return "", "", errors.New("Unsupported Tarkov Market language")
	}
}

func (c *Client) fetch(ctx context.Context, apiKey, mode, lang string) ([]Item, error) {
	if err := c.gate.enter(ctx); err != nil {
		return nil, err
	}
	defer c.gate.leave()
	if err := c.priceGate.enter(ctx); err != nil {
		return nil, err
	}
	defer c.priceGate.leave()
	endpoint := apiURL + "?" + url.Values{"mode": {mode}, "lang": {lang}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("The Tarkov Market request could not be created")
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.gate.backoff(0)
		return nil, errors.New("The Tarkov Market API is unavailable")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case http.StatusTooManyRequests:
		c.gate.backoff(retryAfter(resp.Header.Get("Retry-After"), time.Now()))
		return nil, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			c.gate.backoff(0)
		}
		return nil, fmt.Errorf("Tarkov Market API response: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		c.gate.backoff(0)
		return nil, errors.New("The Tarkov Market response could not be read")
	}
	if len(body) > maxResponseBytes {
		c.gate.backoff(0)
		return nil, ErrResponseTooBig
	}
	var items []Item
	if err := json.Unmarshal(body, &items); err != nil || len(items) == 0 {
		c.gate.backoff(0)
		return nil, ErrInvalidCatalog
	}
	items = cleanItems(items)
	if len(items) == 0 {
		c.gate.backoff(0)
		return nil, ErrInvalidCatalog
	}
	c.gate.success()
	return items, nil
}

func retryAfter(value string, now time.Time) time.Duration {
	delay := time.Minute
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds > 0 {
		// Az időtartam átalakítása előtt korlátozzuk, így nem lehet túlcsordulás.
		if seconds > 3600 {
			seconds = 3600
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, err := http.ParseTime(value); err == nil && date.After(now) {
		delay = date.Sub(now)
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return delay
}

func cleanItems(items []Item) []Item {
	cleaned := make([]Item, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.UID) == "" || strings.TrimSpace(item.Name) == "" {
			continue
		}
		item.Img = SafeImageURL(item.Img)
		item.Icon = SafeImageURL(item.Icon)
		item.Link = SafeItemURL(item.Link)
		cleaned = append(cleaned, item)
	}
	return cleaned
}

func (c *Client) cachePath(mode, lang string) string {
	return filepath.Join(c.cacheDir, "catalog-"+mode+"-"+lang+".json")
}

func (c *Client) readCache(mode, lang string) (Catalog, bool) {
	if c.cacheDir == "" {
		return Catalog{}, false
	}
	f, err := os.Open(c.cachePath(mode, lang))
	if err != nil {
		return Catalog{}, false
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Catalog{}, false
	}
	var cached diskCatalog
	if err := json.Unmarshal(body, &cached); err != nil || cached.Version != 1 || cached.Mode != mode || cached.Lang != lang || cached.UpdatedAt.IsZero() {
		return Catalog{}, false
	}
	items := cleanItems(cached.Items)
	if len(items) == 0 {
		return Catalog{}, false
	}
	return Catalog{Items: items, UpdatedAt: cached.UpdatedAt}, true
}

func (c *Client) writeCache(mode, lang string, catalog Catalog) error {
	if c.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.cacheDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.cacheDir, ".catalog-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(diskCatalog{Version: 1, Mode: mode, Lang: lang, Items: catalog.Items, UpdatedAt: catalog.UpdatedAt}); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.cachePath(mode, lang))
}

// SafeImageURL kizárólag a dokumentált Tarkov Market CDN HTTPS-címét engedi.
// A visszatérő üres sztring azt jelzi, hogy a címet nem szabad megnyitni.
func SafeImageURL(raw string) string { return safeURL(raw, "cdn.tarkov-market.app", false) }

// SafeItemURL kizárólag a Tarkov Market HTTPS-tárgyoldalait engedi.
func SafeItemURL(raw string) string { return safeURL(raw, "tarkov-market.com", true) }

func safeURL(raw, host string, itemOnly bool) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil || u.Opaque != "" {
		return ""
	}
	if itemOnly {
		// A lokalizált linkek /hu/item/... alakúak; tetszőleges oldal helyett
		// csak az API által dokumentált tárgyoldal nyitható meg.
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if !((len(parts) == 2 && parts[0] == "item" && parts[1] != "") ||
			(len(parts) == 3 && parts[1] == "item" && parts[2] != "" && validLocale(parts[0]))) {
			return ""
		}
	}
	if u.Path == "" {
		return ""
	}
	return u.String()
}

func validLocale(locale string) bool {
	_, _, err := parameters("pvp", locale)
	return err == nil && locale != ""
}
