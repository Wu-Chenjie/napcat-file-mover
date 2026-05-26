package websource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ErrUnsupported = errors.New("unsupported web source")

type File struct {
	Name     string
	Path     string
	URL      string
	Size     int64
	Modified time.Time
}

type Options struct {
	HTTPClient            *http.Client
	FireworksListEndpoint string
	FireworksDownloadBase string
	GitHubAPIBase         string
	GitHubRawBase         string
	HOARawBase            string
	HOAPageBase           string
}

type Resolver struct {
	httpClient            *http.Client
	fireworksListEndpoint string
	fireworksDownloadBase string
	githubAPIBase         string
	githubRawBase         string
	hoaRawBase            string
	hoaPageBase           string
}

func NewResolver(opts Options) *Resolver {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Resolver{
		httpClient:            client,
		fireworksListEndpoint: defaultString(opts.FireworksListEndpoint, "https://olist-eo.jwyihao.top/api/fs/list"),
		fireworksDownloadBase: strings.TrimRight(defaultString(opts.FireworksDownloadBase, "https://olist-eo.jwyihao.top/d/Fireworks"), "/"),
		githubAPIBase:         strings.TrimRight(defaultString(opts.GitHubAPIBase, "https://api.github.com"), "/"),
		githubRawBase:         strings.TrimRight(defaultString(opts.GitHubRawBase, "https://raw.githubusercontent.com"), "/"),
		hoaRawBase:            strings.TrimRight(defaultString(opts.HOARawBase, "https://gh.hoa.moe/github.com"), "/"),
		hoaPageBase:           strings.TrimRight(opts.HOAPageBase, "/"),
	}
}

func (r *Resolver) Resolve(ctx context.Context, rawURL string) ([]File, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, ErrUnsupported
	}
	switch strings.ToLower(u.Hostname()) {
	case "fireworks.jwyihao.top":
		return r.resolveFireworks(ctx, u)
	case "hoa.moe":
		return r.resolveHOA(ctx, u)
	case "gh.hoa.moe":
		return r.resolveHOARaw(u)
	case "github.com":
		return r.resolveGitHub(ctx, u, false)
	default:
		return nil, ErrUnsupported
	}
}

var defaultRepos = []struct{ Owner, Repo, Ref string }{
	{"HITLittleZheng", "HITCS", "main"},
}

func (r *Resolver) SearchWeb(ctx context.Context, query string, limit int) ([]File, error) {
	type result struct {
		files []File
		err   error
		name  string
	}
	jobs := []func(context.Context) result{
		func(ctx context.Context) result {
			files, err := r.searchFireworks(ctx, query)
			return result{files: files, err: err, name: "fireworks"}
		},
	}
	for _, repo := range defaultRepos {
		repo := repo
		jobs = append(jobs, func(ctx context.Context) result {
			files, err := r.searchGitHubRepo(ctx, repo.Owner, repo.Repo, repo.Ref, query, false)
			return result{files: files, err: err, name: "github " + repo.Owner + "/" + repo.Repo}
		})
	}

	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		i, job := i, job
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = job(ctx)
		}()
	}
	wg.Wait()

	var all []File
	for _, result := range results {
		if result.err != nil {
			log.Printf("%s search: %v", result.name, result.err)
			continue
		}
		all = append(all, result.files...)
	}

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (r *Resolver) searchGitHubRepo(ctx context.Context, owner, repo, ref, query string, useHOARaw bool) ([]File, error) {
	files, err := r.listGitHubTree(ctx, owner, repo, ref, "", useHOARaw)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(query)
	var out []File
	for _, f := range files {
		if strings.Contains(strings.ToLower(f.Name), query) || strings.Contains(strings.ToLower(f.Path), query) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (r *Resolver) searchFireworks(ctx context.Context, query string) ([]File, error) {
	files, err := r.listFireworks(ctx, "/Fireworks", "")
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(query)
	var out []File
	for _, f := range files {
		if strings.Contains(strings.ToLower(f.Name), query) || strings.Contains(strings.ToLower(f.Path), query) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (r *Resolver) resolveFireworks(ctx context.Context, u *url.URL) ([]File, error) {
	rel := strings.Trim(strings.TrimSuffix(u.EscapedPath(), "/index"), "/")
	if rel == "" {
		rel = ""
	} else if decoded, err := url.PathUnescape(rel); err == nil {
		rel = decoded
	}
	listPath := "/Fireworks"
	if rel != "" {
		listPath += "/" + rel
	}
	return r.listFireworks(ctx, listPath, rel)
}

func (r *Resolver) listFireworks(ctx context.Context, listPath, rel string) ([]File, error) {
	body, _ := json.Marshal(map[string]string{"path": listPath, "password": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.fireworksListEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Content []struct {
				Name     string `json:"name"`
				IsDir    bool   `json:"is_dir"`
				Size     int64  `json:"size"`
				Modified string `json:"modified"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.Code != 200 {
		return nil, fmt.Errorf("fireworks list failed: http=%d code=%d %s", resp.StatusCode, out.Code, out.Message)
	}
	results := make([][]File, len(out.Data.Content))
	errs := make([]error, len(out.Data.Content))
	var wg sync.WaitGroup
	for i, item := range out.Data.Content {
		i, item := i, item
		itemRel := path.Join(rel, item.Name)
		if item.IsDir {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = r.listFireworks(ctx, path.Join(listPath, item.Name), itemRel)
			}()
			continue
		}
		modified, _ := time.Parse(time.RFC3339, item.Modified)
		results[i] = []File{{
			Name:     item.Name,
			Path:     itemRel,
			URL:      r.fireworksDownloadBase + "/" + encodePath(itemRel),
			Size:     item.Size,
			Modified: modified,
		}}
	}
	wg.Wait()
	files := []File{}
	for i, child := range results {
		if errs[i] != nil {
			return nil, errs[i]
		}
		files = append(files, child...)
	}
	return files, nil
}

func (r *Resolver) resolveHOA(ctx context.Context, u *url.URL) ([]File, error) {
	parts := cleanParts(u.Path)
	if len(parts) == 0 {
		return nil, ErrUnsupported
	}
	repo := parts[len(parts)-1]
	if repo == "" {
		return nil, ErrUnsupported
	}
	files, err := r.listGitHubTree(ctx, "HITSZ-OpenAuto", repo, "main", "", true)
	if err == nil && len(files) > 0 {
		return files, nil
	}
	pageFiles, pageErr := r.fetchHOAPageFiles(ctx, u)
	if pageErr == nil && len(pageFiles) > 0 {
		return pageFiles, nil
	}
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (r *Resolver) resolveHOARaw(u *url.URL) ([]File, error) {
	file, ok := fileFromHOARawURL(u.String())
	if !ok {
		return nil, ErrUnsupported
	}
	return []File{file}, nil
}

func (r *Resolver) fetchHOAPageFiles(ctx context.Context, u *url.URL) ([]File, error) {
	pageURL := u.String()
	if r.hoaPageBase != "" {
		base, err := url.Parse(r.hoaPageBase)
		if err != nil {
			return nil, err
		}
		page := *u
		page.Scheme = base.Scheme
		page.Host = base.Host
		pageURL = page.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NapCatFileMover/0.1")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hoa page failed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	files := extractHOARawFiles(string(body))
	if len(files) == 0 {
		return nil, ErrUnsupported
	}
	return files, nil
}

var hoaRawURLPattern = regexp.MustCompile(`https://gh\.hoa\.moe/github\.com/[^"'\\<>\s]+`)

func extractHOARawFiles(page string) []File {
	decoded := html.UnescapeString(page)
	decoded = strings.NewReplacer(
		`\/`, `/`,
		`\u002F`, `/`,
		`\u002f`, `/`,
		`\u0026`, `&`,
	).Replace(decoded)
	matches := hoaRawURLPattern.FindAllString(decoded, -1)
	files := make([]File, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		if seen[match] {
			continue
		}
		file, ok := fileFromHOARawURL(match)
		if !ok || !isLikelyDocument(file.Name) {
			continue
		}
		seen[match] = true
		files = append(files, file)
	}
	return files
}

func fileFromHOARawURL(rawURL string) (File, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || strings.ToLower(u.Hostname()) != "gh.hoa.moe" {
		return File{}, false
	}
	parts := cleanParts(u.Path)
	if len(parts) < 6 || parts[0] != "github.com" || parts[3] != "raw" {
		return File{}, false
	}
	filePath := strings.Join(parts[5:], "/")
	if filePath == "" {
		return File{}, false
	}
	return File{
		Name: path.Base(filePath),
		Path: filePath,
		URL:  rawURL,
	}, true
}

func isLikelyDocument(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".txt", ".md", ".csv", ".zip", ".rar", ".7z", ".tar", ".gz":
		return true
	default:
		return false
	}
}

func (r *Resolver) resolveGitHub(ctx context.Context, u *url.URL, useHOARaw bool) ([]File, error) {
	parts := cleanParts(u.Path)
	if len(parts) < 2 {
		return nil, ErrUnsupported
	}
	owner, repo := parts[0], parts[1]
	ref := "main"
	prefix := ""
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		ref = parts[3]
		prefix = strings.Join(parts[4:], "/")
		if parts[2] == "blob" {
			name := path.Base(prefix)
			return []File{{Name: name, Path: prefix, URL: r.githubRawURL(owner, repo, ref, prefix, useHOARaw)}}, nil
		}
	}
	return r.listGitHubTree(ctx, owner, repo, ref, prefix, useHOARaw)
}

func (r *Resolver) listGitHubTree(ctx context.Context, owner, repo, ref, prefix string, useHOARaw bool) ([]File, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", r.githubAPIBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NapCatFileMover/0.1")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github tree failed: %s", resp.Status)
	}
	prefix = strings.Trim(prefix, "/")
	files := []File{}
	for _, item := range out.Tree {
		if item.Type != "blob" {
			continue
		}
		if prefix != "" && item.Path != prefix && !strings.HasPrefix(item.Path, prefix+"/") {
			continue
		}
		files = append(files, File{
			Name: path.Base(item.Path),
			Path: item.Path,
			URL:  r.githubRawURL(owner, repo, ref, item.Path, useHOARaw),
			Size: item.Size,
		})
	}
	return files, nil
}

func (r *Resolver) githubRawURL(owner, repo, ref, filePath string, useHOARaw bool) string {
	if useHOARaw {
		return fmt.Sprintf("%s/%s/%s/raw/%s/%s", r.hoaRawBase, encodePath(owner), encodePath(repo), encodePath(ref), encodePath(filePath))
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s", r.githubRawBase, encodePath(owner), encodePath(repo), encodePath(ref), encodePath(filePath))
}

func cleanParts(p string) []string {
	raw := strings.Split(strings.Trim(p, "/"), "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if decoded, err := url.PathUnescape(part); err == nil {
			part = decoded
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func encodePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func defaultString(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
