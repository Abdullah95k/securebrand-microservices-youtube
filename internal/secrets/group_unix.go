//go:build unix

package secrets

import (
	"os"
	"syscall"
)

// fileGroup returns the file's group ID.
func fileGroup(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Gid), true
}
