//go:build !unix

package secrets

import "os"

// fileGroup is unknown here, so a group-readable file is refused.
func fileGroup(os.FileInfo) (int, bool) { return 0, false }
