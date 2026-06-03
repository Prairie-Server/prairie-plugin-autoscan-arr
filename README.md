# silo-plugin-autoscan-arr

A Silo plugin that implements the **`scan_source.v1`** capability for **Sonarr / Radarr**.
When Silo's host polls it, the plugin reads the arr instance's recent history,
extracts imported and renamed file paths, translates them into Silo's filesystem
namespace, and hands them back so the host can trigger targeted library rescans.

## How it works

Silo's host owns the polling timer and calls `PollChanges(marker, connection)` on
each tick. The plugin:

1. Reads the resolved arr `{base_url, api_key}` from `PollChangesRequest.connection`
   (the host resolves the operator's connection — own credentials or a reused
   Requests link — and passes them per call). **The plugin stores no credentials.**
2. Queries arr `/api/v3/history/since`, bounded to a 24h max-lookback with a 1-minute
   overlap so boundary events aren't missed and a long-idle source doesn't replay
   full history.
3. Extracts `downloadFolderImported` (new files) and `episodeFileRenamed` /
   `movieFileRenamed` (both the new and old path). Deletes are ignored — upgrade
   deletes are covered by the paired import.
4. Applies the configured **path rewrites** (boundary-safe prefix match) so the
   returned paths are Silo-native.
5. Returns the changed paths plus an opaque `next_marker` (an RFC3339 timestamp that
   never regresses below the caller's marker).

## Configuration

Only path rewrites are plugin config (`global_config_schema.path_rewrites`) — a list
of `{from, to}` prefixes mapping arr-side mount paths to Silo-side paths. The arr
connection (URL + API key) is **not** plugin config; it is supplied by the host on
every poll.

```json
{ "path_rewrites": [ { "from": "/data/media", "to": "/mnt/library" } ] }
```

## Building

```sh
make build          # cross-platform binaries
go test ./...       # unit tests
go test -tags integration ./...   # spawns the real binary over go-plugin gRPC
```

## Status

Depends on `silo-plugin-sdk` ≥ the release that adds `scan_source.v1` with the
`connection` field on `PollChangesRequest`. Until that is tagged, `go.mod` carries a
local `replace`; finalize it to the tagged version before publishing. Catalog
registration in `silo-plugins` is a separate step.
