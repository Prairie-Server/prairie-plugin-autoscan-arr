package arr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func TestChangedPaths(t *testing.T) {
	// imports contribute importedPath; renames contribute both new path and old
	// sourcePath; unrelated events (grabbed, episodeFileDeleted) are ignored.
	// Use recent timestamps (within the lookback floor) so the newest history
	// timestamp drives the returned marker rather than the floor clamp.
	newestEvent := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	d := func(offset time.Duration) string {
		return newestEvent.Add(offset).Format(time.RFC3339)
	}
	body := `[
	  {"eventType":"downloadFolderImported","date":"` + d(-4*time.Minute) + `","data":{"importedPath":"/mnt/media/Movies/Dune (2021)/Dune.mkv"}},
	  {"eventType":"grabbed","date":"` + d(-3*time.Minute) + `","data":{"importedPath":"/should/be/ignored"}},
	  {"eventType":"episodeFileRenamed","date":"` + d(-2*time.Minute) + `","data":{"path":"/mnt/media/Show/S01/E01 new.mkv","sourcePath":"/mnt/media/Show/S01/E01 old.mkv"}},
	  {"eventType":"movieFileRenamed","date":"` + d(-1*time.Minute) + `","data":{"path":"/mnt/media/Movies/Heat/Heat new.mkv","sourcePath":"/mnt/media/Movies/Heat/Heat old.mkv"}},
	  {"eventType":"episodeFileDeleted","date":"` + d(0) + `","data":{"reason":"Upgrade"}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/history/since" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("date") == "" {
			t.Errorf("missing date param")
		}
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("missing/incorrect api key header")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	paths, newest, err := ChangedPaths(context.Background(), srv.URL, "k", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	sort.Strings(paths)
	want := []string{
		"/mnt/media/Movies/Dune (2021)/Dune.mkv",
		"/mnt/media/Movies/Heat/Heat new.mkv",
		"/mnt/media/Movies/Heat/Heat old.mkv",
		"/mnt/media/Show/S01/E01 new.mkv",
		"/mnt/media/Show/S01/E01 old.mkv",
	}
	if len(paths) != len(want) {
		t.Fatalf("ChangedPaths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("ChangedPaths = %v, want %v", paths, want)
		}
	}
	// newest should track the most recent history timestamp seen (the deleted
	// event at offset 0 is the latest date even though its path is ignored).
	if !newest.Equal(newestEvent) {
		t.Fatalf("newest = %v, want %v", newest, newestEvent)
	}
}

func TestChangedPathsClampsLookbackToFloor(t *testing.T) {
	// A since far in the past must be clamped to ~now-24h (minus overlap), not
	// replay the entire history.
	var gotDate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDate = r.URL.Query().Get("date")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	_, _, err := ChangedPaths(context.Background(), srv.URL, "k", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, gotDate)
	if err != nil {
		t.Fatalf("parse date %q: %v", gotDate, err)
	}
	floor := time.Now().UTC().Add(-maxLookback - overlap)
	if parsed.Before(floor.Add(-time.Minute)) {
		t.Fatalf("date %v is older than the lookback floor %v", parsed, floor)
	}
}

func TestChangedPathsEmptyMarkerUsesNow(t *testing.T) {
	var gotDate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDate = r.URL.Query().Get("date")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	before := time.Now().UTC().Add(-overlap - time.Minute)
	_, newest, err := ChangedPaths(context.Background(), srv.URL, "k", time.Time{})
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, gotDate)
	if err != nil {
		t.Fatalf("parse date %q: %v", gotDate, err)
	}
	// Empty marker => poll from ~now (minus overlap).
	if parsed.Before(before) {
		t.Fatalf("empty-marker date %v older than expected ~now %v", parsed, before)
	}
	if newest.IsZero() {
		t.Fatal("newest should not be zero")
	}
}
