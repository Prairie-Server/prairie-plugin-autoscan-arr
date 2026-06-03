package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// pagedBody is a helper that wraps records into a paged arr history envelope.
func pagedBody(page, pageSize, totalRecords int, records []historyRecord) string {
	type envelope struct {
		Page         int             `json:"page"`
		PageSize     int             `json:"pageSize"`
		TotalRecords int             `json:"totalRecords"`
		Records      []historyRecord `json:"records"`
	}
	b, _ := json.Marshal(envelope{Page: page, PageSize: pageSize, TotalRecords: totalRecords, Records: records})
	return string(b)
}

// TestChangedPathsFutureDateMarker verifies that a caller marker in the future
// (e.g. from host/arr clock skew) is clamped to <= now so the query window
// does not sit in the future. The paged request must still reach the stub (the
// query is not skipped), and the returned marker must not be in the future.
func TestChangedPathsFutureDateMarker(t *testing.T) {
	now := time.Now().UTC()

	var serverHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHit = true
		if r.URL.Query().Get("page") == "" {
			t.Errorf("missing page param on paged history request")
		}
		// Return empty records — we only care that the request was made and marker
		// does not propagate into the future.
		_, _ = w.Write([]byte(pagedBody(1, historyPageSize, 0, nil)))
	}))
	defer srv.Close()

	// Caller marker is 2 hours in the future (simulating clock skew).
	futureMarker := now.Add(2 * time.Hour)
	_, newest, err := ChangedPaths(context.Background(), srv.URL, "k", futureMarker)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}

	if !serverHit {
		t.Fatal("stub arr server was not hit; future marker must not suppress the poll")
	}

	// The returned marker must not be in the future.
	if newest.After(now.Add(time.Minute)) {
		t.Fatalf("returned marker %v is in the future (now=%v); future marker was not clamped", newest, now)
	}
}

func TestChangedPaths(t *testing.T) {
	// imports contribute importedPath; renames contribute both new path and old
	// sourcePath; unrelated events (grabbed, episodeFileDeleted) are ignored.
	// Use recent timestamps (within the lookback floor) so the newest history
	// timestamp drives the returned marker rather than the floor clamp.
	newestEvent := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	d := func(offset time.Duration) time.Time {
		return newestEvent.Add(offset)
	}
	records := []historyRecord{
		// date-descending order (as arr returns them)
		{EventType: "episodeFileDeleted", Date: d(0)},
		{EventType: eventMovieRenamed, Date: d(-1 * time.Minute), Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{Path: "/mnt/media/Movies/Heat/Heat new.mkv", SourcePath: "/mnt/media/Movies/Heat/Heat old.mkv"}},
		{EventType: eventEpisodeRenamed, Date: d(-2 * time.Minute), Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{Path: "/mnt/media/Show/S01/E01 new.mkv", SourcePath: "/mnt/media/Show/S01/E01 old.mkv"}},
		{EventType: "grabbed", Date: d(-3 * time.Minute), Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{ImportedPath: "/should/be/ignored"}},
		{EventType: eventImported, Date: d(-4 * time.Minute), Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{ImportedPath: "/mnt/media/Movies/Dune (2021)/Dune.mkv"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/history" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("page") == "" {
			t.Errorf("missing page param")
		}
		if r.URL.Query().Get("sortKey") != "date" {
			t.Errorf("missing/incorrect sortKey param")
		}
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("missing/incorrect api key header")
		}
		// Return fewer records than a full page so paging stops.
		body := pagedBody(1, historyPageSize, len(records), records)
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
	var gotPage1 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			gotPage1 = true
		}
		_, _ = w.Write([]byte(pagedBody(1, historyPageSize, 0, nil)))
	}))
	defer srv.Close()

	_, _, err := ChangedPaths(context.Background(), srv.URL, "k", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if !gotPage1 {
		t.Fatal("expected at least one paginated history request")
	}
}

func TestChangedPathsEmptyMarkerUsesNow(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte(pagedBody(1, historyPageSize, 0, nil)))
	}))
	defer srv.Close()

	_, newest, err := ChangedPaths(context.Background(), srv.URL, "k", time.Time{})
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if !hit {
		t.Fatal("expected arr to be called")
	}
	// Empty marker => poll from ~now (minus overlap).
	if newest.IsZero() {
		t.Fatal("newest should not be zero")
	}
}

// TestChangedPathsPaginates verifies that ChangedPaths pages through multiple
// pages of arr history, collects paths from all pages, and stops paging once a
// record with date <= querySince is encountered (date-descending order).
func TestChangedPathsPaginates(t *testing.T) {
	now := time.Now().UTC()
	// Caller marker: 90 minutes ago.
	callerMarker := now.Add(-90 * time.Minute).Truncate(time.Second)
	// querySince = callerMarker (clamped) minus overlap (1m) => ~91m ago.
	querySince := callerMarker.Add(-overlap)

	// Build a full page of historyPageSize records, all at now-30m (well within window).
	// This ensures the "len < historyPageSize" shortcut does NOT stop paging after page 1.
	fullPage1 := make([]historyRecord, historyPageSize)
	for i := range fullPage1 {
		fullPage1[i] = historyRecord{
			EventType: eventImported,
			Date:      now.Add(-30 * time.Minute),
			Data: struct {
				ImportedPath string `json:"importedPath"`
				Path         string `json:"path"`
				SourcePath   string `json:"sourcePath"`
			}{ImportedPath: fmt.Sprintf("/media/page1-%d.mkv", i)},
		}
	}
	// Override first two to have distinct names that we assert on.
	fullPage1[0].Data.ImportedPath = "/media/page1-a.mkv"
	fullPage1[1].Data.ImportedPath = "/media/page1-b.mkv"
	// Deduplicate the rest by setting them to empty path so they are skipped.
	for i := 2; i < historyPageSize; i++ {
		fullPage1[i].Data.ImportedPath = ""
	}

	// Page 2: one record inside the window (after callerMarker) and one old record
	// that should STOP paging (date <= querySince).
	page2Records := []historyRecord{
		{EventType: eventImported, Date: now.Add(-89 * time.Minute), Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{ImportedPath: "/media/page2-new.mkv"}},
		// This record is older than querySince => must stop paging here.
		{EventType: eventImported, Date: querySince.Add(-5 * time.Minute), Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{ImportedPath: "/media/page2-old-should-not-appear.mkv"}},
	}

	var pagesRequested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		pagesRequested = append(pagesRequested, p)
		switch p {
		case "1":
			// Return a full page (historyPageSize records) so paging does not stop early
			// due to the "fewer than full page" shortcut.
			_, _ = w.Write([]byte(pagedBody(1, historyPageSize, 1000, fullPage1)))
		case "2":
			// Paging must stop at the old record inside this page.
			_, _ = w.Write([]byte(pagedBody(2, historyPageSize, 1000, page2Records)))
		default:
			t.Errorf("unexpected page %q requested", p)
			_, _ = w.Write([]byte(pagedBody(3, historyPageSize, 1000, nil)))
		}
	}))
	defer srv.Close()

	paths, newest, err := ChangedPaths(context.Background(), srv.URL, "k", callerMarker)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}

	// Must have fetched pages 1 and 2 and NOT a third page.
	if len(pagesRequested) != 2 {
		t.Fatalf("expected 2 pages requested, got %d: %v", len(pagesRequested), pagesRequested)
	}

	sort.Strings(paths)

	// page1-a and page1-b are after callerMarker (now-30m > now-90m) → emitted.
	// page1 records with empty ImportedPath are skipped.
	// page2-new: date is now-89m; callerMarker is now-90m → emitted.
	// page2-old: date is querySince-5m < callerMarker → stops paging, not emitted.
	want := []string{
		"/media/page1-a.mkv",
		"/media/page1-b.mkv",
		"/media/page2-new.mkv",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}

	// newest must be the most recent record date (page1 records at now-30m).
	wantNewest := now.Add(-30 * time.Minute).Truncate(time.Second)
	if newest.Before(wantNewest.Add(-2*time.Second)) || newest.After(wantNewest.Add(2*time.Second)) {
		t.Fatalf("newest = %v, want ~%v", newest, wantNewest)
	}
}

// TestChangedPathsBoundaryDedup verifies Fix 2: a record at exactly the caller
// marker (T) is NOT re-emitted; only records strictly after T are emitted.
func TestChangedPathsBoundaryDedup(t *testing.T) {
	now := time.Now().UTC()
	// Caller marker T: 30 minutes ago.
	T := now.Add(-30 * time.Minute).Truncate(time.Second)

	// arr returns two records: one AT T (already seen) and one 30s after T (new).
	atT := T
	afterT := T.Add(30 * time.Second)

	records := []historyRecord{
		// date-descending
		{EventType: eventImported, Date: afterT, Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{ImportedPath: "/media/new.mkv"}},
		{EventType: eventImported, Date: atT, Data: struct {
			ImportedPath string `json:"importedPath"`
			Path         string `json:"path"`
			SourcePath   string `json:"sourcePath"`
		}{ImportedPath: "/media/already-seen.mkv"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(pagedBody(1, historyPageSize, len(records), records)))
	}))
	defer srv.Close()

	paths, newest, err := ChangedPaths(context.Background(), srv.URL, "k", T)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}

	// Only the record after T must appear.
	if len(paths) != 1 || paths[0] != "/media/new.mkv" {
		t.Fatalf("paths = %v, want [/media/new.mkv]", paths)
	}

	// newest must be afterT (the most recent timestamp seen).
	if !newest.Equal(afterT) {
		t.Fatalf("newest = %v, want %v", newest, afterT)
	}
}

// TestChangedPathsLargeSinglePageCapped verifies that the per-page LimitReader
// in getJSON prevents a >1 MiB response from being decoded silently with
// truncated data. The body is a valid JSON object whose importedPath field is
// padded to push the total body size over maxResponseBody; io.LimitReader
// truncates mid-JSON, causing a decode error.
func TestChangedPathsLargeSinglePageCapped(t *testing.T) {
	// Embed the padding INSIDE the importedPath JSON string value so the
	// truncation cuts through valid JSON (the closing quote/bracket are past the
	// 1 MiB mark). io.LimitReader will truncate before the closing `}]}`, causing
	// json.Decoder to return an unexpected-EOF error.
	bigPath := strings.Repeat("A", maxResponseBody) // > 1 MiB on its own
	dateStr := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	oversize := `{"page":1,"pageSize":1,"totalRecords":1,"records":[{"eventType":"downloadFolderImported","date":"` +
		dateStr + `","data":{"importedPath":"` + bigPath + `"}}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oversize))
	}))
	defer srv.Close()

	_, _, err := ChangedPaths(context.Background(), srv.URL, "k", time.Time{})
	if err == nil {
		t.Fatal("expected error when single page body exceeds 1 MiB LimitReader cap, but got nil")
	}
}

// TestChangedPathsPageCapBoundsWork verifies that paging stops at maxHistoryPages
// even if the server keeps returning full pages, so a rogue/huge arr history
// cannot cause an unbounded loop.
func TestChangedPathsPageCapBoundsWork(t *testing.T) {
	now := time.Now().UTC()
	var pagesSeen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		pagesSeen = append(pagesSeen, p)
		// Every page has one recent record, return historyPageSize records so paging
		// continues until the cap.
		rec := historyRecord{
			EventType: eventImported,
			Date:      now.Add(-time.Minute),
			Data: struct {
				ImportedPath string `json:"importedPath"`
				Path         string `json:"path"`
				SourcePath   string `json:"sourcePath"`
			}{ImportedPath: fmt.Sprintf("/media/page%s.mkv", p)},
		}
		recs := make([]historyRecord, historyPageSize)
		for i := range recs {
			recs[i] = rec
		}
		_, _ = w.Write([]byte(pagedBody(1, historyPageSize, 10000, recs)))
	}))
	defer srv.Close()

	_, _, err := ChangedPaths(context.Background(), srv.URL, "k", now.Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if len(pagesSeen) != maxHistoryPages {
		t.Fatalf("expected paging to stop at %d pages, got %d", maxHistoryPages, len(pagesSeen))
	}
}
