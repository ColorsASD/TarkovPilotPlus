package market

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testClient(dir string, f roundTripFunc) *Client {
	client := NewClient(dir)
	client.http.Transport = f
	client.gate = newRequestGate(0)
	client.priceGate = newRequestGate(0)
	return client
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const sampleCatalog = `[{"uid":"ledx","name":"LEDX Skin Transilluminator","shortName":"LEDX","price":550000,"avg24hPrice":598993,"traderName":"Therapist","traderPrice":494700,"traderPriceCur":"₽","traderPriceRub":494700,"slots":1,"img":"https://cdn.tarkov-market.app/images/items/ledx_sm.png","icon":"https://cdn.tarkov-market.app/images/items/ledx_sm.png","link":"https://tarkov-market.com/item/ledx_skin_transilluminator","haveMarketData":true,"bannedOnFlea":false,"updated":"2026-10-10T07:00:00Z"}]`

func TestLoadPersistsCatalogWithoutAPIKeyAndUsesFreshCache(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	client := testClient(dir, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Scheme != "https" || req.URL.Host != "api.tarkov-market.app" || req.URL.Path != "/api/v1/items/all" || req.Method != http.MethodGet {
			t.Fatalf("unexpected API request: %s %s", req.Method, req.URL)
		}
		if req.URL.Query().Get("mode") != "pvp" || req.URL.Query().Get("lang") != "en" || req.Header.Get("x-api-key") != "secret-api-key" {
			t.Fatal("wrong request mode, locale or authentication")
		}
		if strings.Contains(req.URL.String(), "secret-api-key") {
			t.Fatal("API key appeared in URL")
		}
		return response(200, sampleCatalog), nil
	})
	first, err := client.Load(context.Background(), "secret-api-key", "", "")
	if err != nil || len(first.Items) != 1 || first.Stale || first.UpdatedAt.IsZero() || first.Items[0].TraderPriceCur != "₽" {
		t.Fatalf("invalid first catalog: %#v, %v", first, err)
	}
	first.Items[0].Name = "caller modified this copy"
	second, err := client.Load(context.Background(), "secret-api-key", "PVP", "EN")
	if err != nil || calls != 1 || second.Items[0].Name != "LEDX Skin Transilluminator" {
		t.Fatalf("fresh cache was not reused independently: %#v, %v, calls=%d", second, err, calls)
	}
	body, err := os.ReadFile(client.cachePath("pvp", "en"))
	if err != nil || strings.Contains(string(body), "secret-api-key") || strings.Contains(string(body), "x-api-key") {
		t.Fatalf("cache missing or contains authentication: %v", err)
	}
	restarted := testClient(dir, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("restart should use the persisted fresh catalog")
		return nil, errors.New("unexpected request")
	})
	catalog, err := restarted.Load(context.Background(), "secret-api-key", "pvp", "en")
	if err != nil || len(catalog.Items) != 1 || !catalog.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("persisted cache was not reused: %#v, %v", catalog, err)
	}
}

func TestModeAndLanguageHaveSeparateCatalogs(t *testing.T) {
	calls := 0
	client := testClient(t.TempDir(), func(req *http.Request) (*http.Response, error) {
		calls++
		body := strings.ReplaceAll(sampleCatalog, "LEDX Skin Transilluminator", req.URL.Query().Get("mode")+" "+req.URL.Query().Get("lang"))
		return response(200, body), nil
	})
	for _, settings := range [][2]string{{"pvp", "en"}, {"pve", "en"}, {"pve", "hu"}, {"season", "hu"}, {"pvp", "en"}} {
		catalog, err := client.Load(context.Background(), "key", settings[0], settings[1])
		if err != nil || catalog.Items[0].Name != settings[0]+" "+settings[1] {
			t.Fatalf("mixed catalog for %v: %#v, %v", settings, catalog, err)
		}
	}
	if calls != 4 {
		t.Fatalf("requests=%d, want 4 distinct catalogs", calls)
	}
}

func TestFutureOrMismatchedDiskCacheCannotSuppressRefresh(t *testing.T) {
	for _, kind := range []string{"future", "wrong-mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			client := NewClient(dir)
			cached := Catalog{Items: []Item{{UID: "old", Name: "Untrusted old item"}}, UpdatedAt: time.Now()}
			if kind == "future" {
				cached.UpdatedAt = cached.UpdatedAt.Add(time.Hour)
			}
			if err := client.writeCache("pvp", "en", cached); err != nil {
				t.Fatal(err)
			}
			if kind == "wrong-mode" {
				body, err := os.ReadFile(client.cachePath("pvp", "en"))
				if err != nil {
					t.Fatal(err)
				}
				body = []byte(strings.ReplaceAll(string(body), `"mode":"pvp"`, `"mode":"pve"`))
				if err := os.WriteFile(client.cachePath("pvp", "en"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			restarted := testClient(dir, func(_ *http.Request) (*http.Response, error) {
				calls++
				return response(200, sampleCatalog), nil
			})
			catalog, err := restarted.Load(context.Background(), "key", "pvp", "en")
			if err != nil || calls != 1 || catalog.Items[0].UID != "ledx" {
				t.Fatalf("invalid disk cache was trusted: %#v, %v, calls=%d", catalog, err, calls)
			}
		})
	}
}

func TestExpiredCacheRefreshesAndFailureReturnsStaleCatalog(t *testing.T) {
	now := time.Now()
	calls := 0
	client := testClient(t.TempDir(), func(_ *http.Request) (*http.Response, error) {
		calls++
		if calls == 3 {
			return response(503, `{"error":"no service"}`), nil
		}
		return response(200, sampleCatalog), nil
	})
	client.now = func() time.Time { return now }
	if _, err := client.Load(context.Background(), "key", "pvp", "en"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(cacheTTL - time.Millisecond)
	if _, err := client.Load(context.Background(), "key", "pvp", "en"); err != nil || calls != 1 {
		t.Fatal("cache expired before five minutes")
	}
	now = now.Add(time.Millisecond)
	refreshed, err := client.Load(context.Background(), "key", "pvp", "en")
	if err != nil || calls != 2 || refreshed.Stale {
		t.Fatalf("five-minute refresh failed: %#v, %v", refreshed, err)
	}
	now = now.Add(cacheTTL)
	stale, err := client.Load(context.Background(), "key", "pvp", "en")
	if err == nil || calls != 3 || !stale.Stale || len(stale.Items) != 1 || !stale.UpdatedAt.Equal(refreshed.UpdatedAt) {
		t.Fatalf("stale fallback failed: %#v, %v", stale, err)
	}
	client.gate.mu.Lock()
	delay := time.Until(client.gate.next)
	client.gate.mu.Unlock()
	if delay < 14*time.Second {
		t.Fatal("service failure did not apply request backoff")
	}
}

func TestMissingKeyInvalidSettingsAndUnauthorized(t *testing.T) {
	calls := 0
	client := testClient(t.TempDir(), func(_ *http.Request) (*http.Response, error) {
		calls++
		return response(401, `{"error":"Access denied","key":"server must never be logged"}`), nil
	})
	if _, err := client.Load(context.Background(), "  ", "pvp", "en"); !errors.Is(err, ErrAPIKeyRequired) {
		t.Fatalf("missing key returned %v", err)
	}
	for _, settings := range [][2]string{{"../../outside", "en"}, {"pvp", "../outside"}} {
		if _, err := client.Load(context.Background(), "key", settings[0], settings[1]); err == nil {
			t.Fatal("invalid cache parameters accepted")
		}
	}
	if calls != 0 {
		t.Fatal("request sent without key or with invalid parameters")
	}
	if _, err := client.Load(context.Background(), "key", "pvp", "en"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("401 returned %v", err)
	}
}

func TestRateLimitRespectsRetryAfterAndCanBeCancelled(t *testing.T) {
	calls := 0
	client := testClient(t.TempDir(), func(_ *http.Request) (*http.Response, error) {
		calls++
		resp := response(429, `{"error":"limit"}`)
		resp.Header.Set("Retry-After", "120")
		return resp, nil
	})
	if _, err := client.Load(context.Background(), "key", "pvp", "en"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429 returned %v", err)
	}
	client.gate.mu.Lock()
	delay := time.Until(client.gate.next)
	client.gate.mu.Unlock()
	if delay < 119*time.Second {
		t.Fatalf("Retry-After ignored: %v", delay)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Load(ctx, "key", "pve", "en"); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("backoff was not cancelled or sent another request: %v, calls=%d", err, calls)
	}
}

func TestRetryAfterDateAndBounds(t *testing.T) {
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"30", 30 * time.Second},
		{now.Add(2 * time.Minute).Format(http.TimeFormat), 2 * time.Minute},
		{"", time.Minute},
		{"-2", time.Minute},
		{"9223372036854775807", time.Hour},
		{now.Add(2 * time.Hour).Format(http.TimeFormat), time.Hour},
	} {
		if got := retryAfter(test.value, now); got != test.want {
			t.Errorf("retryAfter(%q)=%v, want %v", test.value, got, test.want)
		}
	}
}

func TestRedirectCannotForwardAPIKey(t *testing.T) {
	calls := 0
	client := testClient(t.TempDir(), func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.tarkov-market.app" || req.Header.Get("x-api-key") != "private-key" {
			t.Fatal("API key was forwarded outside the allowed host")
		}
		resp := response(302, "")
		resp.Header.Set("Location", "https://untrusted.example/collect")
		return resp, nil
	})
	if _, err := client.Load(context.Background(), "private-key", "pvp", "en"); err == nil || strings.Contains(err.Error(), "private-key") || calls != 1 {
		t.Fatalf("redirect policy failed: %v, calls=%d", err, calls)
	}
}

func TestBoundedAndInvalidResponses(t *testing.T) {
	for _, body := range []string{"null", "[]", "{}", sampleCatalog + "[]", `[{"uid":"missing-name"}]`} {
		client := testClient(t.TempDir(), func(_ *http.Request) (*http.Response, error) { return response(200, body), nil })
		if _, err := client.Load(context.Background(), "key", "pvp", "en"); !errors.Is(err, ErrInvalidCatalog) {
			t.Fatalf("invalid catalog %q returned %v", body, err)
		}
	}
	client := testClient(t.TempDir(), func(_ *http.Request) (*http.Response, error) {
		return response(200, strings.Repeat(" ", maxResponseBytes+1)), nil
	})
	if _, err := client.Load(context.Background(), "key", "pvp", "en"); !errors.Is(err, ErrResponseTooBig) {
		t.Fatalf("oversized response returned %v", err)
	}
}

func TestUnsafeURLsAreRemovedFromCatalog(t *testing.T) {
	body := strings.ReplaceAll(sampleCatalog, "cdn.tarkov-market.app", "cdn.tarkov-market.app.evil.example")
	body = strings.ReplaceAll(body, "tarkov-market.com/item/", "evil.example/item/")
	client := testClient(t.TempDir(), func(_ *http.Request) (*http.Response, error) { return response(200, body), nil })
	catalog, err := client.Load(context.Background(), "key", "pvp", "en")
	if err != nil || catalog.Items[0].Img != "" || catalog.Items[0].Icon != "" || catalog.Items[0].Link != "" {
		t.Fatalf("unsafe API URLs survived validation: %#v, %v", catalog, err)
	}
}

func TestSafeURLValidation(t *testing.T) {
	for _, raw := range []string{
		"http://cdn.tarkov-market.app/images/items/x.png",
		"https://cdn.tarkov-market.app.evil.example/images/items/x.png",
		"https://user@cdn.tarkov-market.app/images/items/x.png",
		"https://cdn.tarkov-market.app:443/images/items/x.png",
		"//cdn.tarkov-market.app/images/items/x.png",
		"data:image/png;base64,abc",
	} {
		if SafeImageURL(raw) != "" {
			t.Errorf("unsafe image URL accepted: %q", raw)
		}
	}
	if SafeImageURL("https://cdn.tarkov-market.app/images/items/x.png") == "" {
		t.Fatal("documented CDN URL rejected")
	}
	for _, raw := range []string{"https://tarkov-market.com/item/ledx", "https://tarkov-market.com/hu/item/ledx"} {
		if SafeItemURL(raw) == "" {
			t.Errorf("documented item page rejected: %q", raw)
		}
	}
	for _, raw := range []string{"https://example.com/item/ledx", "https://tarkov-market.com/login", "https://tarkov-market.com/evil/item/ledx", "https://tarkov-market.com/item/", "https://tarkov-market.com.evil.example/item/ledx"} {
		if SafeItemURL(raw) != "" {
			t.Errorf("unsafe item URL accepted: %q", raw)
		}
	}
}

func TestAllClientsShareProductionRequestGate(t *testing.T) {
	first, second := NewClient(""), NewClient("")
	if first.gate != second.gate || first.gate.interval != 12*time.Second || first.http.Timeout != 15*time.Second {
		t.Fatal("production request pacing or timeout is missing")
	}
}

func TestRequestGateSerializesAndSpacesRequests(t *testing.T) {
	gate := newRequestGate(30 * time.Millisecond)
	var mu sync.Mutex
	var starts []time.Time
	var running, maximumRunning int
	var workers sync.WaitGroup
	for range 3 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := gate.enter(context.Background()); err != nil {
				t.Error(err)
				return
			}
			defer gate.leave()
			mu.Lock()
			starts = append(starts, time.Now())
			running++
			maximumRunning = max(maximumRunning, running)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
		}()
	}
	workers.Wait()
	if maximumRunning != 1 || len(starts) != 3 {
		t.Fatalf("requests ran concurrently: %d, starts=%d", maximumRunning, len(starts))
	}
	for index := 1; index < len(starts); index++ {
		if starts[index].Sub(starts[index-1]) < 25*time.Millisecond {
			t.Fatal("request minimum spacing was bypassed")
		}
	}
}
