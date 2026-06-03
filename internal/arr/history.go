package arr

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
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

// historyRecord is the subset of an arr history entry we care about. ID is the
// arr-side monotonic record identifier; it is the tiebreaker for records that
// share the same second-granularity Date so a same-second straggler is not
// silently dropped across polls (see the composite marker in ParseMarker).
type historyRecord struct {
	ID        int       `json:"id"`
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

// Marker is the opaque poll cursor handed back to (and round-tripped by) the
// host. It is a composite of the last-processed history record's (Date, ID):
// Date alone is insufficient because arr timestamps have only second
// granularity, so records sharing a second must be disambiguated by their
// monotonic ID to avoid dropping a same-second straggler when a poll stops on a
// page boundary mid-second.
type Marker struct {
	// Date is the second-granularity timestamp of the last processed record.
	Date time.Time
	// ID is the arr record id of the last processed record, used to break ties
	// among records sharing Date. Zero for legacy bare-timestamp markers.
	ID int
}

// markerSep separates the timestamp and id halves of a composite marker string.
const markerSep = "|"

// ParseMarker parses a host-stored marker string into a Marker. It accepts both
// the composite "<RFC3339>|<id>" form and a bare "<RFC3339>" string (parsed as
// {Date, ID: 0}) for backward compatibility with already-stored v1 markers,
// which were bare timestamps. An empty string yields the zero Marker.
func ParseMarker(s string) (Marker, error) {
	if s == "" {
		return Marker{}, nil
	}
	tsPart := s
	idPart := ""
	if i := strings.LastIndex(s, markerSep); i >= 0 {
		tsPart = s[:i]
		idPart = s[i+len(markerSep):]
	}
	t, err := time.Parse(time.RFC3339, tsPart)
	if err != nil {
		return Marker{}, fmt.Errorf("arr: invalid marker timestamp %q: %w", tsPart, err)
	}
	id := 0
	if idPart != "" {
		id, err = strconv.Atoi(idPart)
		if err != nil {
			return Marker{}, fmt.Errorf("arr: invalid marker id %q: %w", idPart, err)
		}
	}
	return Marker{Date: t.UTC(), ID: id}, nil
}

// String formats the Marker as the composite "<RFC3339>|<id>" form that the
// host stores opaquely and round-trips back via ParseMarker.
func (m Marker) String() string {
	return m.Date.UTC().Format(time.RFC3339) + markerSep + strconv.Itoa(m.ID)
}

// markerLess reports whether a orders strictly before b under the composite
// (Date, ID) ordering: earlier Date wins, and for an equal Date the lower ID
// wins. It is the single source of truth for both "is this record newer than
// the marker we should advance to" and "is this record after the caller's
// marker, so emit it".
func markerLess(a, b Marker) bool {
	if a.Date.Equal(b.Date) {
		return a.ID < b.ID
	}
	return a.Date.Before(b.Date)
}

// ChangedPaths returns file paths whose library folder should be rescanned:
// imported files, and for renames both the new and old paths (a rename can move
// a file between folders, so both parents may need scanning). Delete events are
// intentionally not handled — upgrade-deletes are already covered by the paired
// import, and standalone deletes carry no file path in arr history.
//
// since is the marker to poll from. since.Date is the lower bound; a zero
// since.Date means "now". It is clamped to no older than maxLookback and an
// overlap buffer is subtracted to avoid missing boundary events. The returned
// newest marker is the (Date, ID) of the most-recently-processed record (or the
// effective since when no records returned), suitable as the next marker.
// Credentials are passed per call; the plugin stores none.
//
// History is fetched via the paginated GET /api/v3/history endpoint
// (sortKey=date, sortDirection=descending) to avoid 1 MiB body truncation on
// bulk imports. Paging stops as soon as a record at or before the composite
// since boundary is encountered (records are date-descending; anything older or
// equal is already seen). A record is emitted iff it is strictly after the
// caller's original (Date, ID) marker — by Date, or by ID when the Date is
// equal — so a same-second straggler left over from a prior page-bounded poll
// is still emitted on the next poll instead of being filtered out forever.
//
// If the loop exhausts all maxHistoryPages without reaching the since boundary
// (more history exists than a single poll consumes, e.g. a bulk import), it
// logs a warning rather than silently truncating; the composite marker lets the
// next poll resume exactly where this one stopped.
func ChangedPaths(ctx context.Context, baseURL, apiKey string, since Marker) (paths []string, newest Marker, err error) {
	now := time.Now().UTC()
	// marker is the caller's original (Date, ID); the returned marker must never
	// regress below it.
	marker := Marker{Date: since.Date.UTC(), ID: since.ID}
	sinceDate := since.Date
	if sinceDate.IsZero() {
		sinceDate = now
	}
	sinceDate = sinceDate.UTC()

	// Clamp a future-dated marker to now. Clock skew (host/arr on different
	// machines) can produce a since > now; leaving it in the future would push
	// the query window ahead of wall-clock, skipping real events until wall-clock
	// catches up. We clamp both the effective query since and the marker used as
	// the returned-value floor, so a future caller marker cannot propagate into
	// the returned newest value.
	if sinceDate.After(now) {
		sinceDate = now
	}
	if marker.Date.After(now) {
		marker.Date = now
	}

	// Clamp to the max-lookback floor, then subtract the overlap buffer. This
	// only widens the QUERY window; the RETURNED marker is floored separately.
	floor := now.Add(-maxLookback)
	if sinceDate.Before(floor) {
		sinceDate = floor
	}
	querySince := sinceDate.Add(-overlap)

	c := newClient(baseURL, apiKey, nil)

	// Seed the returned marker from the effective since (the floor of the query
	// window before the overlap rewind), never from the rewound querySince —
	// otherwise an empty/first poll would report a marker an overlap behind the
	// effective "from now" point and replay the overlap window next call. Never
	// regress below the caller's original marker either.
	newest = Marker{Date: sinceDate}
	if marker.Date.After(newest.Date) {
		newest = marker
	}

	reachedBoundary := false
	for page := 1; page <= maxHistoryPages; page++ {
		q := url.Values{}
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("pageSize", fmt.Sprintf("%d", historyPageSize))
		q.Set("sortKey", "date")
		q.Set("sortDirection", "descending")

		var pg historyPage
		if err := c.getJSON(ctx, "/api/v3/history?"+q.Encode(), &pg); err != nil {
			return nil, Marker{}, err
		}

		done := false
		for _, rec := range pg.Records {
			recDate := rec.Date.UTC()
			// Records are date-descending. Once we hit a record at or before the
			// query window start, everything remaining is already-seen history.
			if !recDate.After(querySince) {
				done = true
				break
			}

			// Track the newest (Date, ID) processed across all pages.
			if markerLess(newest, Marker{Date: recDate, ID: rec.ID}) {
				newest = Marker{Date: recDate, ID: rec.ID}
			}

			// Only EMIT records strictly after the caller's original (Date, ID)
			// marker. The ID tiebreak ensures a record sharing the marker's second
			// but with a higher id (a same-second straggler from a prior
			// page-bounded poll) is still emitted rather than filtered out forever.
			if !markerLess(marker, Marker{Date: recDate, ID: rec.ID}) {
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
			reachedBoundary = true
			break
		}

		// If we received fewer records than a full page, there are no more pages.
		if len(pg.Records) < historyPageSize {
			reachedBoundary = true
			break
		}
	}

	if !reachedBoundary {
		slog.Warn("arr history page cap hit before reaching the marker boundary; "+
			"more history exists than one poll consumed, records will be processed "+
			"across subsequent polls",
			"max_pages", maxHistoryPages, "page_size", historyPageSize)
	}

	return paths, newest, nil
}
