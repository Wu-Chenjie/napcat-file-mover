package search

import (
	"context"
	"os"
	"testing"
	"time"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/repository"
)

func TestOllamaBgeM3Integration(t *testing.T) {
	if os.Getenv("OLLAMA_INTEGRATION") != "1" {
		t.Skip("set OLLAMA_INTEGRATION=1 to run against a local Ollama bge-m3 service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := NewOllamaClient(config.SemanticSearchConfig{
		Endpoint:       envDefault("OLLAMA_ENDPOINT", "http://127.0.0.1:11434"),
		Model:          envDefault("OLLAMA_MODEL", "bge-m3"),
		TimeoutSeconds: 120,
	})
	query, err := client.Embed(ctx, "高等数学期末复习资料")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	if len(query) < 100 {
		t.Fatalf("embedding dimension too small: %d", len(query))
	}

	mathVec, err := client.Embed(ctx, "高数期末复习讲义 PDF")
	if err != nil {
		t.Fatalf("embed math candidate: %v", err)
	}
	musicVec, err := client.Embed(ctx, "流行音乐吉他谱合集")
	if err != nil {
		t.Fatalf("embed unrelated candidate: %v", err)
	}

	idx := NewVectorIndex([]repository.FileCatalog{
		{ID: 1, FileName: "高数期末复习讲义.pdf", Ext: ".pdf", EmbeddingJSON: EncodeVector(mathVec)},
		{ID: 2, FileName: "流行音乐吉他谱合集.zip", Ext: ".zip", EmbeddingJSON: EncodeVector(musicVec)},
	})
	results := idx.Search(query, 0, "", 2)
	if len(results) < 2 {
		t.Fatalf("expected two semantic results, got %d", len(results))
	}
	if results[0].ID != 1 {
		t.Fatalf("expected math document first, got id=%d score=%.4f", results[0].ID, results[0].Score)
	}
	if results[0].Score <= results[1].Score {
		t.Fatalf("expected top score to be greater, got %.4f <= %.4f", results[0].Score, results[1].Score)
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
