package session

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// TestUISettings_GetCompactDefaults exercises the [ui] compact contract: an
// omitted value preserves the classic (roomier) layout, and an explicit true
// (whether constructed directly or decoded from TOML) turns the tightened
// vertical layout on.
func TestUISettings_GetCompactDefaults(t *testing.T) {
	if (UISettings{}).GetCompact() {
		t.Fatal("zero UISettings should default compact to false")
	}

	on := true
	if !(UISettings{Compact: &on}).GetCompact() {
		t.Fatal("explicit Compact=true was ignored")
	}

	var cfg UserConfig
	if _, err := toml.Decode("[ui]\ncompact = true\n", &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !cfg.UI.GetCompact() {
		t.Fatal("explicit compact=true did not round-trip through TOML decode")
	}
}
