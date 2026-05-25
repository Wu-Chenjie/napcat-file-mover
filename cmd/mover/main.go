package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"napcat-file-mover/internal/app"
	"napcat-file-mover/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	core, err := app.New(cfg)
	if err != nil {
		log.Fatalf("create app: %v", err)
	}

	if err := core.Start(ctx); err != nil {
		log.Fatalf("start app: %v", err)
	}
	<-ctx.Done()
	core.Stop(context.Background())
}
