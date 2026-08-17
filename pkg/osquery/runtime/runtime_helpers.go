//go:build !windows

package runtime

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
)

func setpgid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// kill process group kills a process and all its children.
func killProcessGroup(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill process group %d: %w", cmd.Process.Pid, err)
	}
	return nil
}

func SocketPath(rootDir string, id string) string {
	return filepath.Join(rootDir, fmt.Sprintf("osquery-%s.sock", id))
}

// maxExtensionSocketPathLength is the longest usable path for a unix domain socket --
// `sun_path` in `sockaddr_un` is 108 bytes on Linux and 104 on darwin, and one byte is
// reserved for the terminating NUL. We use the smaller of the two everywhere so that
// a root directory that works on Linux also works on macOS.
const maxExtensionSocketPathLength = 103

// extensionSocketSuffixLength is the number of characters osquery appends to the
// extension manager socket path when it hands out a socket to a registered extension.
// osquery generates a uint16 route UUID, so the longest suffix is a period plus five
// digits -- see getExtensionSocket in osquery/extensions/extensions.h.
const extensionSocketSuffixLength = len(".65535")

// validateExtensionSocketPath returns an error if osquery (or a launcher extension
// registered with it) would be unable to bind a socket at socketPath.
//
// This has to account for the suffixed path, not just socketPath itself: osquery binds
// the manager socket at socketPath, but each registered extension -- including launcher's
// own -- binds at `socketPath.<uuid>`. A path that is short enough for the manager socket
// but too long once suffixed fails in a confusing way. osquery creates the manager socket,
// waits out `--extensions_timeout` for the required kolide_grpc extension that can never
// register, then tears the manager socket back down and exits, so the visible symptom is
// osquery deleting its own socket rather than a path length problem.
func validateExtensionSocketPath(socketPath string) error {
	if maxLength := maxExtensionSocketPathLength - extensionSocketSuffixLength; len(socketPath) > maxLength {
		return fmt.Errorf("extension socket path %s is %d characters, which exceeds the maximum of %d -- use a shorter root directory", socketPath, len(socketPath), maxLength)
	}

	return nil
}

func platformArgs() []string {
	return nil
}

func isExitOk(_ error) bool {
	return false
}
