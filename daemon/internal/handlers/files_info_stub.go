//go:build !linux

package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func buildFileInfo(fullPath string, info os.FileInfo) FileInfo {
	return FileInfo{
		Name:        filepath.Base(fullPath),
		Path:        fullPath,
		Size:        info.Size(),
		IsDir:       info.IsDir(),
		Mode:        fmt.Sprintf("%04o", info.Mode().Perm()),
		Permissions: info.Mode().String(),
		Mtime:       info.ModTime().UTC().Format(time.RFC3339),
		Owner:       "0",
		Group:       "0",
	}
}
