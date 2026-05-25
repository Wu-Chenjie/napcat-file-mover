package security

import (
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	windowsForbidden = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)
	spaces           = regexp.MustCompile(`\s+`)
	underscores      = regexp.MustCompile(`_+`)
	reservedNames    = map[string]struct{}{
		"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
		"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
		"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
	}
)

func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = norm.NFC.String(name)
	name = strings.ReplaceAll(name, "..", "_")
	name = windowsForbidden.ReplaceAllString(name, "_")
	name = underscores.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .\t\r\n")
	name = spaces.ReplaceAllString(name, " ")
	if name == "" || name == "." {
		return "unnamed"
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if _, ok := reservedNames[strings.ToUpper(base)]; ok {
		name = "_" + name
	}
	return trimFilename(name, 180)
}

func trimFilename(name string, maxRunes int) string {
	runes := []rune(name)
	if len(runes) <= maxRunes {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	extRunes := []rune(ext)
	limit := maxRunes - len(extRunes)
	if limit < 16 {
		limit = maxRunes
		ext = ""
	}
	baseRunes := []rune(base)
	if len(baseRunes) > limit {
		baseRunes = baseRunes[:limit]
	}
	return string(baseRunes) + ext
}

func SafeJoin(root, name string) string {
	return filepath.Join(root, SanitizeFilename(name))
}
