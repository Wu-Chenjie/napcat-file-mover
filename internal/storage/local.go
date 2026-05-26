package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"napcat-file-mover/internal/security"
)

type LocalFileInfo struct {
	Name   string
	Path   string
	Size   int64
	SHA256 string
}

type Local struct {
	root string
}

func NewLocal(root string) *Local { return &Local{root: root} }

func (l *Local) ListLocalFiles(_ context.Context) ([]LocalFileInfo, error) {
	entries, err := os.ReadDir(l.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []LocalFileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(l.root, e.Name())
		sha, err := fileSHA256(p)
		if err != nil {
			continue
		}
		out = append(out, LocalFileInfo{
			Name:   info.Name(),
			Path:   filepath.Clean(p),
			Size:   info.Size(),
			SHA256: sha,
		})
	}
	return out, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (l *Local) PutFile(ctx context.Context, localPath, name string) (string, error) {
	if err := os.MkdirAll(l.root, 0o755); err != nil {
		return "", err
	}
	dst := security.SafeJoin(l.root, name)
	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return filepath.Clean(dst), ctx.Err()
}
