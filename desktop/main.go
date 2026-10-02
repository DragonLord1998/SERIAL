package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title: "SERIAL", Width: 1280, Height: 820, MinWidth: 980, MinHeight: 660,
		BackgroundColour: options.NewRGBA(14, 16, 21, 255),
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup, OnDomReady: app.domReady, OnShutdown: app.shutdown,
		Bind: []interface{}{app},
		Mac:  &mac.Options{TitleBar: mac.TitleBarHiddenInset(), Appearance: mac.NSAppearanceNameDarkAqua, About: &mac.AboutInfo{Title: "SERIAL", Message: "Local anime library and player\nBuilt on GoAnime by alvarorichard and contributors"}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
