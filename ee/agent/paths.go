package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// TempPath returns a path within the agent's temp directory tree, by joining the path elements into a single path.
func TempPath(elem ...string) string {
	elements := append([]string{os.TempDir()}, elem...)
	return filepath.Join(elements...)
}

// MkdirTemp creates a new temporary directory in the TempPath directory.
func MkdirTemp(pattern string) (string, error) {
	return os.MkdirTemp(TempPath(), pattern)
}

// CheckDirWritable returns an error if the current process cannot create files in dir.
// We test by actually creating a file rather than by inspecting the mode bits, so that
// the answer accounts for ownership, supplementary groups, and read-only mounts.
func CheckDirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".launcher-writable-check")
	if err != nil {
		return fmt.Errorf("creating file in %s: %w", dir, err)
	}
	defer os.Remove(f.Name())

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing file in %s: %w", dir, err)
	}

	return nil
}
