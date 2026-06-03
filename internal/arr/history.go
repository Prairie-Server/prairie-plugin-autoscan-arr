package arr

import (
	"context"
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
func ChangedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) (paths []string, newest time.Time, err error) {
	now := time.Now().UTC()
	marker := since.UTC() // caller's original marker (may be zero); the returned marker must never regress below it
	if since.IsZero() {
		since = now
	}
	since = since.UTC()

	// Clamp to the max-lookback floor, then subtract the overlap buffer. This
	// only widens the QUERY window; the RETURNED marker is floored separately.
	floor := now.Add(-maxLookback)
	if since.Before(floor) {
		since = floor
	}
	since = since.Add(-overlap)

	c := newClient(baseURL, apiKey, nil)
	q := url.Values{}
	q.Set("date", since.Format(time.RFC3339))

	var records []historyRecord
	if err := c.getJSON(ctx, "/api/v3/history/since?"+q.Encode(), &records); err != nil {
		return nil, time.Time{}, err
	}

	// Seed from the (rewound) query window but never report a marker older than
	// the caller's original marker — otherwise an empty poll would creep the
	// window back by the overlap each time and re-emit the same paths.
	newest = since
	if marker.After(newest) {
		newest = marker
	}
	for _, rec := range records {
		if rec.Date.After(newest) {
			newest = rec.Date.UTC()
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
	return paths, newest, nil
}
