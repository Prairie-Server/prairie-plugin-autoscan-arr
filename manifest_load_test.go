package main

import "testing"

func TestEmbeddedManifestLoads(t *testing.T) {
	m, err := loadManifest()
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if m.GetPluginId() != "silo.autoscan.arr" {
		t.Fatalf("plugin_id = %q", m.GetPluginId())
	}
	if len(m.GetCapabilities()) != 1 || m.GetCapabilities()[0].GetType() != "scan_source.v1" {
		t.Fatalf("capabilities = %+v", m.GetCapabilities())
	}
	if m.GetCapabilities()[0].GetId() != "arr" {
		t.Fatalf("capability id = %q", m.GetCapabilities()[0].GetId())
	}
	if len(m.GetGlobalConfigSchema()) != 1 || m.GetGlobalConfigSchema()[0].GetKey() != "path_rewrites" {
		t.Fatalf("global_config_schema = %+v", m.GetGlobalConfigSchema())
	}
}
