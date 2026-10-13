package plus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func installDiagnosticResponse(t *testing.T, status int, body string) *int {
	t.Helper()
	calls := new(int)
	before := http.DefaultTransport
	http.DefaultTransport = diagnosticTransport(func(request *http.Request) (*http.Response, error) {
		*calls++
		if request.Method != http.MethodPatch || request.URL.Host != "api.tarkov-market.app" {
			t.Fatal("diagnostic fixture tried a real or unexpected request")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = before })
	return calls
}

func TestTrackedWriteFailureDiagnosticSurvivesToStateWithoutSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		status             int
	}{
		{"server", `{"error":"private-response-secret"}`, "http(HTTP 503)", 503},
		{"missing changed", `{"ok":true,"revision":"private-ack-revision"}`, "missing_changed", 200},
		{"null changed", `{"ok":true,"changed":null,"revision":"private-ack-revision"}`, "null_changed", 200},
		{"invalid revision", `{"ok":true,"changed":true,"revision":"private unsafe revision"}`, "invalid_revision", 200},
		{"changed mismatch", `{"ok":true,"changed":true,"revision":"private-ack-revision"}`, "none", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newTrackingTestApp(t)
			a.state.Item.Name, fake.snapshot.Items[0].Name = "CMS surgical kit (3/3)", "CMS surgical kit (1/3)"
			fake.snapshot.Revision = "private-before-revision"
			fake.snapshot.Items[0].AvailableQuantity = 1
			a.refreshTracking()
			calls := installDiagnosticResponse(t, tc.status, tc.body)
			client := market.NewClient("")
			fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
				return client.SetTrackedQuantity(context.Background(), "local-test-key", write.Character, write.Item, write.Revision, write.Quantity)
			}
			state := addMarketTrackedItemForTest(a, trackingItemUID, "fir")
			status := state.Tracking.Status
			if *calls != 1 || len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1 || state.Tracking.Error == "" ||
				!strings.Contains(status, "Requested stock: 2; current stock: 1") || !strings.Contains(status, "write="+tc.reason) ||
				!strings.Contains(status, "Selected item: CMS surgical kit (3/3); tracked item: CMS surgical kit (1/3)") {
				t.Fatalf("write cause did not reach the existing state: calls=%d writes=%+v tracking=%+v", *calls, fake.writes, state.Tracking)
			}
			for _, secret := range []string{"local-test-key", "character-a", "private-before-revision", "private-ack-revision", "private-response-secret", "private unsafe revision", "https://"} {
				if strings.Contains(status+state.Tracking.Error, secret) {
					t.Fatalf("diagnostic exposed %q: %s", secret, status)
				}
			}
			if tc.name == "changed mismatch" && (!strings.Contains(status, "ok=true; changed=true") || !strings.Contains(status, "ACK same as before=false; GET matches ACK=false")) {
				t.Fatalf("valid acknowledgment mismatch cannot be distinguished: %s", status)
			}
			if tc.name == "missing changed" && !strings.Contains(status, "ok=true; changed=unknown") {
				t.Fatal("partial safe acknowledgment was lost")
			}
			addMarketTrackedItemForTest(a, trackingItemUID, "fir")
			if *calls != 1 || len(fake.writes) != 1 {
				t.Fatal("diagnostics allowed a repeated write without refresh")
			}
		})
	}
}

func TestHandoverDefiniteFailureKeepsDiagnosticAndExistingCause(t *testing.T) {
	for _, status := range []int{403, 429} {
		a, fake, _ := newSupplementedLocalTestApp(t, trackingItemUID, trackingBSGID)
		addAndSyncCachedTest(t, a, trackingItemUID, "regular")
		addAndSyncCachedTest(t, a, trackingItemUID, "fir")
		calls := installDiagnosticResponse(t, status, `{"error":"private-response-secret"}`)
		client := market.NewClient("")
		fake.writeHook = func(write stockWrite) (market.ProgressWriteResult, error) {
			return client.SetTrackedQuantity(context.Background(), "local-test-key", write.Character, write.Item, write.Revision, write.Quantity)
		}
		handoverDraftForTest(a, handoverRowForTest(trackingBSGID, 1, "regular"))
		state := a.confirmHandoverLegacy()
		requireHandoverPending(t, a, state)
		if *calls != 1 || len(fake.writes) != 3 || !strings.Contains(state.Handover.Error, "write=http(HTTP ") || !strings.Contains(state.Handover.Error, "API: ok=unknown") || strings.Contains(state.Handover.Error, "private-response-secret") {
			t.Fatalf("definite failure category discarded the safe detail: status=%d calls=%d error=%s", status, *calls, state.Handover.Error)
		}
		data, err := os.ReadFile(a.handoverPath("character-a", "PVP"))
		if err != nil || strings.Contains(string(data), "API:") || strings.Contains(string(data), "http(HTTP") {
			t.Fatal("transient diagnostics were persisted into the operation journal")
		}
	}
}

func TestProgressDiagnosticUnknownErrorAndBoundedNames(t *testing.T) {
	feedback := progressWriteDiagnostic(market.ProgressWriteResult{}, errors.New("private-key https://unsafe.invalid private-char private-revision"), "before", "after", nil)
	if !strings.Contains(feedback, "write=unknown") || strings.Contains(feedback, "private-") || strings.Contains(feedback, "https://") {
		t.Fatalf("unknown error text leaked: %s", feedback)
	}
	name := diagnosticItemName(strings.Repeat("Á", 100) + "\n\x00")
	if len([]rune(name)) != 60 || strings.ContainsAny(name, "\n\x00") {
		t.Fatalf("item name was not bounded and sanitized: %q", name)
	}
	a, _ := newTrackingTestApp(t)
	state := addMarketTrackedItemForTest(a, trackingItemUID, "regular")
	if state.Tracking.Status != "Added 1 item to Tracked Items stock." {
		t.Fatalf("normal success was changed by error diagnostics: %s", state.Tracking.Status)
	}
}
