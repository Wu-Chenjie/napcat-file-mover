package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/repository"
	"napcat-file-mover/internal/search"
)

func TestSemanticSearchHTTPIntegration(t *testing.T) {
	if os.Getenv("OLLAMA_INTEGRATION") != "1" {
		t.Skip("set OLLAMA_INTEGRATION=1 to run against a local Ollama bge-m3 service")
	}
	base := t.TempDir()
	cfg := config.Default(config.Paths{
		BaseDir:  base,
		Config:   filepath.Join(base, "config.yaml"),
		Database: filepath.Join(base, "mover.db"),
		CacheDir: filepath.Join(base, "cache"),
		FilesDir: filepath.Join(base, "files"),
		LogDir:   filepath.Join(base, "logs"),
	})
	cfg.Search.Semantic.Enabled = true
	cfg.Search.Semantic.Endpoint = envDefault("OLLAMA_ENDPOINT", "http://127.0.0.1:11434")
	cfg.Search.Semantic.Model = envDefault("OLLAMA_MODEL", "bge-m3")
	cfg.Search.Semantic.TimeoutSeconds = 120
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Storage.LocalRoot = cfg.Paths.FilesDir

	core, err := New(cfg)
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	defer core.Stop(context.Background())

	for _, item := range []repository.FileCatalog{
		catalogItem(1, "高数期末复习讲义.pdf", ".pdf"),
		catalogItem(2, "流行音乐吉他谱合集.zip", ".zip"),
	} {
		if err := core.repo.UpsertCatalog(context.Background(), item); err != nil {
			t.Fatalf("upsert catalog: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	core.refreshSemanticIndex(ctx)

	req := httptest.NewRequest(http.MethodGet, "/api/search/files?q=%E9%AB%98%E7%AD%89%E6%95%B0%E5%AD%A6%E6%9C%9F%E6%9C%AB%E5%A4%8D%E4%B9%A0%E8%B5%84%E6%96%99&limit=2", nil)
	rec := httptest.NewRecorder()
	core.searchFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Results []repository.SearchResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Results) == 0 {
		t.Fatal("expected search results")
	}
	if body.Results[0].FileName != "高数期末复习讲义.pdf" {
		t.Fatalf("expected math file first, got %+v", body.Results[0])
	}
	if body.Results[0].MatchedBy != "semantic" && body.Results[0].MatchedBy != "text+semantic" {
		t.Fatalf("expected semantic match, got matched_by=%q", body.Results[0].MatchedBy)
	}
}

func catalogItem(id int64, name, ext string) repository.FileCatalog {
	idx := search.BuildIndexedText(name)
	return repository.FileCatalog{
		GroupID:        12345,
		FileID:         name,
		BusID:          int32(id),
		FileName:       name,
		Ext:            ext,
		FileSize:       1024,
		NormalizedText: idx.Normalized,
		Pinyin:         idx.Pinyin,
		Initials:       idx.Initials,
		NGrams:         idx.NGrams,
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
