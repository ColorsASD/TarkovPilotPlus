package market

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestCachedCatalogRemainsAvailableDuringSlowRefresh(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	client := testClient(t.TempDir(), func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return response(200, sampleCatalog), nil
	})
	old := Catalog{UpdatedAt: time.Now().Add(-time.Hour), Items: []Item{{UID: "old", Name: "Cached item"}}}
	if err := client.writeCache("pvp", "en", old); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() { _, err := client.Load(context.Background(), "test-key", "pvp", "en"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	type result struct {
		catalog Catalog
		ok      bool
	}
	cached := make(chan result, 1)
	go func() { catalog, ok := client.CachedCatalog("pvp", "en"); cached <- result{catalog, ok} }()
	select {
	case r := <-cached:
		if !r.ok || !r.catalog.Stale || len(r.catalog.Items) != 1 || r.catalog.Items[0].Name != "Cached item" {
			t.Fatalf("wrong local catalog: %+v", r)
		}
		r.catalog.Items[0].Name = "caller change"
		again, _ := client.CachedCatalog("pvp", "en")
		if again.Items[0].Name != "Cached item" {
			t.Fatal("cached result aliases client storage")
		}
	case <-time.After(time.Second):
		t.Fatal("local catalog waited for the API")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, ok := client.CachedCatalog("pvp", "en")
	if !ok || final.Stale || final.Items[0].UID != "ledx" {
		t.Fatalf("new catalog was not installed: %+v", final)
	}
}
