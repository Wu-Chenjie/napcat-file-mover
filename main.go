package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"napcat-file-mover/internal/desktop"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	svc, err := desktop.NewService()
	if err != nil {
		log.Fatal(err)
	}
	err = wails.Run(&options.App{
		Title:  "NapCat File Mover",
		Width:  1200,
		Height: 800,
		OnStartup: func(ctx context.Context) {
			_ = svc.Start()
		},
		OnShutdown: func(ctx context.Context) {
			svc.Stop()
		},
		AssetServer: &assetserver.Options{Assets: assets},
		Bind:        []any{svc},
	})
	if err != nil {
		log.Fatal(err)
	}
}
