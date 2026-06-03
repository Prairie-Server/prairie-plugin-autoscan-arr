package config

import "testing"

func TestParseConfig(t *testing.T) {
	cfg, err := Parse(map[string]string{
		"path_rewrites": `[{"from":"/mnt/arr/tv","to":"/mnt/media/tv"}]`,
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Rewrites) != 1 || cfg.Rewrites[0].From != "/mnt/arr/tv" || cfg.Rewrites[0].To != "/mnt/media/tv" {
		t.Fatalf("bad rewrites: %+v", cfg.Rewrites)
	}
}

func TestParseConfigEmpty(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if len(cfg.Rewrites) != 0 {
		t.Fatalf("expected no rewrites, got %+v", cfg.Rewrites)
	}

	cfg, err = Parse(map[string]string{"path_rewrites": ""})
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if len(cfg.Rewrites) != 0 {
		t.Fatalf("expected no rewrites for empty value, got %+v", cfg.Rewrites)
	}
}

func TestParseConfigMultiple(t *testing.T) {
	cfg, err := Parse(map[string]string{
		"path_rewrites": `[{"from":"/data","to":"/A"},{"from":"/data/media","to":"/B"}]`,
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Rewrites) != 2 {
		t.Fatalf("expected 2 rewrites, got %+v", cfg.Rewrites)
	}
}

func TestParseConfigInvalidJSON(t *testing.T) {
	if _, err := Parse(map[string]string{"path_rewrites": "{not json"}); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
