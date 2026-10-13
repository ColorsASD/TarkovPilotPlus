package market

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const apiBaseURL = "https://api.tarkov-market.app/api/v1"

var (
	ErrProgressForbidden        = errors.New("The Tarkov Market progress API requires PRO access and an available game mode")
	ErrProgressNotFound         = errors.New("The character or tracked item is unavailable")
	ErrProgressConflict         = errors.New("Progress changed; refresh tracked items")
	ErrProgressRevisionRequired = errors.New("A valid progress revision is required for this change")
	ErrProgressInvalidQuantity  = errors.New("Stock quantity must be an integer between 0 and 1000000000")
	ErrProgressInvalidRequest   = errors.New("The Tarkov Market progress request data is invalid")
	ErrInvalidProgress          = errors.New("The Tarkov Market progress response is invalid")
	ErrProgressUnavailable      = errors.New("The Tarkov Market progress API is unavailable")
	ErrProgressWriteUncertain   = errors.New("The stock change result is unknown; refresh stock before another change")
	ErrItemNotFound             = errors.New("The Tarkov Market item ID was not found")
)

// A progress 300/perc kerete az olvasásokra és módosításokra együtt érvényes.
// A PATCH ezen felül külön, 60/perc keretet is használ. Mindkettő közös a kliensekben.
var (
	progressRequests = newRequestGate(time.Minute / 300)
	progressWrites   = newRequestGate(time.Minute / 60)
)

type Character struct {
	CharUID  string `json:"charUid"`
	Name     string `json:"name"`
	GameMode string `json:"gameMode"`
	// A nyilvános /characters példa nem garantál szintadatot. A hiányzó
	// vagy null érték nil marad; más válaszból vagy névből nem következtetünk rá.
	Level *int `json:"level"`
}

type TrackedSource struct {
	Type                      string `json:"type"`
	QuestID                   string `json:"questId,omitempty"`
	UpgradeKey                string `json:"upgradeKey,omitempty"`
	Amount                    int64  `json:"amount"`
	RemainingRequiredQuantity int64  `json:"remainingRequiredQuantity"`
	FIR                       bool   `json:"fir"`
	Optional                  bool   `json:"optional"`
	Completed                 bool   `json:"completed"`
	Canceled                  bool   `json:"canceled"`
}

type TrackedItem struct {
	ItemID            string          `json:"itemId"`
	Name              string          `json:"name"`
	ShortName         string          `json:"shortName"`
	Slots             float64         `json:"slots"`
	Icon              string          `json:"icon"`
	RequiredQuantity  int64           `json:"requiredQuantity"`
	SpentQuantity     int64           `json:"spentQuantity"`
	AvailableQuantity int64           `json:"availableQuantity"`
	RemainingQuantity int64           `json:"remainingQuantity"`
	Sources           []TrackedSource `json:"sources"`
}

type TrackedSnapshot struct {
	CharUID              string        `json:"charUid"`
	GameMode             string        `json:"gameMode"`
	Revision             string        `json:"revision"`
	FIRInventorySeparate bool          `json:"firInventorySeparate"`
	Items                []TrackedItem `json:"items"`
}

type ProgressWriteResult struct {
	OK                    bool   `json:"ok"`
	Changed               bool   `json:"changed"`
	Revision              string `json:"revision"`
	NotificationDelivered bool   `json:"notificationDelivered"`
}

// ListCharacters a kulcshoz tartozó karaktereket olvassa. Személyes adatot nem
// gyorsítótáraz: minden hívás külön HTTP-kérés, a kulcs csak fejlécben szerepel.
func (c *Client) ListCharacters(ctx context.Context, apiKey string) ([]Character, error) {
	body, err := c.progressRequest(ctx, apiKey, http.MethodGet, "/characters", nil, nil, "")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Characters []Character `json:"characters"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Characters == nil {
		return nil, ErrInvalidProgress
	}
	seen := make(map[string]bool)
	for _, character := range payload.Characters {
		if !validProgressID(character.CharUID) || !validGameMode(character.GameMode) || seen[character.CharUID] {
			return nil, ErrInvalidProgress
		}
		seen[character.CharUID] = true
	}
	return payload.Characters, nil
}

// LoadTracked az adott karakter pillanatnyi készletét olvassa. A GameMode a
// karakterből származik; a tárgyjelző árlistájának módját nem küldjük a kérésben.
func (c *Client) LoadTracked(ctx context.Context, apiKey, charUID, lang string) (TrackedSnapshot, error) {
	if !validProgressID(charUID) {
		return TrackedSnapshot{}, ErrProgressInvalidRequest
	}
	_, lang, err := parameters("pvp", lang)
	if err != nil {
		return TrackedSnapshot{}, err
	}
	body, err := c.progressRequest(ctx, apiKey, http.MethodGet, "/characters/"+charUID+"/tracked-items", url.Values{"lang": {lang}}, nil, "")
	if err != nil {
		return TrackedSnapshot{}, err
	}
	var snapshot TrackedSnapshot
	if json.Unmarshal(body, &snapshot) != nil || snapshot.CharUID != charUID || !validGameMode(snapshot.GameMode) ||
		!validRevision(snapshot.Revision) || snapshot.Items == nil {
		return TrackedSnapshot{}, ErrInvalidProgress
	}
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if !validProgressID(item.ItemID) || item.RequiredQuantity < 0 || item.SpentQuantity < 0 ||
			item.AvailableQuantity < 0 || item.AvailableQuantity > 1_000_000_000 || item.RemainingQuantity < 0 {
			return TrackedSnapshot{}, ErrInvalidProgress
		}
		for _, source := range item.Sources {
			if source.Amount < 0 || source.RemainingRequiredQuantity < 0 {
				return TrackedSnapshot{}, ErrInvalidProgress
			}
		}
		item.Icon = SafeImageURL(item.Icon)
	}
	// Egy BSG-azonosítóhoz külön változatok is tartozhatnak (például teli és
	// üres tartály). Minden rekord saját készletét és forrásait őrizzük meg;
	// a csak BSG-azonosítót fogadó PATCH egyértelműségét a hívó ellenőrzi.
	return snapshot, nil
}

// SetTrackedQuantity kizárólag az abszolút készletet módosítja a kiolvasott
// revízió alapján. Ütközéskor vagy bizonytalan hálózati eredménynél nem próbálkozik
// újra; a hívónak új pillanatképből kell egyeztetnie a következő felhasználói műveletet.
func (c *Client) SetTrackedQuantity(ctx context.Context, apiKey, charUID, itemID, revision string, quantity int64) (ProgressWriteResult, error) {
	if !validProgressID(charUID) || !validProgressID(itemID) {
		return ProgressWriteResult{}, ErrProgressInvalidRequest
	}
	if !validRevision(revision) {
		return ProgressWriteResult{}, ErrProgressRevisionRequired
	}
	if quantity < 0 || quantity > 1_000_000_000 {
		return ProgressWriteResult{}, ErrProgressInvalidQuantity
	}
	body, _ := json.Marshal(struct {
		AvailableQuantity int64 `json:"availableQuantity"`
	}{AvailableQuantity: quantity})
	response, err := c.progressRequest(ctx, apiKey, http.MethodPatch, "/characters/"+charUID+"/tracked-items/"+itemID, nil, body, revision)
	if err != nil {
		return ProgressWriteResult{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(response, &fields) != nil || fields == nil {
		return ProgressWriteResult{}, invalidWriteResponse("invalid_json", nil, nil)
	}
	var ok, changed *bool
	if field := fields["ok"]; len(field) != 0 {
		if json.Unmarshal(field, &ok) != nil {
			ok = nil
		}
	}
	changedField, exists := fields["changed"]
	if !exists {
		return ProgressWriteResult{}, invalidWriteResponse("missing_changed", ok, nil)
	}
	if string(bytes.TrimSpace(changedField)) == "null" {
		return ProgressWriteResult{}, invalidWriteResponse("null_changed", ok, nil)
	}
	if json.Unmarshal(changedField, &changed) != nil || changed == nil {
		return ProgressWriteResult{}, invalidWriteResponse("invalid_changed", ok, nil)
	}
	if ok == nil || !*ok {
		return ProgressWriteResult{}, invalidWriteResponse("not_ok", ok, changed)
	}
	var ackRevision string
	if json.Unmarshal(fields["revision"], &ackRevision) != nil || !validRevision(ackRevision) {
		return ProgressWriteResult{}, invalidWriteResponse("invalid_revision", ok, changed)
	}
	var result ProgressWriteResult
	if json.Unmarshal(response, &result) != nil {
		return ProgressWriteResult{}, invalidWriteResponse("invalid_schema", ok, changed)
	}
	// notificationDelivered=false nem oka újabb PATCH-nek. changed=false
	// sikeres, idempotens setter eredmény, de nem igazol új hozzáadott darabot;
	// a hívó külön ellenőrzi a készletet és a művelet típusát.
	return result, nil
}

// LookupItem UID alapján tölti ki a régi katalógusból hiányzó BSGID mezőt.
// Név szerinti keresést és privát készletcache-t nem használ.
func (c *Client) LookupItem(ctx context.Context, apiKey, uid, mode, lang string) (Item, error) {
	if !validMarketUID(uid) {
		return Item{}, ErrItemNotFound
	}
	mode, lang, err := parameters(mode, lang)
	if err != nil {
		return Item{}, err
	}
	body, err := c.apiRequest(ctx, apiKey, http.MethodGet, "/item", url.Values{"uid": {uid}, "mode": {mode}, "lang": {lang}}, nil, "", c.priceGate, nil)
	if err != nil {
		if errors.Is(err, ErrProgressNotFound) {
			return Item{}, ErrItemNotFound
		}
		return Item{}, err
	}
	var items []Item
	if json.Unmarshal(body, &items) != nil {
		return Item{}, ErrInvalidCatalog
	}
	items = cleanItems(items)
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	if len(items) != 1 || items[0].UID != uid || !validProgressID(items[0].BSGID) {
		return Item{}, ErrInvalidCatalog
	}
	return items[0], nil
}

func (c *Client) progressRequest(ctx context.Context, apiKey, method, path string, query url.Values, body []byte, revision string) ([]byte, error) {
	var writeGate *requestGate
	if method == http.MethodPatch {
		writeGate = c.progressWriteGate
	}
	return c.apiRequest(ctx, apiKey, method, path, query, body, revision, c.progressGate, writeGate)
}

func (c *Client) apiRequest(ctx context.Context, apiKey, method, path string, query url.Values, body []byte, revision string, gate, writeGate *requestGate) ([]byte, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, ErrAPIKeyRequired
	}
	if len(apiKey) > 4096 || strings.ContainsAny(apiKey, "\r\n\x00") {
		return nil, ErrUnauthorized
	}
	if writeGate != nil {
		if err := writeGate.enter(ctx); err != nil {
			return nil, err
		}
		defer writeGate.leave()
	}
	if err := gate.enter(ctx); err != nil {
		return nil, err
	}
	defer gate.leave()
	endpoint := apiBaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrProgressInvalidRequest
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	if writeGate != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", strconv.Quote(revision))
	}
	// A védelem akkor is megmarad, ha a belső HTTP-kliens tesztben vagy később
	// más redirect-beállítást kapna. A kulcs soha nem követ átirányítást.
	httpClient := *c.http
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			gate.backoff(0)
			reason := "network"
			var timedOut interface{ Timeout() bool }
			if errors.As(err, &timedOut) && timedOut.Timeout() {
				reason = "timeout"
			}
			err = &progressDiagnosticError{details: ProgressWriteFailureInfo{Reason: reason}, cause: ErrProgressUnavailable}
		}
		return nil, uncertainWrite(err, writeGate != nil)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, progressHTTPError(resp.StatusCode, ErrUnauthorized)
	case http.StatusForbidden:
		return nil, progressHTTPError(resp.StatusCode, ErrProgressForbidden)
	case http.StatusNotFound:
		return nil, progressHTTPError(resp.StatusCode, ErrProgressNotFound)
	case http.StatusConflict:
		return nil, progressHTTPError(resp.StatusCode, ErrProgressConflict)
	case http.StatusPreconditionRequired:
		return nil, progressHTTPError(resp.StatusCode, ErrProgressRevisionRequired)
	case http.StatusBadRequest:
		return nil, progressHTTPError(resp.StatusCode, ErrProgressInvalidRequest)
	case http.StatusTooManyRequests:
		delay := retryAfter(resp.Header.Get("Retry-After"), time.Now())
		gate.backoff(delay)
		if writeGate != nil {
			writeGate.backoff(delay)
		}
		return nil, progressHTTPError(resp.StatusCode, ErrRateLimited)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := progressHTTPError(resp.StatusCode, ErrProgressInvalidRequest)
		if resp.StatusCode >= 500 {
			gate.backoff(0)
			err = progressHTTPError(resp.StatusCode, ErrProgressUnavailable)
		}
		return nil, uncertainWrite(err, writeGate != nil && resp.StatusCode >= 500)
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, uncertainWrite(ctx.Err(), writeGate != nil)
		}
		gate.backoff(0)
		return nil, uncertainWrite(ErrProgressUnavailable, writeGate != nil)
	}
	if len(response) > maxResponseBytes {
		return nil, uncertainWrite(ErrResponseTooBig, writeGate != nil)
	}
	gate.success()
	if writeGate != nil {
		writeGate.success()
	}
	return response, nil
}

func uncertainWrite(err error, write bool) error {
	if write {
		return errors.Join(ErrProgressWriteUncertain, err)
	}
	return err
}

func validProgressID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validRevision(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

func validMarketUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func validGameMode(mode string) bool {
	return mode == "PVP" || mode == "PVE" || mode == "SEASON"
}
