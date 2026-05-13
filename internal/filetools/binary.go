package filetools

import (
	"path/filepath"
	"strings"
)

const MaxInlineImageBytes = 20 * 1024 * 1024

var binaryExtensions = map[string]struct{}{
	".7z":    {},
	".a":     {},
	".bin":   {},
	".bz2":   {},
	".class": {},
	".dll":   {},
	".dmg":   {},
	".dylib": {},
	".exe":   {},
	".gz":    {},
	".ico":   {},
	".iso":   {},
	".jar":   {},
	".o":     {},
	".pdf":   {},
	".rar":   {},
	".so":    {},
	".tar":   {},
	".tgz":   {},
	".war":   {},
	".xz":    {},
	".zip":   {},
}

var blockedDeviceFiles = map[string]struct{}{
	"/dev/full":    {},
	"/dev/null":    {},
	"/dev/random":  {},
	"/dev/urandom": {},
	"/dev/zero":    {},
}

func IsBinaryExtension(path string) bool {
	_, ok := binaryExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

func IsBlockedDeviceFile(path string) bool {
	_, ok := blockedDeviceFiles[filepath.Clean(path)]
	return ok
}

func IsSupportedImageExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func IsSupportedImageMime(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}
