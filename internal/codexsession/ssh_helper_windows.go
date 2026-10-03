package codexsession

import (
	"context"
	"errors"
)

// RunSSHBootstrapHelperInstall is unavailable on Windows because the pinned
// bootstrap receiver requires the Linux Controller.
func RunSSHBootstrapHelperInstall(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, body []byte) (string, error) {
	return "", errors.New("pinned bootstrap helper transport requires a Linux Controller")
}

// RunSSHBootstrapHelperObserve is unavailable on Windows because the pinned
// bootstrap receiver requires the Linux Controller.
func RunSSHBootstrapHelperObserve(ctx context.Context, config SSHAppServerConfig, generationDigest, helperSHA256 string, size int64) (string, error) {
	return "", errors.New("pinned bootstrap helper transport requires a Linux Controller")
}
