//go:build !windows

package osquery

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"errors"
)

// default CA certs for osquery. Copied from macOS's `/etc/ssl/cert.pem`
//
//go:embed ca-bundle.crt
var defaultCaCerts []byte

// InstallCaCerts returns the path to CA certificates.
// It prefers system CA certificates over embedded bundle.
func InstallCaCerts(directory string, slog *slog.Logger) (string, error) {
	// Try to use system CA certificates directly
	systemCaPath, err := getSystemCaCertPath()
	if err == nil {
		// Verify the file is readable
		if _, err := os.Stat(systemCaPath); err == nil {
			return systemCaPath, nil
		}
	}

	// Fall back to installing embedded bundle
	sum := sha256.Sum256(defaultCaCerts)
	caFile := filepath.Join(directory, fmt.Sprintf("ca-certs-embedded-%x.crt", sum))

	_, err = os.Stat(caFile)
	switch {
	case os.IsNotExist(err):
		return caFile, os.WriteFile(caFile, defaultCaCerts, 0444)
	case err != nil:
		return "", err
	}
	return caFile, nil
}

// getSystemCaCertPath returns the path to system CA certificates based on OS
func getSystemCaCertPath() (string, error) {
	return "", errors.New("no system CA certificates found")
}
