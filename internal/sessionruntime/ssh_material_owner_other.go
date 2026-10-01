//go:build !linux

package sessionruntime

import "os"

func materialOwnerTrusted(os.FileInfo) bool { return false }
