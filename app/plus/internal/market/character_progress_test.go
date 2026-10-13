package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	questsJSON  = `{"charUid":"char-1","gameMode":"SEASON","revision":"revision-1","quests":[{"questId":"quest-1","questIdAliases":["quest-old"],"name":"First quest","trader":"Therapist","completed":true,"status":"completed"},{"questId":"quest-2","questIdAliases":[],"name":"Second quest","trader":"Prapor","completed":false,"status":"locked"}]}`
	hideoutJSON = `{"charUid":"char-1","gameMode":"SEASON","revision":"revision-1","modules":[{"upgradeKey":"Workbench_2","moduleId":"5d484fdf654e7609151d28a2","name":"Workbench","level":2,"completed":false,"prebuilt":false,"canComplete":true},{"upgradeKey":"Stash_1","moduleId":null,"name":"Stash","level":1,"completed":true,"prebuilt":true,"canComplete":false}]}`
)

type characterProgressRead struct {
	name, path, body string
	load             func(*Client, context.Context, string, string, string) (any, error)
}

func characterProgressReads() []characterProgressRead {
	return []characterProgressRead{
		{"quests", "/api/v1/characters/char-1/quests", questsJSON, func(c *Client, ctx context.Context, key, charUID, lang string) (any, error) {
			return c.LoadQuests(ctx, key, charUID, lang)
		}},
		{"hideout", "/api/v1/characters/char-1/hideout", hideoutJSON, func(c *Client, ctx context.Context, key, charUID, lang string) (any, error) {
			return c.LoadHideout(ctx, key, charUID, lang)
		}},
	}
}

func TestCharacterProgressReadsUseDocumentedGETEndpointsAndKeepPrivateDataUncached(t *testing.T) {
	for _, read := range characterProgressReads() {
		t.Run(read.name, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				requireHeaderOnlyKey(t, req)
				if req.Method != http.MethodGet || req.URL.Path != read.path || req.URL.Query().Get("lang") != "hu" ||
					len(req.URL.Query()) != 1 || req.Header.Get("If-Match") != "" || req.Header.Get("Content-Type") != "" || req.ContentLength != 0 {
					t.Fatalf("wrong character progress read: %s %s", req.Method, req.URL)
				}
				return response(200, strings.ReplaceAll(read.body, "revision-1", fmt.Sprintf("revision-%d", calls))), nil
			})
			for iteration := range 2 {
				snapshot, err := read.load(client, context.Background(), "private-fixture-key", "char-1", " HU ")
				if err != nil {
					t.Fatal(err)
				}
				wantRevision := fmt.Sprintf("revision-%d", iteration+1)
				switch value := snapshot.(type) {
				case QuestSnapshot:
					var expected QuestSnapshot
					if err := json.Unmarshal([]byte(strings.ReplaceAll(questsJSON, "revision-1", wantRevision)), &expected); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(value, expected) {
						t.Fatalf("quest state or aliases were altered: %#v", value)
					}
				case HideoutSnapshot:
					var expected HideoutSnapshot
					if err := json.Unmarshal([]byte(strings.ReplaceAll(hideoutJSON, "revision-1", wantRevision)), &expected); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(value, expected) || value.Modules[1].ModuleID != "" {
						t.Fatalf("hideout state or nullable module identity was altered: %#v", value)
					}
				}
			}
			entries, err := os.ReadDir(client.cacheDir)
			if calls != 2 || err != nil || len(entries) != 0 || len(client.catalogs) != 0 {
				t.Fatalf("private progress used the catalog cache: calls=%d, entries=%v, err=%v", calls, entries, err)
			}
		})
	}
}

func TestCharacterProgressAllowsEmptyListsInEveryDocumentedMode(t *testing.T) {
	for _, read := range characterProgressReads() {
		for _, mode := range []string{"PVP", "PVE", "SEASON"} {
			t.Run(read.name+"/"+mode, func(t *testing.T) {
				list := "quests"
				if read.name == "hideout" {
					list = "modules"
				}
				body := fmt.Sprintf(`{"charUid":"char-1","gameMode":%q,"revision":"revision-1",%q:[]}`, mode, list)
				client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
					if req.URL.Query().Get("lang") != "en" {
						t.Fatal("missing locale did not default to en")
					}
					return response(200, body), nil
				})
				if _, err := read.load(client, context.Background(), "key", "char-1", ""); err != nil {
					t.Fatalf("valid empty %s snapshot rejected: %v", mode, err)
				}
			})
		}
	}
}

func TestQuestAliasesMayBeAbsentAndStatusIsPreserved(t *testing.T) {
	for _, status := range []string{"completed", "failed", "unreachable", "available", "locked"} {
		client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
			return response(200, fmt.Sprintf(`{"charUid":"char-1","gameMode":"PVP","revision":"revision-1","quests":[{"questId":"quest-1","completed":%t,"status":%q}]}`, status == "completed", status)), nil
		})
		snapshot, err := client.LoadQuests(context.Background(), "key", "char-1", "en")
		if err != nil || snapshot.Quests[0].Status != status || snapshot.Quests[0].Completed != (status == "completed") || snapshot.Quests[0].QuestIDAliases != nil {
			t.Fatalf("documented quest status or absent aliases changed: %#v, %v", snapshot, err)
		}
	}
}

func TestCharacterProgressRejectsInvalidInputBeforeHTTP(t *testing.T) {
	for _, read := range characterProgressReads() {
		t.Run(read.name, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, read.body), nil
			})
			for _, test := range []struct {
				key, charUID, lang string
				want               error
			}{
				{"", "char-1", "en", ErrAPIKeyRequired},
				{"private-fixture-key\r\nx-extra: bad", "char-1", "en", ErrUnauthorized},
				{"key", "", "en", ErrProgressInvalidRequest},
				{"key", "../another", "en", ErrProgressInvalidRequest},
				{"key", "char-1?mode=pve", "en", ErrProgressInvalidRequest},
				{"key", strings.Repeat("x", 129), "en", ErrProgressInvalidRequest},
			} {
				_, err := read.load(client, context.Background(), test.key, test.charUID, test.lang)
				if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private-fixture-key") {
					t.Fatalf("invalid input returned unsafe error: %v, want %v", err, test.want)
				}
			}
			if _, err := read.load(client, context.Background(), "key", "char-1", "unsupported"); err == nil {
				t.Fatal("unsupported locale accepted")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := read.load(client, ctx, "key", "char-1", "en"); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled read returned %v", err)
			}
			if calls != 0 {
				t.Fatalf("invalid or cancelled input made %d requests", calls)
			}
		})
	}
}

func TestCharacterProgressRejectsInvalidSnapshotEnvelopes(t *testing.T) {
	for _, read := range characterProgressReads() {
		list := "quests"
		if read.name == "hideout" {
			list = "modules"
		}
		for _, test := range []struct{ name, body string }{
			{"other-character", strings.ReplaceAll(read.body, `"charUid":"char-1"`, `"charUid":"char-2"`)},
			{"invalid-mode", strings.ReplaceAll(read.body, `"gameMode":"SEASON"`, `"gameMode":"pvp"`)},
			{"empty-revision", strings.ReplaceAll(read.body, `"revision":"revision-1"`, `"revision":""`)},
			{"unsafe-revision", strings.ReplaceAll(read.body, `"revision":"revision-1"`, `"revision":"revision\nprivate-fixture-key"`)},
			{"missing-list", `{"charUid":"char-1","gameMode":"PVP","revision":"revision-1"}`},
			{"null-list", fmt.Sprintf(`{"charUid":"char-1","gameMode":"PVP","revision":"revision-1",%q:null}`, list)},
			{"private-error-body", `{"error":"private-fixture-key"}`},
			{"invalid-json", `private-fixture-key`},
		} {
			t.Run(read.name+"/"+test.name, func(t *testing.T) {
				client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, test.body), nil })
				snapshot, err := read.load(client, context.Background(), "key", "char-1", "en")
				if !errors.Is(err, ErrInvalidProgress) || strings.Contains(err.Error(), "private-fixture-key") || !reflect.ValueOf(snapshot).IsZero() {
					t.Fatalf("invalid private response accepted or partially returned: %#v, %v", snapshot, err)
				}
			})
		}
	}
}

func TestQuestsRejectInvalidOrAmbiguousIdentities(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"invalid-id", strings.ReplaceAll(questsJSON, `"questId":"quest-1"`, `"questId":"../private-fixture-key"`)},
		{"duplicate-id", strings.ReplaceAll(questsJSON, `"questId":"quest-2"`, `"questId":"quest-1"`)},
		{"invalid-alias", strings.ReplaceAll(questsJSON, `"quest-old"`, `"private-fixture-key?"`)},
		{"alias-is-other-primary-id", strings.ReplaceAll(questsJSON, `"quest-old"`, `"quest-2"`)},
		{"alias-shared-by-two-quests", strings.ReplaceAll(questsJSON, `"questIdAliases":[]`, `"questIdAliases":["quest-old"]`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, test.body), nil })
			if _, err := client.LoadQuests(context.Background(), "key", "char-1", "en"); !errors.Is(err, ErrInvalidProgress) || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("invalid quest identity accepted: %v", err)
			}
		})
	}
}

func TestHideoutRejectsInvalidUpgradeIdentitiesAndLevels(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"empty-upgrade", strings.ReplaceAll(hideoutJSON, `"upgradeKey":"Workbench_2"`, `"upgradeKey":""`)},
		{"whitespace-upgrade", strings.ReplaceAll(hideoutJSON, `"upgradeKey":"Workbench_2"`, `"upgradeKey":"   "`)},
		{"control-upgrade", strings.ReplaceAll(hideoutJSON, `"upgradeKey":"Workbench_2"`, `"upgradeKey":"private-fixture-key\t_2"`)},
		{"null-upgrade", strings.ReplaceAll(hideoutJSON, `"upgradeKey":"Workbench_2"`, `"upgradeKey":"private-fixture-key\u0000_2"`)},
		{"invalid-utf8-upgrade", strings.ReplaceAll(hideoutJSON, "Workbench_2", string([]byte{'W', 0xff, '_', '2'}))},
		{"duplicate-upgrade", strings.ReplaceAll(hideoutJSON, `"upgradeKey":"Stash_1"`, `"upgradeKey":"Workbench_2"`)},
		{"invalid-module", strings.ReplaceAll(hideoutJSON, `"moduleId":null`, `"moduleId":"private-fixture-key?"`)},
		{"negative-level", strings.ReplaceAll(hideoutJSON, `"level":2`, `"level":-2`)},
		{"zero-level", strings.ReplaceAll(hideoutJSON, `"level":2`, `"level":0`)},
		{"fractional-level", strings.ReplaceAll(hideoutJSON, `"level":2`, `"level":1.5`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, test.body), nil })
			if _, err := client.LoadHideout(context.Background(), "key", "char-1", "en"); !errors.Is(err, ErrInvalidProgress) || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("invalid hideout identity or level accepted: %v", err)
			}
		})
	}
}

func TestHideoutKeepsSpaceContainingAndUnicodeUpgradeKeys(t *testing.T) {
	expected := HideoutSnapshot{CharUID: "char-1", GameMode: "SEASON", Revision: "revision-1", Modules: []HideoutUpgrade{
		{UpgradeKey: "Water Collector_1", Name: "Water Collector", Level: 1, CanComplete: true},
		{UpgradeKey: "Hall of Fame_2", Name: "Hall of Fame", Level: 2},
		{UpgradeKey: "Rest Space_1", Name: "Rest Space", Level: 1, Completed: true, Prebuilt: true},
		{UpgradeKey: "Víztisztító_1", Name: "Víztisztító", Level: 1},
	}}
	body, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
		requireHeaderOnlyKey(t, req)
		if req.Method != http.MethodGet || req.URL.Path != "/api/v1/characters/char-1/hideout" || req.URL.Query().Has("upgradeKey") {
			t.Fatal("opaque upgrade keys changed the read request")
		}
		return response(200, string(body)), nil
	})
	snapshot, err := client.LoadHideout(context.Background(), "private-fixture-key", "char-1", "en")
	if err != nil || !reflect.DeepEqual(snapshot, expected) {
		t.Fatalf("valid Market upgrade names rejected or changed: %#v, %v", snapshot, err)
	}
}

func TestUpgradeKeysAreBoundedOpaqueNamesWithoutControlCharacters(t *testing.T) {
	for _, test := range []struct {
		name, key string
		valid     bool
	}{
		{"space", "Water Collector_1", true},
		{"unicode", "Pihenőtér_1", true},
		{"opaque-punctuation", "Module / room?_1", true},
		{"preserved-surrounding-space", " Hall of Fame_2 ", true},
		{"maximum-bytes", strings.Repeat("a", 128), true},
		{"maximum-unicode-bytes", strings.Repeat("ű", 64), true},
		{"empty", "", false},
		{"space-only", "   ", false},
		{"unicode-space-only", "\u00a0\u2003", false},
		{"too-long", strings.Repeat("a", 129), false},
		{"too-many-unicode-bytes", strings.Repeat("ű", 65), false},
		{"tab", "Water\tCollector_1", false},
		{"newline", "Water\nCollector_1", false},
		{"carriage-return", "Water\rCollector_1", false},
		{"null", "Water\x00Collector_1", false},
		{"delete", "Water\x7fCollector_1", false},
		{"unicode-control", "Water\u0085Collector_1", false},
		{"unicode-line-separator", "Water\u2028Collector_1", false},
		{"unicode-paragraph-separator", "Water\u2029Collector_1", false},
		{"invalid-utf8", string([]byte{'W', 0xff, '_', '1'}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if validUpgradeKey(test.key) != test.valid {
				t.Fatalf("wrong upgrade key validity for %q: want %v", test.key, test.valid)
			}
		})
	}
}

func TestCharacterProgressUsesExistingSanitizedErrorsAndRefusesRedirects(t *testing.T) {
	for _, read := range characterProgressReads() {
		for status, want := range map[int]error{
			400: ErrProgressInvalidRequest, 401: ErrUnauthorized, 403: ErrProgressForbidden,
			404: ErrProgressNotFound, 409: ErrProgressConflict, 428: ErrProgressRevisionRequired,
			429: ErrRateLimited, 503: ErrProgressUnavailable,
		} {
			t.Run(fmt.Sprintf("%s/http-%d", read.name, status), func(t *testing.T) {
				client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
					return response(status, `{"error":"private-fixture-key"}`), nil
				})
				if _, err := read.load(client, context.Background(), "private-fixture-key", "char-1", "en"); !errors.Is(err, want) || errors.Is(err, ErrProgressWriteUncertain) || strings.Contains(err.Error(), "private-fixture-key") {
					t.Fatalf("wrong or unsafe read error: %v, want %v", err, want)
				}
			})
		}
		t.Run(read.name+"/transport-error", func(t *testing.T) {
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("private-fixture-key") })
			if _, err := read.load(client, context.Background(), "private-fixture-key", "char-1", "en"); !errors.Is(err, ErrProgressUnavailable) || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("transport error leaked private data: %v", err)
			}
		})
		t.Run(read.name+"/redirect", func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != read.path || calls > 1 {
					t.Fatal("private read followed redirect")
				}
				result := response(302, `{"error":"private-fixture-key"}`)
				result.Header.Set("Location", "https://evil.invalid/private-fixture-key")
				return result, nil
			})
			client.http.CheckRedirect = nil
			_, err := read.load(client, context.Background(), "private-fixture-key", "char-1", "en")
			if err == nil || calls != 1 || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("redirect accepted or private key leaked: %v, calls=%d", err, calls)
			}
		})
	}
}

func TestCharacterProgressReadBackoffAlsoAppliesToTrackedItems(t *testing.T) {
	for _, read := range characterProgressReads() {
		t.Run(read.name, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				result := response(429, "")
				result.Header.Set("Retry-After", "60")
				return result, nil
			})
			if _, err := read.load(client, context.Background(), "key", "char-1", "en"); !errors.Is(err, ErrRateLimited) {
				t.Fatalf("wrong progress rate-limit error: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if _, err := client.LoadTracked(ctx, "key", "char-1", "en"); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
				t.Fatalf("character endpoint lost shared progress backoff: %v, calls=%d", err, calls)
			}
		})
	}
}
