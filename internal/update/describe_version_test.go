package update

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCompareVersions_GitDescribeBuilds pins how git-describe build versions
// ("<tag>-<n>-g<hex>[-dirty]", n commits past the release) order against
// releases and each other. Ordinary semver pre-releases must keep sorting
// below their release.
func TestCompareVersions_GitDescribeBuilds(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"describe build above its release", "1.16.28-29-gbe8bf483", "1.16.28", 1},
		{"describe build with v prefix above its release", "v1.16.28-29-gbe8bf483", "v1.16.28", 1},
		{"dirty describe build above its release", "1.16.28-29-gbe8bf483-dirty", "1.16.28", 1},
		{"describe build below next release", "1.16.28-29-gbe8bf483", "1.16.29", -1},
		{"describe builds order by commit distance", "1.16.28-29-gbe8bf483", "1.16.28-30-gabc1234", -1},
		{"identical describe builds are equal", "1.16.28-29-gbe8bf483", "1.16.28-29-gbe8bf483", 0},
		{"regular pre-release stays below release", "1.16.4-switch-preview.abc", "1.16.4", -1},
		{"regular pre-release stays below describe build", "1.16.28-rc.1", "1.16.28-29-gbe8bf483", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CompareVersions(tt.a, tt.b))
			if tt.want != 0 {
				assert.Equal(t, -tt.want, CompareVersions(tt.b, tt.a))
			}
		})
	}
}
