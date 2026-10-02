//go:build !linux

package sessionruntime

import "errors"

func renameArtifactDirectoryNoReplace(string, string) error {
	return errors.New("atomic no-replace artifact promotion requires Linux")
}
