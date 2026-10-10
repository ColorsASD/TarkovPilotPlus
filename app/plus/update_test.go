package plus

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

type manualUpdateTransport struct{ requests *atomic.Int32 }

func (t manualUpdateTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.requests.Add(1)
	return nil, errors.New("unexpected update request blocked by test")
}

func TestUpdateCannotDownloadAnUpstreamExecutable(t *testing.T) {
	var requests atomic.Int32
	// Block every destination if the original downloader is accidentally wired
	// into the Plus policy. No test request can reach the network.
	previousTransport, previousClient := http.DefaultTransport, http.DefaultClient
	transport := manualUpdateTransport{requests: &requests}
	http.DefaultTransport = transport
	http.DefaultClient = &http.Client{Transport: transport}
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
		http.DefaultClient = previousClient
	})
	for _, host := range []string{"127.0.0.1:1", "localhost:1", "tarkov-market.com", "tarkov-market.ru", ""} {
		if err := Update(host); !errors.Is(err, ErrManualUpdateRequired) {
			t.Fatalf("Update(%q) returned %v", host, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("manual updater sent a download request")
	}
}

func TestForkUpdatesUseOnlyItsOwnReleasePage(t *testing.T) {
	if AutomaticUpdatesEnabled {
		t.Fatal("upstream automatic updates must remain disabled in Plus")
	}
	if ReleasesURL != "https://github.com/ColorsASD/TarkovPilotPlus/releases" {
		t.Fatal("manual update link is not the Plus release page")
	}
}
