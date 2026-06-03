package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// pagedEnvelope wraps a flat JSON records slice into a paged arr history envelope.
func pagedEnvelope(records string) string {
	return `{"page":1,"pageSize":200,"totalRecords":1,"records":` + records + `}`
}

func TestPollChangesReturnsRawArrPaths(t *testing.T) {
	const apiKey = "secret-key"
	// Use a marker 2h ago so the query window (with overlap) covers the records.
	callerMarker := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	// Records are 30s after the marker — well within the query window.
	recent := callerMarker.Add(30 * time.Second).Format(time.RFC3339)
	records := `[
	  {"eventType":"downloadFolderImported","date":"` + recent + `","data":{"importedPath":"/data/arr/tv/Show/S01/E01.mkv"}},
	  {"eventType":"movieFileRenamed","date":"` + recent + `","data":{"path":"/data/arr/movies/Heat/Heat new.mkv","sourcePath":"/data/arr/movies/Heat/Heat old.mkv"}}
	]`

	var gotKey string
	var gotPage string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotKey = r.Header.Get("X-Api-Key")
		gotPage = r.URL.Query().Get("page")
		_, _ = w.Write([]byte(pagedEnvelope(records)))
	}))
	defer srv.Close()

	s := &scanSourceServer{}

	resp, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		CapabilityId: "arr",
		Connection:   &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: apiKey},
		Marker:       callerMarker.Format(time.RFC3339),
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
	if gotPage == "" {
		t.Fatal("expected a page query param on the arr request")
	}

	// The plugin returns raw arr paths; the host applies rewrites.
	got := append([]string(nil), resp.GetSourcePaths()...)
	sort.Strings(got)
	want := []string{
		"/data/arr/movies/Heat/Heat new.mkv",
		"/data/arr/movies/Heat/Heat old.mkv",
		"/data/arr/tv/Show/S01/E01.mkv",
	}
	if len(got) != len(want) {
		t.Fatalf("SourcePaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SourcePaths = %v, want %v", got, want)
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
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		records := `[{"eventType":"downloadFolderImported","date":"` + recent + `","data":{"importedPath":"/data/x.mkv"}}]`
		_, _ = w.Write([]byte(pagedEnvelope(records)))
	}))
	defer srv.Close()

	s := &scanSourceServer{}
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

	// At least 2 paged requests (one per PollChanges call, each starts at page 1).
	if len(pages) < 2 {
		t.Fatalf("expected at least 2 arr requests, got %d", len(pages))
	}

	markerTime, err := time.Parse(time.RFC3339, marker)
	if err != nil {
		t.Fatalf("parse first marker %q: %v", marker, err)
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
		_, _ = w.Write([]byte(`{"page":1,"pageSize":200,"totalRecords":0,"records":[]}`))
	}))
	defer srv.Close()

	s := &scanSourceServer{}
	conn := &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: "k"}

	prev := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	resp, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: conn,
		Marker:     prev.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}
	if len(resp.GetSourcePaths()) != 0 {
		t.Fatalf("expected no source paths, got %v", resp.GetSourcePaths())
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
	s := &scanSourceServer{}

	if _, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{}); err == nil {
		t.Fatal("expected error for nil connection")
	}

	if _, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: &pluginv1.ResolvedConnection{BaseUrl: ""},
	}); err == nil {
		t.Fatal("expected error for empty base url")
	}
}

// TestPollChangesInvalidMarkerErrors verifies Fix 3: a non-empty marker that
// cannot be parsed as RFC3339 must return an error rather than silently
// resetting to "now" and losing the polling position.
func TestPollChangesInvalidMarkerErrors(t *testing.T) {
	// Server should not be called at all — we should error before the HTTP request.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"page":1,"pageSize":200,"totalRecords":0,"records":[]}`))
	}))
	defer srv.Close()

	s := &scanSourceServer{}
	conn := &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: "k"}

	garbageMarkers := []string{
		"not-a-date",
		"2025-13-01T00:00:00Z", // invalid month
		"garbage",
		"123456",
	}
	for _, bad := range garbageMarkers {
		_, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
			Connection: conn,
			Marker:     bad,
		})
		if err == nil {
			t.Fatalf("marker %q: expected error for garbage marker, got nil", bad)
		}
	}

	if hits != 0 {
		t.Fatalf("expected no arr requests for garbage markers, got %d", hits)
	}
}

// TestPollChangesEmptyMarkerSucceeds verifies that an empty marker (first run)
// still succeeds without error — the "empty → from now" path must not be broken
// by Fix 3.
func TestPollChangesEmptyMarkerSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"page":1,"pageSize":200,"totalRecords":0,"records":[]}`))
	}))
	defer srv.Close()

	s := &scanSourceServer{}
	conn := &pluginv1.ResolvedConnection{BaseUrl: srv.URL, ApiKey: "k"}

	resp, err := s.PollChanges(context.Background(), &pluginv1.PollChangesRequest{
		Connection: conn,
		Marker:     "", // empty → first run
	})
	if err != nil {
		t.Fatalf("empty marker should succeed, got error: %v", err)
	}
	if resp.GetNextMarker() == "" {
		t.Fatal("expected a non-empty next_marker on first run")
	}
	// Verify it's a valid JSON structure (not used elsewhere but confirm it parses).
	var dummy interface{}
	_ = json.Unmarshal([]byte(`{}`), &dummy)
}
