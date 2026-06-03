package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-plugin-autoscan-arr/internal/config"
)

func TestPollChangesReturnsSiloNativePaths(t *testing.T) {
	const apiKey = "secret-key"
	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	body := `[
	  {"eventType":"downloadFolderImported","date":"` + recent + `","data":{"importedPath":"/data/arr/tv/Show/S01/E01.mkv"}},
	  {"eventType":"movieFileRenamed","date":"` + recent + `","data":{"path":"/data/arr/movies/Heat/Heat new.mkv","sourcePath":"/data/arr/movies/Heat/Heat old.mkv"}}
	]`

	var gotKey, gotDate string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotKey = r.Header.Get("X-Api-Key")
		gotDate = r.URL.Query().Get("date")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	rt := &runtimeServer{cfg: &config.Config{Rewrites: []config.Rewrite{
		{From: "/data/arr/tv", To: "/mnt/media/tv"},
		{From: "/data/arr/movies", To: "/mnt/media/movies"},
	}}}
	s := &scanSourceServer{rt: rt}

	resp, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		CapabilityId: "arr",
		Connection:   &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: apiKey},
	})
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}

	if hits == 0 {
		t.Fatal("stub arr server was not hit")
	}
	if gotKey != apiKey {
		t.Fatalf("X-Api-Key = %q, want %q (from connection.api_key)", gotKey, apiKey)
	}
	if gotDate == "" {
		t.Fatal("expected a date query param on the arr request")
	}

	got := append([]string(nil), resp.GetChangedPaths()...)
	sort.Strings(got)
	want := []string{
		"/mnt/media/movies/Heat/Heat new.mkv",
		"/mnt/media/movies/Heat/Heat old.mkv",
		"/mnt/media/tv/Show/S01/E01.mkv",
	}
	if len(got) != len(want) {
		t.Fatalf("ChangedPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ChangedPaths = %v, want %v", got, want)
		}
	}

	if resp.GetNextMarker() == "" {
		t.Fatal("expected a non-empty next_marker")
	}
	if _, err := time.Parse(time.RFC3339, resp.GetNextMarker()); err != nil {
		t.Fatalf("next_marker %q is not RFC3339: %v", resp.GetNextMarker(), err)
	}
}

func TestPollChangesSecondCallUsesReturnedMarker(t *testing.T) {
	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	var dates []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dates = append(dates, r.URL.Query().Get("date"))
		_, _ = w.Write([]byte(`[{"eventType":"downloadFolderImported","date":"` + recent + `","data":{"importedPath":"/data/x.mkv"}}]`))
	}))
	defer srv.Close()

	s := &scanSourceServer{rt: &runtimeServer{cfg: &config.Config{}}}
	conn := &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: "k"}

	first, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{Connection: conn})
	if err != nil {
		t.Fatalf("first PollChanges: %v", err)
	}
	marker := first.GetNextMarker()
	if marker == "" {
		t.Fatal("expected a marker from the first poll")
	}

	second, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: conn,
		Marker:     marker,
	})
	if err != nil {
		t.Fatalf("second PollChanges: %v", err)
	}

	if len(dates) != 2 {
		t.Fatalf("expected 2 arr requests, got %d", len(dates))
	}

	markerTime, err := time.Parse(time.RFC3339, marker)
	if err != nil {
		t.Fatalf("parse first marker %q: %v", marker, err)
	}

	// The QUERY window legitimately rewinds by the overlap buffer so boundary
	// events are not missed.
	queryDate, err := time.Parse(time.RFC3339, dates[1])
	if err != nil {
		t.Fatalf("parse second query date %q: %v", dates[1], err)
	}
	if queryDate.After(markerTime) {
		t.Fatalf("second poll query date %v should be <= marker %v (overlap rewind)", queryDate, markerTime)
	}

	// The RETURNED marker must NOT regress below the caller's marker, even
	// though the query window rewound by the overlap.
	secondMarker, err := time.Parse(time.RFC3339, second.GetNextMarker())
	if err != nil {
		t.Fatalf("parse second marker %q: %v", second.GetNextMarker(), err)
	}
	if secondMarker.Before(markerTime) {
		t.Fatalf("returned marker regressed: %v < caller marker %v", secondMarker, markerTime)
	}
}

func TestPollChangesEmptyPollDoesNotRegressMarker(t *testing.T) {
	// An idle source returns no history. The returned marker must not creep
	// backward by the overlap buffer; it must stay >= the caller's marker.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	s := &scanSourceServer{rt: &runtimeServer{cfg: &config.Config{}}}
	conn := &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: "k"}

	prev := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	resp, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: conn,
		Marker:     prev.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if len(resp.GetChangedPaths()) != 0 {
		t.Fatalf("expected no changed paths, got %v", resp.GetChangedPaths())
	}

	got, err := time.Parse(time.RFC3339, resp.GetNextMarker())
	if err != nil {
		t.Fatalf("parse returned marker %q: %v", resp.GetNextMarker(), err)
	}
	if got.Before(prev) {
		t.Fatalf("empty-poll marker regressed: got %v, want >= caller marker %v (not prev-overlap)", got, prev)
	}
}

func TestPollChangesNilConnectionErrors(t *testing.T) {
	s := &scanSourceServer{rt: &runtimeServer{cfg: &config.Config{}}}

	if _, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{}); err == nil {
		t.Fatal("expected error for nil connection")
	}

	if _, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: &pluginv1.ResolvedConnection{BaseUrl: ""},
	}); err == nil {
		t.Fatal("expected error for empty base url")
	}
}
