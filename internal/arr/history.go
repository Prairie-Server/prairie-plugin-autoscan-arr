package arr

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

const (
	eventImported       = "downloadFolderImported"
	eventEpisodeRenamed = "episodeFileRenamed"
	eventMovieRenamed   = "movieFileRenamed"
)

// maxLookback floors how far back ChangedPaths will poll. A stale or absent
// marker is clamped to now-maxLookback so a long-idle source does not replay
// the entire arr history on its next poll.
const maxLookback = 24 * time.Hour

// overlap re-polls slightly before the marker so events landing right on a
// previous poll's boundary are not missed (arr history timestamps have
// second granularity).
const overlap = 1 * time.Minute

// historyPageSize is the number of records requested per page. A single page
// of 200 records stays well under the 1 MiB per-page body cap.
const historyPageSize = 200

// maxHistoryPages caps the total pages fetched per poll to bound work.
const maxHistoryPages = 50

// historyRecord is the subset of an arr history entry we care about.
type historyRecord struct {
	EventType string    `json:"eventType"`
	Date      time.Time `json:"date"`
	Data      struct {
		ImportedPath string `json:"importedPath"` // downloadFolderImported: new file
		Path         string `json:"path"`         // *FileRenamed: new path
		SourcePath   string `json:"sourcePath"`   // *FileRenamed: old path
	} `json:"data"`
}

// historyPage is the paged envelope returned by GET /api/v3/history.
type historyPage struct {
	Page         int             `json:"page"`
	PageSize     int             `json:"pageSize"`
	TotalRecords int             `json:"totalRecords"`
	Records      []historyRecord `json:"records"`
}

// ChangedPaths returns file paths whose library folder should be rescanned:
// imported files, and for renames both the new and old paths (a rename can move
// a file between folders, so both parents may need scanning). Delete events are
// intentionally not handled — upgrade-deletes are already covered by the paired
// import, and standalone deletes carry no file path in arr history.
//
// since is the lower bound to poll from; an empty since means "now". It is
// clamped to no older than maxLookback and an overlap buffer is subtracted to
// avoid missing boundary events. newest is the most recent history timestamp
// observed (or the effective since when no records returned), suitable as the
// next marker. Credentials are passed per call; the plugin stores none.
//
// History is fetched via the paginated GET /api/v3/history endpoint
// (sortKey=date, sortDirection=descending) to avoid 1 MiB body truncation on
// bulk imports. Paging stops as soon as a record with date <= querySince is
// encountered (records are date-descending; anything older is already seen).
// Only records strictly newer than the caller's original marker are emitted to
// avoid re-emitting the boundary record on every poll.
func ChangedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) (paths []string, newest time.Time, err error) {
	now := time.Now().UTC()
	marker := since.UTC() // caller's original marker (may be zero); the returned marker must never regress below it
	if since.IsZero() {
		since = now
	}
	since = since.UTC()

	// Clamp a future-dated marker to now. Clock skew (host/arr on different
	// machines) can produce a since > now; leaving it in the future would push
	// the query window ahead of wall-clock, skipping real events until wall-clock
	// catches up. We clamp both the effective query since and the marker used as
	// the returned-value floor, so a future caller marker cannot propagate into
	// the returned newest value.
	if since.After(now) {
		since = now
	}
	if marker.After(now) {
		marker = now
	}

	// Clamp to the max-lookback floor, then subtract the overlap buffer. This
	// only widens the QUERY window; the RETURNED marker is floored separately.
	floor := now.Add(-maxLookback)
	if since.Before(floor) {
		since = floor
	}
	querySince := since.Add(-overlap)

	c := newClient(baseURL, apiKey, nil)

	// Seed from the (rewound) query window but never report a marker older than
	// the caller's original marker — otherwise an empty poll would creep the
	// window back by the overlap each time and re-emit the same paths.
	newest = querySince
	if marker.After(newest) {
		newest = marker
	}

	for page := 1; page <= maxHistoryPages; page++ {
		q := url.Values{}
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("pageSize", fmt.Sprintf("%d", historyPageSize))
		q.Set("sortKey", "date")
		q.Set("sortDirection", "descending")

		var pg historyPage
		if err := c.getJSON(ctx, "/api/v3/history?"+q.Encode(), &pg); err != nil {
			return nil, time.Time{}, err
		}

		done := false
		for _, rec := range pg.Records {
			// Records are date-descending. Once we hit a record at or before the
			// query window start, everything remaining is already-seen history.
			if !rec.Date.After(querySince) {
				done = true
				break
			}

			// Track the newest timestamp seen across all pages.
			if rec.Date.After(newest) {
				newest = rec.Date.UTC()
			}

			// Only EMIT records strictly newer than the caller's original marker
			// to avoid re-emitting the boundary record on every poll (Fix 2).
			if !rec.Date.After(marker) {
				continue
			}

			switch rec.EventType {
			case eventImported:
				if rec.Data.ImportedPath != "" {
					paths = append(paths, rec.Data.ImportedPath)
				}
			case eventEpisodeRenamed, eventMovieRenamed:
				if rec.Data.Path != "" {
					paths = append(paths, rec.Data.Path)
				}
				if rec.Data.SourcePath != "" {
					paths = append(paths, rec.Data.SourcePath)
				}
			}
		}

		if done {
			break
		}

		// If we received fewer records than a full page, there are no more pages.
		if len(pg.Records) < historyPageSize {
			break
		}
	}

	return paths, newest, nil
}
