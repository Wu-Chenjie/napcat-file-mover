package storage

import "context"

type Storage interface {
	PutFile(ctx context.Context, localPath, name string) (string, error)
}

type LocalFileLister interface {
	ListLocalFiles(ctx context.Context) ([]LocalFileInfo, error)
}
