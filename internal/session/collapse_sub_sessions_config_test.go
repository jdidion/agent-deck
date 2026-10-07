package session

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestUISettings_GetCollapseSubSessions(t *testing.T) {
	if (UISettings{}).GetCollapseSubSessions() {
		t.Fatal("zero UISettings should default collapse_sub_sessions to false")
	}

	on := true
	if !(UISettings{CollapseSubSessions: &on}).GetCollapseSubSessions() {
		t.Fatal("explicit CollapseSubSessions=true was ignored")
	}

	off := false
	if (UISettings{CollapseSubSessions: &off}).GetCollapseSubSessions() {
		t.Fatal("explicit CollapseSubSessions=false was reported as true")
	}

	var cfg UserConfig
	if _, err := toml.Decode("[ui]\ncollapse_sub_sessions = true\n", &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !cfg.UI.GetCollapseSubSessions() {
		t.Fatal("collapse_sub_sessions=true did not round-trip through TOML decode")
	}
}
