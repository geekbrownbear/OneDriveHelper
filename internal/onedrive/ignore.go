package onedrive

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

// IgnoredFoldersKey lists folders a technician has decided to keep even
// though OneDrive no longer syncs them (for example a copy of an archived
// SharePoint site the user wants for reference). Each value is the full path
// (name) with a note (data). It lives under HKLM so only administrators can
// add to it; every user's detection reads it.
const IgnoredFoldersKey = `SOFTWARE\OneDriveHelper\IgnoredFolders`

// IgnoredFolder is one entry of the ignore list.
type IgnoredFolder struct {
	Path string
	Note string // "<who/when>: <reason>", free text
}

// IgnoredFolders reads the ignore list (empty when the key doesn't exist).
func IgnoredFolders() []IgnoredFolder {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, IgnoredFoldersKey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	out := make([]IgnoredFolder, 0, len(names))
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		note, _, _ := k.GetStringValue(n)
		out = append(out, IgnoredFolder{Path: filepath.Clean(n), Note: note})
	}
	sort.Slice(out, func(i, j int) bool { return Clean(out[i].Path) < Clean(out[j].Path) })
	return out
}

// IgnoredPaths returns the ignore list as cleaned paths.
func IgnoredPaths() []string {
	var out []string
	for _, f := range IgnoredFolders() {
		out = append(out, Clean(f.Path))
	}
	return out
}

// AddIgnoredFolder adds (or updates) an entry. Requires administrator rights.
func AddIgnoredFolder(path, note string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || len(path) <= 3 || strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf("%q is not an absolute folder path", path)
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, IgnoredFoldersKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	// Keep the existing spelling if the same folder is already listed.
	if existing, _ := k.ReadValueNames(-1); existing != nil {
		for _, n := range existing {
			if Clean(n) == Clean(path) {
				path = n
			}
		}
	}
	if strings.TrimSpace(note) == "" {
		note = "ignored " + time.Now().Format("2006-01-02")
	}
	return k.SetStringValue(path, note)
}

// RemoveIgnoredFolder deletes an entry. Requires administrator rights.
func RemoveIgnoredFolder(path string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, IgnoredFoldersKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("%s is not on the ignore list", path)
		}
		return err
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	for _, n := range names {
		if Clean(n) == Clean(path) {
			return k.DeleteValue(n)
		}
	}
	return fmt.Errorf("%s is not on the ignore list", path)
}
