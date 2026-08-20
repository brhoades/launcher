//go:build windows

package launcher

import "golang.org/x/sys/windows"

// Detects UAC elevation or when running as LocalSystem.
func runningElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
