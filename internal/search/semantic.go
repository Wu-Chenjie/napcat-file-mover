package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"napcat-file-mover/internal/config"
	"napcat-file-mover/internal/repository"
)

type OllamaClient struct {
	endpoint string
	model    string
	http     *http.Client
}

func NewOllamaClient(cfg config.SemanticSearchConfig) *OllamaClient {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &OllamaClient{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		model:    cfg.Model,
		http:     &http.Client{Timeout: timeout},
	}
}

func (c *OllamaClient) Embed(ctx context.Context, text string) ([]float64, error) {
	reqBody := map[string]any{"model": c.model, "input": text}
	b, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/embed", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama embed bad status: %s", resp.Status)
	}
	var out struct {
		Embeddings [][]float64 `json:"embeddings"`
		Embedding  []float64   `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) > 0 {
		return out.Embeddings[0], nil
	}
	if len(out.Embedding) > 0 {
		return out.Embedding, nil
	}
	return nil, fmt.Errorf("ollama response has no embedding")
}

type VectorIndex struct {
	items []VectorItem
}

type VectorItem struct {
	Catalog repository.FileCatalog
	Vector  []float64
}

func NewVectorIndex(catalog []repository.FileCatalog) *VectorIndex {
	idx := &VectorIndex{}
	for _, item := range catalog {
		var vec []float64
		if item.EmbeddingJSON == "" {
			continue
		}
		if err := json.Unmarshal([]byte(item.EmbeddingJSON), &vec); err != nil || len(vec) == 0 {
			continue
		}
		idx.items = append(idx.items, VectorItem{Catalog: item, Vector: vec})
	}
	return idx
}

func (v *VectorIndex) Search(query []float64, groupID int64, ext string, limit int) []repository.SearchResult {
	if v == nil || len(query) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	var out []repository.SearchResult
	for _, item := range v.items {
		if groupID != 0 && item.Catalog.GroupID != groupID {
			continue
		}
		if ext != "" && item.Catalog.Ext != ext {
			continue
		}
		score := cosine(query, item.Vector)
		if score <= 0 {
			continue
		}
		out = append(out, repository.SearchResult{
			FileCatalog:   item.Catalog,
			Score:         score,
			SemanticScore: score,
			MatchedBy:     "semantic",
			Reason:        "ollama",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func MergeResults(textResults, semanticResults []repository.SearchResult, limit int) []repository.SearchResult {
	byID := map[int64]repository.SearchResult{}
	for _, r := range textResults {
		r.TextScore = max(r.TextScore, r.Score)
		if r.MatchedBy == "" {
			r.MatchedBy = "text"
		}
		byID[r.ID] = r
	}
	for _, r := range semanticResults {
		existing, ok := byID[r.ID]
		if ok {
			existing.SemanticScore = r.SemanticScore
			existing.Score = max(existing.TextScore, r.SemanticScore)
			existing.MatchedBy = "text+semantic"
			existing.Reason = strings.Trim(existing.Reason+"+"+r.Reason, "+")
			byID[r.ID] = existing
			continue
		}
		byID[r.ID] = r
	}
	out := make([]repository.SearchResult, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func EncodeVector(v []float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
