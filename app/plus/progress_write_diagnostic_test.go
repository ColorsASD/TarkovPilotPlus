package plus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func installDiagnosticResponse(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	calls := new(atomic.Int32)
	before := http.DefaultTransport
	http.DefaultTransport = diagnosticTransport(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
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
		pending            int
	}{
		{"forbidden", `{"error":"private-response-secret"}`, "http(HTTP 403)", 403, 0},
		{"missing changed", `{"ok":true,"revision":"private-ack-revision"}`, "missing_changed", 200, 1},
		{"null changed", `{"ok":true,"changed":null,"revision":"private-ack-revision"}`, "null_changed", 200, 1},
		{"invalid revision", `{"ok":true,"changed":true,"revision":"private unsafe revision"}`, "invalid_revision", 200, 1},
		{"changed mismatch", `{"ok":true,"changed":true,"revision":"private-ack-revision"}`, "none", 200, 1},
		// Az 503 a kliens kozos keresi korlatjan backoffot allit be.
		// Utolsokent mar nem varakoztatja masik fixture HTTP-kereset.
		{"server", `{"error":"private-response-secret"}`, "http(HTTP 503)", 503, 1},
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
			a.AddTrackedItemFIR(trackingItemUID)
			state := waitPendingForTest(t, a)
			status := state.Tracking.Status
			if calls.Load() != 1 || len(fake.writes) != 1 || state.Tracking.AvailableQuantity != 1+int64(tc.pending) || state.Tracking.MarketAvailableQuantity != 1 ||
				state.PendingAdds == nil || state.PendingAdds.Count != tc.pending || state.PendingAdds.Running || state.PendingAdds.Error == "" || state.Tracking.Error != "" ||
				!strings.Contains(status, "Requested stock: 2; current stock: 1") || !strings.Contains(status, "write="+tc.reason) ||
				!strings.Contains(status, "Selected item: CMS surgical kit (3/3); tracked item: CMS surgical kit (1/3)") {
				t.Fatalf("write cause or pending reservation was lost: calls=%d writes=%+v tracking=%+v pending=%+v", calls.Load(), fake.writes, state.Tracking, state.PendingAdds)
			}
			for _, secret := range []string{"local-test-key", "character-a", "private-before-revision", "private-ack-revision", "private-response-secret", "private unsafe revision", "https://"} {
				if strings.Contains(status+state.Tracking.Error+state.PendingAdds.Error, secret) {
					t.Fatalf("diagnostic exposed %q: %s", secret, status)
				}
			}
			if tc.name == "changed mismatch" && (!strings.Contains(status, "ok=true; changed=true") || !strings.Contains(status, "ACK same as before=false; GET matches ACK=false")) {
				t.Fatalf("valid acknowledgment mismatch cannot be distinguished: %s", status)
			}
			if tc.name == "missing changed" && !strings.Contains(status, "ok=true; changed=unknown") {
				t.Fatal("partial safe acknowledgment was lost")
			}
			a.RecheckPendingAdds()
			state = waitPendingForTest(t, a)
			if calls.Load() != 1 || len(fake.writes) != 1 || state.PendingAdds.Count != tc.pending {
				t.Fatal("diagnostic recheck repeated the stock write or discarded the unresolved reservation")
			}
		})
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
