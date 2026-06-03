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

	if _, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: conn,
		Marker:     marker,
	}); err != nil {
		t.Fatalf("second PollChanges: %v", err)
	}

	if len(dates) != 2 {
		t.Fatalf("expected 2 arr requests, got %d", len(dates))
	}
	// The second poll's date must derive from the marker (minus the overlap
	// buffer), i.e. close to the 2026-06-01T10:00:00Z history timestamp.
	second, err := time.Parse(time.RFC3339, dates[1])
	if err != nil {
		t.Fatalf("parse second date %q: %v", dates[1], err)
	}
	markerTime, _ := time.Parse(time.RFC3339, marker)
	if second.After(markerTime) {
		t.Fatalf("second poll date %v should be <= marker %v (overlap rewind)", second, markerTime)
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
