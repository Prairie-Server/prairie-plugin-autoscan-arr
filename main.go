// Command silo-plugin-autoscan-arr implements the Silo scan_source.v1 capability
// for Sonarr/Radarr. On each host poll it reads the arr /history endpoint,
// extracts imported + renamed file paths, applies operator-configured path
// rewrites, and returns Silo-native paths plus an opaque marker.
//
// Credentials (arr base URL + API key) are NOT plugin config: they arrive in
// each PollChangesRequest.connection, resolved by the host from the operator's
// Autoscan connection. The plugin stores only path rewrites.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/Silo-Server/silo-plugin-autoscan-arr/internal/arr"
	"github.com/Silo-Server/silo-plugin-autoscan-arr/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version string

//go:embed manifest.json
var manifestJSON []byte

// runtimeServer serves the plugin manifest and stores parsed configuration.
type runtimeServer struct {
	pluginv1.UnimplementedRuntimeServer

	manifest *pluginv1.PluginManifest

	mu  sync.RWMutex
	cfg *config.Config
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

// Configure parses the host-supplied config entries into path rewrites. The
// host delivers config as []ConfigEntry{Key, Value(*structpb.Struct)}. We
// flatten each entry's struct value into a JSON string keyed by the entry key
// so config.Parse can unmarshal it. Parsing is best-effort: a malformed payload
// leaves the previously-stored (or empty) rewrites in place rather than failing
// the whole Configure call, so polling degrades to pass-through paths.
func (s *runtimeServer) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	values := configValuesFromRequest(req)
	cfg, err := config.Parse(values)
	if err != nil {
		// Best-effort: keep prior config, don't fail Configure.
		return &pluginv1.ConfigureResponse{}, nil
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return &pluginv1.ConfigureResponse{}, nil
}

func (s *runtimeServer) rewrites() []config.Rewrite {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil {
		return nil
	}
	return s.cfg.Rewrites
}

// configValuesFromRequest flattens ConfigEntry structs into a flat string map
// that config.Parse understands. For an entry whose struct holds a single field
// matching the entry key (e.g. {"path_rewrites": [...]}), the nested value is
// extracted; otherwise the whole struct is marshalled. The value is always a
// JSON string.
func configValuesFromRequest(req *pluginv1.ConfigureRequest) map[string]string {
	values := make(map[string]string)
	for _, entry := range req.GetConfig() {
		key := entry.GetKey()
		if key == "" {
			continue
		}
		st := entry.GetValue()
		if st == nil {
			continue
		}
		m := st.AsMap()
		// Prefer the nested field that mirrors the entry key.
		if nested, ok := m[key]; ok {
			if raw, err := json.Marshal(nested); err == nil {
				values[key] = string(raw)
			}
			continue
		}
		if raw, err := json.Marshal(m); err == nil {
			values[key] = string(raw)
		}
	}
	return values
}

// scanSourceServer implements scan_source.v1 PollChanges.
type scanSourceServer struct {
	pluginv1.UnimplementedScanSourceServer
	rt *runtimeServer
}

// PollChanges reads the connection from the request (never from config), polls
// arr history since the supplied marker, rewrites each path to Silo-native form,
// and returns the changed paths plus the next marker (RFC3339).
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

	rewrites := s.rt.rewrites()
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		out = append(out, arr.ApplyRewrites(arr.NormalizeSeparators(p), rewrites))
	}

	return &pluginv1.PollChangesResponse{
		ChangedPaths: out,
		NextMarker:   newest.UTC().Format(time.RFC3339),
	}, nil
}

func main() {
	manifest, err := loadManifest()
	if err != nil {
		panic(err)
	}

	rt := &runtimeServer{manifest: manifest, cfg: &config.Config{}}

	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:    rt,
			ScanSource: &scanSourceServer{rt: rt},
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
