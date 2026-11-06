package backend

import (
	"os"
	"regexp"
	"testing"
)

// TestVersionMatchesBuildConfig keeps the version the backend reports and the
// one the packagers read from build/config.yml from drifting apart.
func TestVersionMatchesBuildConfig(t *testing.T) {
	data, err := os.ReadFile("../build/config.yml")
	if err != nil {
		t.Fatalf("read build config: %v", err)
	}

	m := regexp.MustCompile(`(?m)^\s*version:\s*"([^"]+)"`).FindSubmatch(data)
	if m == nil {
		t.Fatal("build/config.yml has no quoted version entry")
	}
	if got := string(m[1]); got != Version {
		t.Fatalf("build/config.yml version = %q, want %q", got, Version)
	}
}
