package search

import (
	"strings"
	"testing"
)

func TestBuildIndexedText(t *testing.T) {
	idx := BuildIndexedText("高数 期末-复习资料.pdf")
	if !strings.Contains(idx.Normalized, "高数") {
		t.Fatalf("normalized missing chinese text: %q", idx.Normalized)
	}
	if idx.Pinyin == "" || idx.Initials == "" {
		t.Fatalf("expected pinyin and initials, got %+v", idx)
	}
	if idx.NGrams == "" {
		t.Fatal("expected ngrams")
	}
}
