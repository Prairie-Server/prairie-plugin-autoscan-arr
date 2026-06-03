//go:build integration

// End-to-end test: builds the real plugin binary, spawns it over go-plugin
// (the same handshake silo's host uses), and drives PollChanges across the
// gRPC boundary against a stub arr — exercising the scan_source.v1 contract
// including the resolved-connection delivery. Run: go test -tags integration .
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	plugin "github.com/hashicorp/go-plugin"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

func TestEndToEndPollChangesOverGRPC(t *testing.T) {
	const apiKey = "secret-key"
	const importedPath = "/data/media/tv/Show/Season 01/Show - S01E01.mkv"

	gotAPIKey := make(chan string, 1)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAPIKey <- r.Header.Get("X-Api-Key"):
		default:
		}
		// Compute a fresh "just now" date on every request so the record always falls
		// within the paged query window (querySince = now - overlap) regardless of how
		// long the binary build took before the first poll arrives.
		freshDate := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339)
		// Return the paged envelope that the plugin now expects from /api/v3/history.
		_, _ = w.Write([]byte(`{"page":1,"pageSize":200,"totalRecords":2,"records":[
		  {"eventType":"downloadFolderImported","date":"` + freshDate + `","data":{"importedPath":"` + importedPath + `"}},
		  {"eventType":"grabbed","date":"` + freshDate + `","data":{"importedPath":"/ignored"}}
		]}`))
	}))
	defer stub.Close()

	// Build the real plugin binary (uses this module's go.mod + SDK replace).
	bin := filepath.Join(t.TempDir(), "autoscan-arr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}

	// Spawn it exactly as the host does.
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  runtime.HandshakeConfig(),
		Plugins:          runtime.DefaultPluginSet(runtime.CapabilityServers{}),
		Cmd:              exec.Command(bin),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	defer client.Kill()

	rpc, err := client.Client()
	if err != nil {
		t.Fatalf("connect to plugin: %v", err)
	}
	raw, err := rpc.Dispense(runtime.PluginSetName)
	if err != nil {
		t.Fatalf("dispense: %v", err)
	}
	rt, ok := raw.(*runtime.Client)
	if !ok {
		t.Fatalf("dispensed %T, want *runtime.Client", raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// First poll: empty marker => "from now"; connection carries the creds.
	resp, err := rt.ScanSource().PollChanges(ctx, &pluginv1.PollChangesRequest{
		CapabilityId: "arr",
		Connection:   &pluginv1.ResolvedConnection{BaseUrl: stub.URL, ApiKey: apiKey},
	})
	if err != nil {
		t.Fatalf("PollChanges: %v", err)
	}

	// The plugin must have reached the stub using the api key from the request.
	select {
	case k := <-gotAPIKey:
		if k != apiKey {
			t.Fatalf("arr received X-Api-Key %q, want %q", k, apiKey)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plugin never called the stub arr")
	}

	// The raw arr-side imported path must come back (grabbed ignored); no
	// rewriting applied — the host owns that. Marker non-empty.
	var found bool
	for _, p := range resp.GetSourcePaths() {
		if p == importedPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("imported path missing from source_paths %v", resp.GetSourcePaths())
	}
	if resp.GetNextMarker() == "" {
		t.Fatal("expected a non-empty next_marker")
	}

	// Nil connection must be rejected (no creds to poll with).
	if _, err := rt.ScanSource().PollChanges(ctx, &pluginv1.PollChangesRequest{CapabilityId: "arr"}); err == nil {
		t.Fatal("PollChanges with no connection should error")
	}
}
