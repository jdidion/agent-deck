package ui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func userConfigPathForDisplay() string {
	path, err := session.GetUserConfigPath()
	if err != nil {
		return "$XDG_CONFIG_HOME/agent-deck/config.toml"
	}
	return homeRelativeForDisplay(path)
}

func skillPoolPathForDisplay() string {
	path, err := session.GetSkillPoolPath()
	if err != nil {
		return "$XDG_CONFIG_HOME/agent-deck/skills/pool"
	}
	return homeRelativeForDisplay(path)
}

// homeRelativeForDisplay abbreviates a path under the home directory to "~/…".
// These paths are shown inside fixed-height dialogs, where a long absolute
// home prefix wraps onto extra lines and pushes the dialog's last lines off
// screen.
func homeRelativeForDisplay(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	home = filepath.Clean(home)
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}
