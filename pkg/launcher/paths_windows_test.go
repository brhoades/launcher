//go:build windows

package launcher

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/kolide/launcher/v2/pkg/log/multislogger"
	"github.com/stretchr/testify/require"
)

const kolideServerURLForTest = "k2device.kolide.com"

// Subtests are not parallel because each one points likelyWindowsRootDirPaths at its own
// well-known directory, and that's a single package-level slot.
func TestDetermineRootDirectoryOverrideWindows(t *testing.T) { //nolint:paralleltest
	for _, tt := range []struct { //nolint:paralleltest
		testCaseName string
		serverURL    string
		// setup preps and returns the provided and expected dirs
		setup func(t *testing.T) (passedRootDir string, expectedRootDir string)
	}{
		{
			testCaseName: "non-kolide server url passthrough",
			serverURL:    "https://example.com",
			setup: func(t *testing.T) (string, string) {
				setLikelyPaths(t, wellKnownDir(t, "launcher db contents"))

				optsRootDir := filepath.Join("some", "dir", "somewhere")
				return optsRootDir, optsRootDir
			},
		},
		{
			testCaseName: "database already in passed directory passthrough",
			serverURL:    kolideServerURLForTest,
			setup: func(t *testing.T) (string, string) {
				setLikelyPaths(t, wellKnownDir(t, "launcher db contents"))

				optsRootDir := t.TempDir()
				writeDB(t, optsRootDir, "launcher db contents")
				return optsRootDir, optsRootDir
			},
		},
		{
			testCaseName: "root directory cannot be checked passthrough",
			serverURL:    kolideServerURLForTest,
			setup: func(t *testing.T) (string, string) {
				setLikelyPaths(t, wellKnownDir(t, "launcher db contents"))

				// the NUL byte makes os.Stat fail with EINVAL rather than a not-exist error
				optsRootDir := filepath.Join(t.TempDir(), "root\x00dir")
				return optsRootDir, optsRootDir
			},
		},
		{
			testCaseName: "all well-known empty databases passthrough",
			serverURL:    kolideServerURLForTest,
			setup: func(t *testing.T) (string, string) {
				setLikelyPaths(t, wellKnownDir(t, ""))

				optsRootDir := filepath.Join(t.TempDir(), "does-not-exist")
				return optsRootDir, optsRootDir
			},
		},
		{
			testCaseName: "well-known databases absent passthrough",
			serverURL:    kolideServerURLForTest,
			setup: func(t *testing.T) (string, string) {
				setLikelyPaths(t, wellKnownDirNoDB(t))

				optsRootDir := filepath.Join(t.TempDir(), "does-not-exist")
				return optsRootDir, optsRootDir
			},
		},
		{
			testCaseName: "well-known unwritable passthrough",
			serverURL:    kolideServerURLForTest,
			setup: func(t *testing.T) (string, string) {
				overrideDir := wellKnownDir(t, "launcher db contents")
				denyWriteAccess(t, filepath.Join(overrideDir, "launcher.db"))
				setLikelyPaths(t, overrideDir)

				optsRootDir := filepath.Join(t.TempDir(), "does-not-exist")
				return optsRootDir, optsRootDir
			},
		},
		{
			testCaseName: "well-known database works returns override",
			serverURL:    kolideServerURLForTest,
			setup: func(t *testing.T) (string, string) {
				overrideDir := wellKnownDir(t, "launcher db contents")
				setLikelyPaths(t, overrideDir)

				return filepath.Join(t.TempDir(), "does-not-exist"), overrideDir
			},
		},
	} {
		t.Run(tt.testCaseName, func(t *testing.T) { //nolint:paralleltest
			optsRootDir, expectedRootDir := tt.setup(t)

			require.Equal(
				t,
				expectedRootDir,
				DetermineRootDirectoryOverride(multislogger.NewNopLogger(), optsRootDir, tt.serverURL, ""),
				"incorrect root directory",
			)
		})
	}
}

func setLikelyPaths(t *testing.T, paths ...string) {
	original := likelyWindowsRootDirPaths
	t.Cleanup(func() { likelyWindowsRootDirPaths = original })
	likelyWindowsRootDirPaths = paths
}

// wellKnownDirNoDB returns a stand-in for one of the likelyWindowsRootDirPaths -- the
// package identifier must appear in the path for it to be considered a candidate.
func wellKnownDirNoDB(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "Kolide", "Launcher-"+DefaultLauncherIdentifier, "data")
	require.NoError(t, os.MkdirAll(dir, 0755))
	return dir
}

func wellKnownDir(t *testing.T, dbContents string) string {
	dir := wellKnownDirNoDB(t)
	writeDB(t, dir, dbContents)
	return dir
}

func writeDB(t *testing.T, dir string, contents string) {
	require.NoError(t, os.WriteFile(filepath.Join(dir, "launcher.db"), []byte(contents), 0644))
}

// sets a deny ACE for the current user on the provided path.
func denyWriteAccess(t *testing.T, path string) {
	currentUser, err := user.Current()
	require.NoError(t, err)

	require.NoError(t, exec.CommandContext(context.TODO(), "C:\\Windows\\System32\\icacls.exe", path, "/deny", currentUser.Username+":(W)").Run()) //nolint:forbidigo // Fine to use exec.CommandContext in test
	t.Cleanup(func() {
		_ = exec.CommandContext(context.TODO(), "C:\\Windows\\System32\\icacls.exe", path, "/remove:d", currentUser.Username).Run() //nolint:forbidigo // Fine to use exec.CommandContext in test
	})
}
