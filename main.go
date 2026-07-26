// Command prairie-plugin-autoscan-arr implements the Prairie scan_source.v1 capability
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

	pluginv1 "github.com/prairie-server/prairie-plugin-sdk/pkg/pluginproto/prairie/plugin/v1"
	publicmanifest "github.com/prairie-server/prairie-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/prairie-server/prairie-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/prairie-server/prairie-plugin-autoscan-arr/internal/arr"
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

// GetManifest returns the plugin's embedded manifest (with the build-time
// version and computed binary checksum) so the host can register the plugin's
// capabilities.
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
// plus the next marker. The host applies path rewrites.
//
// The incoming marker is parsed via arr.ParseMarker, which accepts both the
// composite "<RFC3339>|<id>" form and a bare RFC3339 timestamp for backward
// compatibility with already-stored markers. The outgoing NextMarker is always
// emitted in the composite form. The host treats the marker as opaque.
func (s *scanSourceServer) PollChanges(ctx context.Context, req *pluginv1.PollChangesRequest) (*pluginv1.PollChangesResponse, error) {
	conn := req.GetConnection()
	if conn == nil || conn.GetBaseUrl() == "" {
		return nil, fmt.Errorf("scan_source: no connection supplied")
	}

	since, err := arr.ParseMarker(req.GetMarker()) // empty => zero Marker => floor to "now"
	if err != nil {
		return nil, fmt.Errorf("scan_source: invalid marker %q: %w", req.GetMarker(), err)
	}

	raw, newest, err := arr.ChangedPaths(ctx, conn.GetBaseUrl(), conn.GetApiKey(), since)
	if err != nil {
		return nil, err
	}

	return &pluginv1.PollChangesResponse{
		SourcePaths: raw,
		NextMarker:  newest.String(),
	}, nil
}

// main loads the embedded manifest and serves the plugin's Runtime and
// ScanSource capabilities over the go-plugin gRPC transport the host drives.
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

// loadManifest parses the embedded manifest.json, overrides its version with
// the build-time main.version when set, and stamps the manifest Checksum with
// the SHA-256 of the running executable so the host can verify the binary.
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
