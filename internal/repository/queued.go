package repository

import "context"

type TaskEnqueuer interface {
	Enqueue(ctx context.Context, taskID int64) error
}

type QueuedStore struct {
	Store
	queue TaskEnqueuer
}

func NewQueuedStore(base Store, queue TaskEnqueuer) *QueuedStore {
	return &QueuedStore{Store: base, queue: queue}
}

func (q *QueuedStore) CreateTask(ctx context.Context, t *Task) (int64, error) {
	id, err := q.Store.CreateTask(ctx, t)
	if err != nil || id == 0 || q.queue == nil {
		return id, err
	}
	return id, q.queue.Enqueue(ctx, id)
}
