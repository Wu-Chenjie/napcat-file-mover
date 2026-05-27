package queue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"napcat-file-mover/internal/config"
)

type RedisStream struct {
	client *redis.Client
	cfg    config.RedisConfig
}

type Message struct {
	ID     string
	TaskID int64
}

func NewRedisStream(ctx context.Context, cfg config.RedisConfig) (*RedisStream, error) {
	if cfg.Addr == "" {
		return nil, errors.New("redis.addr is required")
	}
	r := &RedisStream{
		client: redis.NewClient(&redis.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB}),
		cfg:    cfg,
	}
	if err := r.client.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	if err := r.client.XGroupCreateMkStream(ctx, cfg.Stream, cfg.ConsumerGroup, "0").Err(); err != nil && !isBusyGroup(err) {
		return nil, err
	}
	return r, nil
}

func (r *RedisStream) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Close()
}

func (r *RedisStream) Enqueue(ctx context.Context, taskID int64) error {
	if r == nil {
		return nil
	}
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.cfg.Stream,
		Values: map[string]any{"task_id": strconv.FormatInt(taskID, 10)},
	}).Err()
}

func (r *RedisStream) Read(ctx context.Context, block time.Duration, count int64) ([]Message, error) {
	if r == nil {
		return nil, nil
	}
	if count <= 0 {
		count = 1
	}
	streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    r.cfg.ConsumerGroup,
		Consumer: r.cfg.ConsumerName,
		Streams:  []string{r.cfg.Stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	var out []Message
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			taskID, _ := strconv.ParseInt(fmt.Sprint(msg.Values["task_id"]), 10, 64)
			if taskID > 0 {
				out = append(out, Message{ID: msg.ID, TaskID: taskID})
			}
		}
	}
	return out, nil
}

func (r *RedisStream) Ack(ctx context.Context, messageID string) error {
	if r == nil || messageID == "" {
		return nil
	}
	return r.client.XAck(ctx, r.cfg.Stream, r.cfg.ConsumerGroup, messageID).Err()
}

func isBusyGroup(err error) bool {
	return err != nil && (err.Error() == "BUSYGROUP Consumer Group name already exists" ||
		err.Error() == "BUSYGROUP Consumer Group name already exists ")
}
