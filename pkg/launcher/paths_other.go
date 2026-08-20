//go:build !windows

package launcher

// Unreachable on non-Windows, included for compilation.
func runningElevated() bool {
	return false
}
