//go:build linux

package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func buildFileInfo(fullPath string, info os.FileInfo) FileInfo {
	fi := FileInfo{
		Name:        filepath.Base(fullPath),
		Path:        fullPath,
		Size:        info.Size(),
		IsDir:       info.IsDir(),
		Mode:        fmt.Sprintf("%04o", info.Mode().Perm()),
		Permissions: info.Mode().String(),
		Mtime:       info.ModTime().UTC().Format(time.RFC3339),
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		fi.Owner = fmt.Sprintf("%d", stat.Uid)
		fi.Group = fmt.Sprintf("%d", stat.Gid)
	}
	return fi
}
