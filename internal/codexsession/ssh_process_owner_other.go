//go:build !linux

package codexsession

import "os"

func sshPathOwnerTrusted(os.FileInfo) bool { return true }
