package ui

import (
	"path/filepath"
	"testing"
)

func TestHomeRelativeForDisplay(t *testing.T) {
	home := t.TempDir()
	siblingPath := home + "-other"

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "path under home",
			path:     filepath.Join(home, ".config", "agent-deck", "config.toml"),
			expected: "~/.config/agent-deck/config.toml",
		},
		{
			name:     "home directory itself",
			path:     home,
			expected: "~",
		},
		{
			name:     "path outside home",
			path:     "/etc/agent-deck/config.toml",
			expected: "/etc/agent-deck/config.toml",
		},
		{
			name:     "sibling whose name starts with home path",
			path:     filepath.Join(siblingPath, "config.toml"),
			expected: filepath.Join(siblingPath, "config.toml"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", home)

			result := homeRelativeForDisplay(tt.path)
			if result != tt.expected {
				t.Errorf("homeRelativeForDisplay(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}
