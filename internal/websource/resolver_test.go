package websource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveFireworksDirectoryURL(t *testing.T) {
	var seenPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		seenPaths = append(seenPaths, req.Path)
		w.Header().Set("Content-Type", "application/json")
		switch req.Path {
		case "/Fireworks/【公共课】/课程笔记":
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[{"name":"微积分上","is_dir":true,"size":0,"modified":"2026-01-02T03:04:05+08:00"},{"name":"总复习.pdf","is_dir":false,"size":1024,"modified":"2026-01-02T03:04:05+08:00"}]}}`))
		case "/Fireworks/【公共课】/课程笔记/微积分上":
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[{"name":"讲义.pdf","is_dir":false,"size":2048,"modified":"2026-01-03T03:04:05+08:00"}]}}`))
		default:
			t.Fatalf("unexpected list path: %s", req.Path)
		}
	}))
	defer server.Close()

	resolver := NewResolver(Options{
		HTTPClient:            server.Client(),
		FireworksListEndpoint: server.URL + "/api/fs/list",
		FireworksDownloadBase: "https://olist-eo.jwyihao.top/d/Fireworks",
	})
	files, err := resolver.Resolve(context.Background(), "https://fireworks.jwyihao.top/%E3%80%90%E5%85%AC%E5%85%B1%E8%AF%BE%E3%80%91/%E8%AF%BE%E7%A8%8B%E7%AC%94%E8%AE%B0/")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %#v", len(files), files)
	}
	if seenPaths[0] != "/Fireworks/【公共课】/课程笔记" || seenPaths[1] != "/Fireworks/【公共课】/课程笔记/微积分上" {
		t.Fatalf("unexpected paths: %#v", seenPaths)
	}
	if files[0].Name != "讲义.pdf" || files[0].Size != 2048 {
		t.Fatalf("unexpected first file: %#v", files[0])
	}
	wantURL := "https://olist-eo.jwyihao.top/d/Fireworks/%E3%80%90%E5%85%AC%E5%85%B1%E8%AF%BE%E3%80%91/%E8%AF%BE%E7%A8%8B%E7%AC%94%E8%AE%B0/%E5%BE%AE%E7%A7%AF%E5%88%86%E4%B8%8A/%E8%AE%B2%E4%B9%89.pdf"
	if files[0].URL != wantURL {
		t.Fatalf("unexpected download URL:\nwant %s\n got %s", wantURL, files[0].URL)
	}
}

func TestResolveGitHubRepositoryURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/HITLittleZheng/HITCS/git/trees/main" || r.URL.Query().Get("recursive") != "1" {
			t.Fatalf("unexpected request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tree":[{"path":"README.md","type":"blob","size":12},{"path":"操作系统/讲义.pdf","type":"blob","size":4096},{"path":"操作系统","type":"tree"}]}`))
	}))
	defer server.Close()

	resolver := NewResolver(Options{
		HTTPClient:    server.Client(),
		GitHubAPIBase: server.URL,
	})
	files, err := resolver.Resolve(context.Background(), "https://github.com/HITLittleZheng/HITCS/tree/main/%E6%93%8D%E4%BD%9C%E7%B3%BB%E7%BB%9F")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %#v", len(files), files)
	}
	if files[0].Name != "讲义.pdf" || files[0].Path != "操作系统/讲义.pdf" {
		t.Fatalf("unexpected file: %#v", files[0])
	}
	wantURL := "https://raw.githubusercontent.com/HITLittleZheng/HITCS/main/%E6%93%8D%E4%BD%9C%E7%B3%BB%E7%BB%9F/%E8%AE%B2%E4%B9%89.pdf"
	if files[0].URL != wantURL {
		t.Fatalf("unexpected raw URL:\nwant %s\n got %s", wantURL, files[0].URL)
	}
}

func TestResolveHOACourseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/HITSZ-OpenAuto/PHYS1002/git/trees/main" || r.URL.Query().Get("recursive") != "1" {
			t.Fatalf("unexpected request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tree":[{"path":"Exp01/报告.pdf","type":"blob","size":8192}]}`))
	}))
	defer server.Close()

	resolver := NewResolver(Options{
		HTTPClient:    server.Client(),
		GitHubAPIBase: server.URL,
	})
	files, err := resolver.Resolve(context.Background(), "https://hoa.moe/docs/2025/25L51/fresh-spring/PHYS1002")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %#v", len(files), files)
	}
	wantURL := "https://gh.hoa.moe/github.com/HITSZ-OpenAuto/PHYS1002/raw/main/Exp01/%E6%8A%A5%E5%91%8A.pdf"
	if files[0].URL != wantURL {
		t.Fatalf("unexpected HOA raw URL:\nwant %s\n got %s", wantURL, files[0].URL)
	}
}
