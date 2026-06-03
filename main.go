// Command silo-plugin-autoscan-arr implements the Silo scan_source.v1 capability
// for Sonarr/Radarr. On each host poll it reads the arr /history endpoint,
// extracts imported + renamed file paths, and returns the raw arr-side paths
// plus an opaque marker. Path rewrites are applied by the host; the plugin has
// no configuration of its own.
//
// Credentials (arr base URL + API key) are NOT plugin config: they arrive in
// each PollChangesRequest.connection, resolved by the host from the operator's
// Autoscan connection.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/Silo-Server/silo-plugin-autoscan-arr/internal/arr"
)

// version is set at build time via -ldflags "-X main.version=...".
var version string

//go:embed manifest.json
var manifestJSON []byte

// runtimeServer serves the plugin manifest. The plugin has no config.
type runtimeServer struct {
	pluginv1.UnimplementedRuntimeServer

	manifest *pluginv1.PluginManifest
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

// Configure is a no-op: the plugin has no configuration. Path rewrites are
// owned by the host.
func (s *runtimeServer) Configure(_ context.Context, _ *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	return &pluginv1.ConfigureResponse{}, nil
}

// scanSourceServer implements scan_source.v1 PollChanges.
type scanSourceServer struct {
	pluginv1.UnimplementedScanSourceServer
}

// PollChanges reads the connection from the request (never from config), polls
// arr history since the supplied marker, and returns the raw arr-side paths
// plus the next marker (RFC3339). The host applies path rewrites.
func (s *scanSourceServer) PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	conn := req.GetConnection()
	if conn == nil || conn.GetBaseUrl() == "" {
		return nil, fmt.Errorf("scan_source: no connection supplied")
	}

	var since time.Time // zero => history client floors to "now"
	if m := req.GetMarker(); m != "" {
		if t, err := time.Parse(time.RFC3339, m); err == nil {
			since = t
		}
	}

	raw, newest, err := arr.ChangedPaths(ctx, conn.GetBaseUrl(), conn.GetApiKey(), since)
	if err != nil {
		return nil, err
	}

	return &pluginv1.PollChangesResponse{
		SourcePaths: raw,
		NextMarker:  newest.UTC().Format(time.RFC3339),
	}, nil
}

func main() {
	manifest, err := loadManifest()
	if err != nil {
		panic(err)
	}

	rt := &runtimeServer{manifest: manifest}

	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:    rt,
			ScanSource: &scanSourceServer{},
		},
	})
}

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}

	if version != "" {
		manifest.Version = version
	}

	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	binaryData, err := os.ReadFile(executablePath)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", executablePath, err)
	}
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])

	return manifest, nil
}
