package search

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
	"golang.org/x/text/unicode/norm"
)

type IndexedText struct {
	Normalized string
	Pinyin     string
	Initials   string
	NGrams     string
}

func BuildIndexedText(parts ...string) IndexedText {
	normalized := Normalize(strings.Join(parts, " "))
	py, initials := Pinyin(normalized)
	return IndexedText{
		Normalized: normalized,
		Pinyin:     py,
		Initials:   initials,
		NGrams:     strings.Join(NGrams(normalized, 2), " "),
	}
}

func Normalize(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
			prevSpace = false
			continue
		}
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func Pinyin(s string) (string, string) {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	words := pinyin.Pinyin(s, args)
	full := make([]string, 0, len(words))
	initials := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) == 0 {
			continue
		}
		full = append(full, word[0])
		r := []rune(word[0])
		if len(r) > 0 {
			initials = append(initials, string(r[0]))
		}
	}
	return strings.Join(full, ""), strings.Join(initials, "")
}

func NGrams(s string, n int) []string {
	runes := []rune(strings.ReplaceAll(s, " ", ""))
	if len(runes) <= n {
		if len(runes) == 0 {
			return nil
		}
		return []string{string(runes)}
	}
	out := make([]string, 0, len(runes)-n+1)
	for i := 0; i+n <= len(runes); i++ {
		out = append(out, string(runes[i:i+n]))
	}
	return out
}
