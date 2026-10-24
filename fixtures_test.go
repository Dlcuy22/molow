package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// engineRoot resolves the molo module's source directory, so fixtures come from
// the engine itself rather than a copy that drifts.
var engineRoot = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/dlcuy22/molo").Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
})

// engineFixture returns a file's path inside the molo module.
func engineFixture(t *testing.T, rel string) string {
	t.Helper()

	root, err := engineRoot()
	if err != nil {
		t.Fatalf("resolve the molo module directory: %v", err)
	}
	if root == "" {
		t.Fatal("the molo module has no directory, so its fixtures cannot be read")
	}

	return filepath.Join(root, filepath.FromSlash(rel))
}
