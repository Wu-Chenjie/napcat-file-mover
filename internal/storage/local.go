package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"napcat-file-mover/internal/security"
)

type Local struct {
	root string
}

func NewLocal(root string) *Local { return &Local{root: root} }

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
