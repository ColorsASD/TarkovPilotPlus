package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	progressItemID = "5c0530ee86f774697952d952"
	progressUID    = "a7f7568c-61fb-437e-9f71-06e58aae26ba"
	charactersJSON = `{"characters":[{"charUid":"char-1","name":"Fő karakter","gameMode":"SEASON"}]}`
	trackedJSON    = `{"charUid":"char-1","gameMode":"SEASON","revision":"revision-1","firInventorySeparate":false,"items":[{"itemId":"5c0530ee86f774697952d952","name":"LEDX Skin Transilluminator","shortName":"LEDX","slots":1,"icon":"https://cdn.tarkov-market.app/images/items/ledx.png","requiredQuantity":10,"spentQuantity":2,"availableQuantity":5,"remainingQuantity":5,"sources":[{"type":"quest","questId":"quest-1","amount":6,"remainingRequiredQuantity":4,"fir":true,"completed":false,"canceled":false},{"type":"hideout","upgradeKey":"Workbench_2","amount":4,"remainingRequiredQuantity":4,"fir":false,"completed":false,"canceled":true}]}]}`
	writeJSON      = `{"ok":true,"changed":true,"revision":"revision-2","notificationDelivered":false}`
)

func progressTestClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	client := testClient(t.TempDir(), transport)
	client.progressGate = newRequestGate(0)
	client.progressWriteGate = newRequestGate(0)
	return client
}

func requireHeaderOnlyKey(t *testing.T, req *http.Request) {
	t.Helper()
	if req.URL.Scheme != "https" || req.URL.Host != "api.tarkov-market.app" || req.Header.Get("x-api-key") != "private-fixture-key" {
		t.Fatalf("wrong endpoint or authentication: %s", req.URL)
	}
	if strings.Contains(req.URL.String(), "private-fixture-key") || req.URL.Query().Has("x-api-key") || req.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("authentication in URL or missing no-store header")
	}
}

func TestProgressReadsAreUncachedAndNeverStoredOnDisk(t *testing.T) {
	calls := 0
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		requireHeaderOnlyKey(t, req)
		if req.Method != http.MethodGet || req.Header.Get("If-Match") != "" {
			t.Fatal("read request used a write method or precondition")
		}
		switch req.URL.Path {
		case "/api/v1/characters":
			if req.URL.RawQuery != "" {
				t.Fatal("character list should not use the price mode")
			}
			return response(200, charactersJSON), nil
		case "/api/v1/characters/char-1/tracked-items":
			if req.URL.Query().Get("lang") != "hu" || req.URL.Query().Has("mode") {
				t.Fatal("tracked snapshot has wrong locale or price mode")
			}
			return response(200, strings.ReplaceAll(trackedJSON, "revision-1", fmt.Sprintf("revision-%d", calls))), nil
		default:
			t.Fatalf("unexpected path: %s", req.URL.Path)
			return nil, nil
		}
	})
	for range 2 {
		characters, err := client.ListCharacters(context.Background(), "private-fixture-key")
		if err != nil || len(characters) != 1 || characters[0].CharUID != "char-1" || characters[0].GameMode != "SEASON" {
			t.Fatalf("wrong characters: %#v, %v", characters, err)
		}
	}
	for iteration := range 2 {
		snapshot, err := client.LoadTracked(context.Background(), "private-fixture-key", "char-1", "HU")
		if err != nil || snapshot.Revision != fmt.Sprintf("revision-%d", iteration+3) || snapshot.FIRInventorySeparate || snapshot.GameMode != "SEASON" {
			t.Fatalf("stale or incomplete tracked snapshot: %#v, %v", snapshot, err)
		}
		item := snapshot.Items[0]
		if item.ItemID != progressItemID || item.RequiredQuantity != 10 || item.SpentQuantity != 2 || item.AvailableQuantity != 5 || item.RemainingQuantity != 5 ||
			len(item.Sources) != 2 || !item.Sources[0].FIR || item.Sources[1].FIR || item.Sources[1].UpgradeKey != "Workbench_2" || !item.Sources[1].Canceled {
			t.Fatalf("tracked item data was altered: %#v", item)
		}
	}
	if calls != 4 {
		t.Fatalf("progress reads were cached: requests=%d", calls)
	}
	entries, err := os.ReadDir(client.cacheDir)
	if err != nil || len(entries) != 0 || len(client.catalogs) != 0 {
		t.Fatalf("private progress entered catalog cache: files=%v, err=%v", entries, err)
	}
}

func TestCharactersKeepIdenticalNamesAsDistinctUIDsAndMissingLevelAsNull(t *testing.T) {
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/characters" {
			t.Fatal("character list used another endpoint")
		}
		return response(200, `{"characters":[{"charUid":"char-pvp","name":"ColorsASD","gameMode":"PVP"},{"charUid":"char-pve","name":"ColorsASD","gameMode":"PVE","level":null},{"charUid":"char-season","name":"Seasonal","gameMode":"SEASON"}]}`), nil
	})
	characters, err := client.ListCharacters(context.Background(), "key")
	if err != nil || len(characters) != 3 {
		t.Fatalf("identical character names lost entries: %#v, %v", characters, err)
	}
	for index, uid := range []string{"char-pvp", "char-pve", "char-season"} {
		if characters[index].CharUID != uid || characters[index].Level != nil {
			t.Fatalf("character identity or unknown level changed: %#v", characters[index])
		}
	}
	if characters[0].Name != "ColorsASD" || characters[1].Name != "ColorsASD" || characters[0].GameMode != "PVP" || characters[1].GameMode != "PVE" || characters[2].GameMode != "SEASON" {
		t.Fatalf("character names or modes were merged: %#v", characters)
	}
	encoded, err := json.Marshal(characters)
	if err != nil || strings.Count(string(encoded), `"level":null`) != 3 {
		t.Fatalf("unknown levels were fabricated or omitted: %s, %v", encoded, err)
	}
}

func TestTrackedSourceOptionalFlagIsPreserved(t *testing.T) {
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"charUid":"char-1","gameMode":"PVP","revision":"rev-1","firInventorySeparate":false,"items":[{"itemId":"source-item","requiredQuantity":3,"availableQuantity":0,"remainingQuantity":3,"sources":[{"type":"quest","amount":20,"remainingRequiredQuantity":20,"fir":true,"optional":true},{"type":"hideout","amount":3,"remainingRequiredQuantity":3,"fir":false}]}]}`), nil
	})
	snapshot, err := client.LoadTracked(context.Background(), "local-test-key", "char-1", "en")
	if err != nil || len(snapshot.Items) != 1 || len(snapshot.Items[0].Sources) != 2 ||
		!snapshot.Items[0].Sources[0].Optional || snapshot.Items[0].Sources[1].Optional {
		t.Fatalf("optional source flag was lost or invented: %+v %v", snapshot, err)
	}
}

func TestCharacterLevelComesOnlyFromItsOwnResponseField(t *testing.T) {
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"characters":[{"charUid":"char-a","name":"ColorsASD","gameMode":"PVP","level":42},{"charUid":"char-b","name":"ColorsASD","gameMode":"PVP","experience":123456}]}`), nil
	})
	characters, err := client.ListCharacters(context.Background(), "key")
	if err != nil || len(characters) != 2 || characters[0].Level == nil || *characters[0].Level != 42 || characters[1].Level != nil {
		t.Fatalf("level was lost or inferred from another character/experience: %#v, %v", characters, err)
	}
}

func TestTrackedWriteUsesOnlyAbsoluteQuantityAndQuotedRevision(t *testing.T) {
	for _, quantity := range []int64{0, 6, 1_000_000_000} {
		t.Run(fmt.Sprint(quantity), func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				requireHeaderOnlyKey(t, req)
				if req.Method != http.MethodPatch || req.URL.Path != "/api/v1/characters/char-1/tracked-items/"+progressItemID || req.URL.RawQuery != "" ||
					req.Header.Get("If-Match") != `"revision-1"` || req.Header.Get("Content-Type") != "application/json" {
					t.Fatal("wrong PATCH endpoint, revision or content type")
				}
				body, err := io.ReadAll(req.Body)
				if err != nil || string(body) != fmt.Sprintf(`{"availableQuantity":%d}`, quantity) {
					t.Fatalf("PATCH sent fields besides absolute quantity: %s, %v", body, err)
				}
				return response(200, writeJSON), nil
			})
			result, err := client.SetTrackedQuantity(context.Background(), "private-fixture-key", "char-1", progressItemID, "revision-1", quantity)
			if err != nil || !result.OK || !result.Changed || result.Revision != "revision-2" || result.NotificationDelivered || calls != 1 {
				t.Fatalf("notification failure was mistaken for write failure: %#v, %v, calls=%d", result, err, calls)
			}
		})
	}
}

func TestTrackedWriteUnchangedIsSuccessful(t *testing.T) {
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, strings.ReplaceAll(writeJSON, `"changed":true`, `"changed":false`)), nil
	})
	result, err := client.SetTrackedQuantity(context.Background(), "key", "char-1", progressItemID, "revision-1", 5)
	if err != nil || !result.OK || result.Changed {
		t.Fatalf("idempotent write reported a failure: %#v, %v", result, err)
	}
}

func TestTrackedWriteMissingChangedIsUncertainAndNeverRetries(t *testing.T) {
	for _, body := range []string{
		`{"ok":true,"revision":"revision-2","notificationDelivered":true}`,
		`{"ok":true,"changed":null,"revision":"revision-2","notificationDelivered":true}`,
	} {
		calls := 0
		client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			return response(200, body), nil
		})
		result, err := client.SetTrackedQuantity(context.Background(), "key", "char-1", progressItemID, "revision-1", 1)
		if !errors.Is(err, ErrProgressWriteUncertain) || !errors.Is(err, ErrInvalidProgress) || result.OK || calls != 1 {
			t.Fatalf("missing mutation acknowledgment was treated as an explicit no-op or repeated: result=%+v err=%v calls=%d", result, err, calls)
		}
	}
}

func TestTrackedConflictNeverRetriesAndAllowsExplicitReread(t *testing.T) {
	methods := make([]string, 0)
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		methods = append(methods, req.Method)
		if req.Method == http.MethodPatch {
			return response(409, `{"error":"conflicting_progress"}`), nil
		}
		return response(200, strings.ReplaceAll(trackedJSON, "revision-1", "revision-new")), nil
	})
	_, err := client.SetTrackedQuantity(context.Background(), "key", "char-1", progressItemID, "revision-old", 6)
	if !errors.Is(err, ErrProgressConflict) || errors.Is(err, ErrProgressWriteUncertain) || len(methods) != 1 {
		t.Fatalf("conflict caused automatic write/reload: %v, requests=%v", err, methods)
	}
	snapshot, err := client.LoadTracked(context.Background(), "key", "char-1", "en")
	if err != nil || snapshot.Revision != "revision-new" || strings.Join(methods, ",") != "PATCH,GET" {
		t.Fatalf("explicit conflict reread failed: %#v, %v, methods=%v", snapshot, err, methods)
	}
}

func TestTrackedWriteAmbiguityNeverRepeatsOrLeaksTransportErrors(t *testing.T) {
	for _, failure := range []string{"network", "timeout", "server", "invalid-json", "body-read"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				switch failure {
				case "network":
					return nil, errors.New("transport exposed private-fixture-key")
				case "timeout":
					<-req.Context().Done()
					return nil, req.Context().Err()
				case "server":
					return response(503, `{"error":"private-fixture-key"}`), nil
				case "invalid-json":
					return response(200, "private-fixture-key"), nil
				default:
					result := response(200, "")
					result.Body = io.NopCloser(errorReader{})
					return result, nil
				}
			})
			ctx := context.Background()
			if failure == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			_, err := client.SetTrackedQuantity(ctx, "private-fixture-key", "char-1", progressItemID, "revision-1", 6)
			if !errors.Is(err, ErrProgressWriteUncertain) || calls != 1 || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("ambiguous write repeated or leaked key: %v, calls=%d", err, calls)
			}
			if failure == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost cancellation reason: %v", err)
			}
			if failure == "server" && (!errors.Is(err, ErrProgressUnavailable) || !strings.Contains(err.Error(), "HTTP 503")) {
				t.Fatalf("server failure lost its category or status: %v", err)
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("private-fixture-key") }

func TestProgressHTTPStatusErrorsAreSanitized(t *testing.T) {
	for status, expected := range map[int]error{
		401: ErrUnauthorized, 403: ErrProgressForbidden, 404: ErrProgressNotFound, 409: ErrProgressConflict,
		428: ErrProgressRevisionRequired, 400: ErrProgressInvalidRequest, 413: ErrProgressPayloadTooLarge,
		500: ErrProgressUnavailable, 503: ErrProgressUnavailable,
	} {
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			t.Run(fmt.Sprint(status)+"/"+method, func(t *testing.T) {
				calls := 0
				client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
					calls++
					requireHeaderOnlyKey(t, req)
					if req.Method != method || req.Header.Get("Accept") != "application/json" {
						t.Fatalf("wrong progress method or response format: %s", req.Method)
					}
					if method == http.MethodPatch {
						body, err := io.ReadAll(req.Body)
						if err != nil || req.URL.Path != "/api/v1/characters/char-1/tracked-items/"+progressItemID || req.URL.RawQuery != "" ||
							req.Header.Get("If-Match") != `"revision-1"` || req.Header.Get("Content-Type") != "application/json" || string(body) != `{"availableQuantity":5}` {
							t.Fatalf("rejected PATCH lost its exact target, revision or payload: %s %s", req.URL, body)
						}
					} else if req.URL.Path != "/api/v1/characters/char-1/tracked-items" || req.URL.RawQuery != "lang=en" || req.Header.Get("If-Match") != "" {
						t.Fatalf("read request used write parameters: %s", req.URL)
					}
					return response(status, `{"error":"private-fixture-key"}`), nil
				})
				var err error
				if method == http.MethodPatch {
					_, err = client.SetTrackedQuantity(context.Background(), "private-fixture-key", "char-1", progressItemID, "revision-1", 5)
				} else {
					_, err = client.LoadTracked(context.Background(), "private-fixture-key", "char-1", "en")
				}
				if !errors.Is(err, expected) || strings.Contains(err.Error(), "private-fixture-key") || calls != 1 {
					t.Fatalf("wrong, repeated or unsanitized HTTP %d error: %v calls=%d", status, err, calls)
				}
				info := ProgressWriteFailure(err)
				if info.Reason != "http" || info.HTTPStatus != status || info.ACKOK != nil || info.ACKChanged != nil {
					t.Fatalf("HTTP diagnostic category was lost: %+v", info)
				}
				uncertain := method == http.MethodPatch && status >= 500
				if errors.Is(err, ErrProgressWriteUncertain) != uncertain {
					t.Fatalf("HTTP %d %s has wrong write certainty: %v", status, method, err)
				}
			})
		}
	}
}

func TestProgressRateLimitBackoffIsSharedByReadsAndWrites(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				result := response(429, `{"error":"rate_limit"}`)
				result.Header.Set("Retry-After", "2")
				return result, nil
			})
			var err error
			if write {
				_, err = client.SetTrackedQuantity(context.Background(), "key", "char-1", progressItemID, "revision-1", 5)
			} else {
				_, err = client.ListCharacters(context.Background(), "key")
			}
			client.progressGate.mu.Lock()
			waiting := time.Until(client.progressGate.next)
			client.progressGate.mu.Unlock()
			if !errors.Is(err, ErrRateLimited) || waiting < 1900*time.Millisecond {
				t.Fatalf("Retry-After ignored: %v, waiting=%v", err, waiting)
			}
			if write {
				client.progressWriteGate.mu.Lock()
				waiting = time.Until(client.progressWriteGate.next)
				client.progressWriteGate.mu.Unlock()
				if waiting < 1900*time.Millisecond {
					t.Fatal("PATCH budget lost its Retry-After")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			_, err = client.ListCharacters(ctx, "key")
			if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
				t.Fatalf("backoff could not be cancelled or made another request: %v, calls=%d", err, calls)
			}
		})
	}
}

func TestProgressAndPriceGatesAreIndependentAndSharedAcrossClients(t *testing.T) {
	first, second := NewClient(""), NewClient("")
	if first.progressGate != second.progressGate || first.progressWriteGate != second.progressWriteGate || first.priceGate != second.priceGate ||
		first.progressGate == first.priceGate || first.progressGate == first.gate || first.progressWriteGate == first.progressGate ||
		first.progressGate.interval != time.Minute/300 || first.progressWriteGate.interval != time.Second || first.priceGate.interval != time.Minute/300 {
		t.Fatal("wrong shared rate budgets")
	}
}

func TestProgressRefusesRedirectEvenWhenHTTPClientAllowsIt(t *testing.T) {
	for _, target := range []string{"https://evil.invalid/collect", "https://api.tarkov-market.app/api/v1/other"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 || req.URL.Host != "api.tarkov-market.app" {
					t.Fatal("private key followed a redirect")
				}
				result := response(302, "")
				result.Header.Set("Location", target)
				return result, nil
			})
			client.http.CheckRedirect = nil
			_, err := client.ListCharacters(context.Background(), "private-fixture-key")
			if err == nil || calls != 1 || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("redirect accepted or leaked authentication: %v, calls=%d", err, calls)
			}
		})
	}
}

func TestProgressRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	calls := 0
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, writeJSON), nil
	})
	for _, test := range []struct {
		key, charUID, itemID, revision string
		quantity                       int64
		want                           error
	}{
		{"", "char-1", progressItemID, "revision-1", 1, ErrAPIKeyRequired},
		{"key\r\nx-extra: bad", "char-1", progressItemID, "revision-1", 1, ErrUnauthorized},
		{"key", "../characters/another", progressItemID, "revision-1", 1, ErrProgressInvalidRequest},
		{"key", "char-1", "item?x-api-key=bad", "revision-1", 1, ErrProgressInvalidRequest},
		{"key", "char-1", progressItemID, "", 1, ErrProgressRevisionRequired},
		{"key", "char-1", progressItemID, "revision\nInjected", 1, ErrProgressRevisionRequired},
		{"key", "char-1", progressItemID, `"revision"`, 1, ErrProgressRevisionRequired},
		{"key", "char-1", progressItemID, "revision-1", -1, ErrProgressInvalidQuantity},
		{"key", "char-1", progressItemID, "revision-1", 1_000_000_001, ErrProgressInvalidQuantity},
	} {
		_, err := client.SetTrackedQuantity(context.Background(), test.key, test.charUID, test.itemID, test.revision, test.quantity)
		if !errors.Is(err, test.want) {
			t.Errorf("wrong input error: %v, want %v", err, test.want)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input reached HTTP: %d requests", calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.SetTrackedQuantity(ctx, "key", "char-1", progressItemID, "revision-1", 5)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrProgressWriteUncertain) || calls != 0 {
		t.Fatalf("pre-send cancellation marked a write as attempted: %v, calls=%d", err, calls)
	}
}

func TestTrackedSnapshotRejectsWrongCharacterAndMalformedQuantities(t *testing.T) {
	for _, body := range []string{
		strings.ReplaceAll(trackedJSON, `"charUid":"char-1"`, `"charUid":"char-2"`),
		strings.ReplaceAll(trackedJSON, `"revision":"revision-1"`, `"revision":""`),
		strings.ReplaceAll(trackedJSON, `"availableQuantity":5`, `"availableQuantity":-1`),
		strings.ReplaceAll(trackedJSON, `"availableQuantity":5`, `"availableQuantity":1.5`),
		strings.ReplaceAll(trackedJSON, `"remainingQuantity":5`, `"remainingQuantity":-1`),
		`{"charUid":"char-1","gameMode":"PVP","revision":"revision-1"}`,
		`{"error":"private-fixture-key"}`,
	} {
		client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		_, err := client.LoadTracked(context.Background(), "key", "char-1", "en")
		if !errors.Is(err, ErrInvalidProgress) || strings.Contains(err.Error(), "private-fixture-key") {
			t.Errorf("invalid snapshot accepted: %v", err)
		}
	}
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"charUid":"char-1","gameMode":"PVP","revision":"revision-1","items":[],"firInventorySeparate":false}`), nil
	})
	if snapshot, err := client.LoadTracked(context.Background(), "key", "char-1", "en"); err != nil || len(snapshot.Items) != 0 {
		t.Fatalf("valid empty progress rejected: %#v, %v", snapshot, err)
	}
}

func TestTrackedVariantsKeepTheirOwnCountsAndSourcesWithoutBlockingOtherItems(t *testing.T) {
	const fuelID = "aaaaaaaaaaaaaaaaaaaaaaaa"
	fixture := TrackedSnapshot{CharUID: "char-1", GameMode: "SEASON", Revision: "fixture-revision", Items: []TrackedItem{
		{ItemID: fuelID, Name: "Metal fuel tank", ShortName: "Fuel", Slots: 6, RequiredQuantity: 3, SpentQuantity: 1, AvailableQuantity: 2, RemainingQuantity: 1,
			Sources: []TrackedSource{{Type: "quest", QuestID: "fixture-quest", Amount: 4, RemainingRequiredQuantity: 3, FIR: true}}},
		{ItemID: fuelID, Name: "Metal fuel tank (0/100)", ShortName: "Fuel empty", Slots: 6, RequiredQuantity: 6, AvailableQuantity: 5, RemainingQuantity: 1,
			Sources: []TrackedSource{{Type: "hideout", UpgradeKey: "Fixture_2", Amount: 6, RemainingRequiredQuantity: 6}}},
		{ItemID: progressItemID, Name: "LEDX Skin Transilluminator", ShortName: "LEDX", Slots: 1, RequiredQuantity: 9, AvailableQuantity: 7, RemainingQuantity: 2,
			Sources: []TrackedSource{{Type: "quest", QuestID: "fixture-other-quest", Amount: 9, RemainingRequiredQuantity: 9, FIR: true}}},
	}}
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/characters/char-1/tracked-items" {
			t.Fatal("reading variants must not send a write or another endpoint")
		}
		return response(200, string(body)), nil
	})
	snapshot, err := client.LoadTracked(context.Background(), "key", "char-1", "en")
	if err != nil || !reflect.DeepEqual(snapshot, fixture) || calls != 1 {
		t.Fatalf("variants were rejected, merged or changed alongside other items: %#v, %v, requests=%d", snapshot, err, calls)
	}
	// A változatok elfogadása nem lazíthatja a többi mező ellenőrzését.
	fixture.Items[1].Sources[0].Amount = -1
	body, err = json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LoadTracked(context.Background(), "key", "char-1", "en"); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("invalid variant source quantity was accepted: %v", err)
	}
}

func TestTrackedIconsUseOnlyApprovedCDN(t *testing.T) {
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, strings.ReplaceAll(trackedJSON, "https://cdn.tarkov-market.app/images/items/ledx.png", "https://evil.invalid/private")), nil
	})
	snapshot, err := client.LoadTracked(context.Background(), "key", "char-1", "en")
	if err != nil || snapshot.Items[0].Icon != "" {
		t.Fatalf("unapproved tracked item image: %#v, %v", snapshot, err)
	}
}

func TestTrackedSeparateFIRInventoryIsPreserved(t *testing.T) {
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, strings.ReplaceAll(trackedJSON, `"firInventorySeparate":false`, `"firInventorySeparate":true`)), nil
	})
	snapshot, err := client.LoadTracked(context.Background(), "key", "char-1", "en")
	if err != nil || !snapshot.FIRInventorySeparate || !snapshot.Items[0].Sources[0].FIR || snapshot.Items[0].Sources[1].FIR {
		t.Fatalf("separate FiR stock or source flags were lost: %#v, %v", snapshot, err)
	}
}

func TestPatchSpacingAllowsReadsAndCancelsBeforeAnotherWrite(t *testing.T) {
	patches, reads := 0, 0
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPatch {
			patches++
			return response(200, writeJSON), nil
		}
		reads++
		return response(200, charactersJSON), nil
	})
	client.progressWriteGate = newRequestGate(time.Second)
	if _, err := client.SetTrackedQuantity(context.Background(), "key", "char-1", progressItemID, "revision-1", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListCharacters(context.Background(), "key"); err != nil {
		t.Fatalf("PATCH spacing incorrectly blocked a progress read: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := client.SetTrackedQuantity(ctx, "key", "char-1", progressItemID, "revision-2", 6)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrProgressWriteUncertain) || patches != 1 || reads != 1 {
		t.Fatalf("rate wait was treated as an attempted write: %v, PATCH=%d GET=%d", err, patches, reads)
	}
}

func TestPatchCancelledWhileWaitingForEitherGateNeverSendsHTTP(t *testing.T) {
	for _, waitingGate := range []string{"write-limit", "progress-limit"} {
		t.Run(waitingGate, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, writeJSON), nil
			})
			gate := client.progressWriteGate
			if waitingGate == "progress-limit" {
				gate = client.progressGate
			}
			gate.next = time.Now().Add(time.Hour)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, err := client.SetTrackedQuantity(ctx, "key", "char-1", progressItemID, "revision-1", 6)
				finished <- err
			}()
			// Várjuk meg a tényleges limiter-várakozást, majd szakítsuk meg a
			// már elindított műveletet a HTTP-küldés előtt.
			wait := time.NewTimer(500 * time.Millisecond)
			defer wait.Stop()
			poll := time.NewTicker(time.Millisecond)
			defer poll.Stop()
			for len(gate.token) == 0 {
				select {
				case err := <-finished:
					t.Fatalf("PATCH finished before entering the limiter: %v", err)
				case <-wait.C:
					t.Fatal("PATCH did not enter the limiter")
				case <-poll.C:
				}
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrProgressWriteUncertain) || calls != 0 {
					t.Fatalf("cancelled limiter wait sent or attempted a PATCH: %v, requests=%d", err, calls)
				}
				if len(client.progressGate.token) != 0 || len(client.progressWriteGate.token) != 0 {
					t.Fatal("cancelled PATCH did not release its limiter tokens")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("PATCH limiter wait ignored cancellation")
			}
		})
	}
}

func TestProgressBackoffDoesNotBlockPriceIdentityLookup(t *testing.T) {
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/api/v1/item" {
			body, _ := json.Marshal([]Item{{UID: progressUID, BSGID: progressItemID, Name: "LEDX"}})
			return response(200, string(body)), nil
		}
		result := response(429, "")
		result.Header.Set("Retry-After", "60")
		return result, nil
	})
	if _, err := client.ListCharacters(context.Background(), "key"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected progress rate limit, got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if item, err := client.LookupItem(ctx, "key", progressUID, "pvp", "en"); err != nil || item.BSGID != progressItemID {
		t.Fatalf("private progress backoff blocked price API: %#v, %v", item, err)
	}
}

type countedBody struct {
	remaining int64
	read      int64
	closed    bool
}

func (body *countedBody) Read(target []byte) (int, error) {
	if body.remaining == 0 {
		return 0, io.EOF
	}
	count := min(int64(len(target)), body.remaining)
	for i := int64(0); i < count; i++ {
		target[i] = 'x'
	}
	body.remaining -= count
	body.read += count
	return int(count), nil
}

func (body *countedBody) Close() error { body.closed = true; return nil }

func TestPrivateResponseSizeIsBoundedAndWriteOversizeIsUncertain(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			body := &countedBody{remaining: maxResponseBytes * 2}
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})
			var err error
			if write {
				_, err = client.SetTrackedQuantity(context.Background(), "key", "char-1", progressItemID, "revision-1", 5)
			} else {
				_, err = client.ListCharacters(context.Background(), "key")
			}
			if !errors.Is(err, ErrResponseTooBig) || errors.Is(err, ErrProgressWriteUncertain) != write || body.read != maxResponseBytes+1 || !body.closed {
				t.Fatalf("unbounded private response: err=%v, read=%d closed=%v", err, body.read, body.closed)
			}
		})
	}
}

func TestLookupItemUsesUIDAndFillsBSGIDWithoutPrivateCache(t *testing.T) {
	calls := 0
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		requireHeaderOnlyKey(t, req)
		if req.URL.Path != "/api/v1/item" || req.URL.Query().Get("uid") != progressUID || req.URL.Query().Get("mode") != "pve" || req.URL.Query().Get("lang") != "ru" ||
			req.URL.Query().Has("q") || req.Method != http.MethodGet {
			t.Fatal("item identity was looked up by name or wrong mode")
		}
		body, _ := json.Marshal([]Item{{UID: progressUID, BSGID: progressItemID, Name: "LEDX Skin Transilluminator", Img: "https://evil.invalid/image"}})
		return response(200, string(body)), nil
	})
	for range 2 {
		item, err := client.LookupItem(context.Background(), "private-fixture-key", progressUID, "PVE", "RU")
		if err != nil || item.UID != progressUID || item.BSGID != progressItemID || item.Img != "" {
			t.Fatalf("wrong identity lookup: %#v, %v", item, err)
		}
	}
	entries, err := os.ReadDir(client.cacheDir)
	if calls != 2 || err != nil || len(entries) != 0 {
		t.Fatalf("identity lookup used or wrote cache: calls=%d, files=%v, err=%v", calls, entries, err)
	}
}

func TestLookupRejectsMissingOrDifferentItemIdentity(t *testing.T) {
	for _, test := range []struct {
		body string
		want error
	}{
		{`[]`, ErrItemNotFound},
		{`[{"uid":"a7f7568c-61fb-437e-9f71-06e58aae26ba","name":"LEDX"}]`, ErrInvalidCatalog},
		{`[{"uid":"another-uid","bsgId":"5c0530ee86f774697952d952","name":"LEDX"}]`, ErrInvalidCatalog},
		{`{"error":"private-fixture-key"}`, ErrInvalidCatalog},
	} {
		client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, test.body), nil })
		_, err := client.LookupItem(context.Background(), "key", progressUID, "pvp", "en")
		if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private-fixture-key") {
			t.Errorf("unsafe item identity accepted: %v, want %v", err, test.want)
		}
	}
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(404, ""), nil })
	if _, err := client.LookupItem(context.Background(), "key", progressUID, "pvp", "en"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("wrong missing UID error: %v", err)
	}
}
