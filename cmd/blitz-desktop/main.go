// Command blitz-desktop is Blitz's desktop app: a window with a
// tab per workspace, driving the per-user Blitz service.
package main

import (
	"embed"
	"log"

	"github.com/retail-cortex/blitz/internal/server"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// dist is the page, built from web/desktop.
//
//go:embed all:dist
var dist embed.FS

func main() {
	app := &App{socket: server.DefaultSocket()}
	err := wails.Run(&options.App{
		Title:     "Blitz",
		Width:     1100,
		Height:    760,
		MinWidth:  640,
		MinHeight: 420,
		AssetServer: &assetserver.Options{
			Assets:  dist,
			Handler: serviceProxy(app.socket),
		},
		OnStartup: app.startup,
		Bind:      []any{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
