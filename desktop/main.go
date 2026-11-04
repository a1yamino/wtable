package main

import (
	"context"
	"embed"
	"os"
	"os/signal"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"wtable/internal/config"
	"wtable/internal/server"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		runDaemon()
		return
	}
	app := NewApp()
	err := wails.Run(&options.App{
		Title:     "wtable",
		Width:     1440,
		Height:    900,
		MinWidth:  1120,
		MinHeight: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 21, G: 23, B: 26, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.NSAppearanceNameDarkAqua,
			About: &mac.AboutInfo{
				Title:   "wtable",
				Message: "A visual terminal workbench for coding agents.",
			},
		},
	})
	if err != nil {
		println("wtable desktop:", err.Error())
	}
}

func runDaemon() {
	paths, err := config.Resolve()
	if err != nil {
		println("wtable daemon:", err.Error())
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = server.Run(ctx, paths); err != nil {
		println("wtable daemon:", err.Error())
		os.Exit(1)
	}
}
