package worker

import (
	"context"
	"sync"
)

type SemaphoreLimiter struct {
	mu          sync.Mutex
	limits      map[string]chan struct{}
	defaultSize int
}

func NewLimiter(defaultSize int) *SemaphoreLimiter {
	if defaultSize <= 0 {
		defaultSize = 1
	}
	return &SemaphoreLimiter{limits: map[string]chan struct{}{}, defaultSize: defaultSize}
}

func (l *SemaphoreLimiter) Add(key string, size int) {
	if size <= 0 {
		size = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limits[key] = make(chan struct{}, size)
}

func (l *SemaphoreLimiter) Wait(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	ch := l.limits[key]
	if ch == nil {
		ch = make(chan struct{}, l.defaultSize)
		l.limits[key] = ch
	}
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
