package market

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestProgressWriteSchemaDiagnosticPreservesControlledReason(t *testing.T) {
	for _, tc := range []struct{ body, reason, ok, changed string }{
		{`private-fixture-key`, "invalid_json", "unknown", "unknown"},
		{`{"ok":true,"revision":"revision-2"}`, "missing_changed", "true", "unknown"},
		{`{"ok":true,"changed":null,"revision":"revision-2"}`, "null_changed", "true", "unknown"},
		{`{"ok":true,"changed":"private-fixture-key","revision":"revision-2"}`, "invalid_changed", "true", "unknown"},
		{`{"ok":true,"changed":true,"revision":"private-fixture-key with unsafe spaces"}`, "invalid_revision", "true", "true"},
		{`{"ok":true,"changed":true,"revision":123}`, "invalid_revision", "true", "true"},
		{`{"ok":false,"changed":false,"revision":"revision-2"}`, "not_ok", "false", "false"},
		{`{"ok":"private-fixture-key","changed":true,"revision":"revision-2"}`, "not_ok", "unknown", "true"},
	} {
		t.Run(tc.reason+tc.changed, func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, tc.body), nil
			})
			_, err := client.SetTrackedQuantity(context.Background(), "private-fixture-key", "char-1", progressItemID, "revision-1", 2)
			info := ProgressWriteFailure(err)
			boolLabel := func(value *bool) string {
				if value == nil {
					return "unknown"
				}
				return fmt.Sprint(*value)
			}
			if !errors.Is(err, ErrProgressWriteUncertain) || !errors.Is(err, ErrInvalidProgress) || calls != 1 || info.Reason != tc.reason ||
				boolLabel(info.ACKOK) != tc.ok || boolLabel(info.ACKChanged) != tc.changed || info.HTTPStatus != 0 || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatalf("write schema cause was lost, retried or leaked: info=%+v err=%v calls=%d", info, err, calls)
			}
		})
	}
}

func TestProgressWriteHTTPDiagnosticContainsOnlyStatus(t *testing.T) {
	for _, status := range []int{400, 401, 403, 409, 413, 418, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(status, `{"error":"private-fixture-key char-1 private-revision"}`), nil
			})
			_, err := client.SetTrackedQuantity(context.Background(), "private-fixture-key", "char-1", progressItemID, "revision-1", 2)
			info := ProgressWriteFailure(err)
			if info.Reason != "http" || info.HTTPStatus != status || calls != 1 || info.ACKOK != nil || info.ACKChanged != nil || strings.Contains(err.Error(), "private-fixture-key") || strings.Contains(err.Error(), "char-1") {
				t.Fatalf("HTTP diagnostic lost status or exposed body: info=%+v err=%v calls=%d", info, err, calls)
			}
		})
	}
}

func TestProgressWriteFailureNeverFormatsUnknownError(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{errors.New("private-fixture-key https://unsafe.invalid/?char=private-character"), "unknown"},
		{errors.Join(ErrProgressWriteUncertain, context.DeadlineExceeded), "timeout"},
		{errors.Join(ErrProgressWriteUncertain, context.Canceled), "canceled"},
		{ErrProgressWriteUncertain, "uncertain"},
		{ErrProgressPayloadTooLarge, "payload_too_large"},
	} {
		if info := ProgressWriteFailure(tc.err); info.Reason != tc.reason || info.HTTPStatus != 0 || info.ACKOK != nil || info.ACKChanged != nil {
			t.Fatalf("uncontrolled error was not reduced to a safe category: %+v", info)
		}
	}
	client := progressTestClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private-fixture-key https://unsafe.invalid/?char=private-character")
	})
	_, err := client.SetTrackedQuantity(context.Background(), "private-fixture-key", "char-1", progressItemID, "revision-1", 2)
	if info := ProgressWriteFailure(err); info.Reason != "network" || !errors.Is(err, ErrProgressWriteUncertain) || !errors.Is(err, ErrProgressUnavailable) || strings.Contains(err.Error(), "private-fixture-key") {
		t.Fatalf("transport error was exposed or lost: info=%+v err=%v", info, err)
	}
}
