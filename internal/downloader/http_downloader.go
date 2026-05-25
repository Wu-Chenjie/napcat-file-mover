package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type HTTPDownloader struct {
	client      *http.Client
	userAgent   string
	maxBytes    int64
	bufferPool  sync.Pool
	bufferBytes int
}

type Result struct {
	Path        string
	SHA256      string
	Size        int64
	ContentType string
}

func New(userAgent string, maxBytes int64, bufferBytes int) *HTTPDownloader {
	if bufferBytes <= 0 {
		bufferBytes = 512 * 1024
	}
	tr := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 128,
		MaxConnsPerHost:     128,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &HTTPDownloader{
		client:      &http.Client{Transport: tr},
		userAgent:   userAgent,
		maxBytes:    maxBytes,
		bufferBytes: bufferBytes,
		bufferPool: sync.Pool{New: func() any {
			return make([]byte, bufferBytes)
		}},
	}
}

func (d *HTTPDownloader) Download(ctx context.Context, url, dst string) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	if d.userAgent != "" {
		req.Header.Set("User-Agent", d.userAgent)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("download bad status: %s", resp.Status)
	}
	if d.maxBytes > 0 && resp.ContentLength > d.maxBytes {
		return Result{}, fmt.Errorf("file too large: %d > %d", resp.ContentLength, d.maxBytes)
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	h := sha256.New()
	reader := io.Reader(resp.Body)
	if d.maxBytes > 0 {
		reader = io.LimitReader(resp.Body, d.maxBytes+1)
	}
	buf := d.bufferPool.Get().([]byte)
	defer d.bufferPool.Put(buf)
	n, err := io.CopyBuffer(io.MultiWriter(f, h), reader, buf)
	if err != nil {
		return Result{}, err
	}
	if d.maxBytes > 0 && n > d.maxBytes {
		return Result{}, fmt.Errorf("file too large: %d > %d", n, d.maxBytes)
	}
	if err := f.Sync(); err != nil {
		return Result{}, err
	}
	if err := f.Close(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(part, dst); err != nil {
		return Result{}, err
	}
	return Result{Path: dst, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, ContentType: resp.Header.Get("Content-Type")}, nil
}
