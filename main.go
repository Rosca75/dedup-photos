// =============================================================================
// main.go — Wails v2 entry point for the DedupPhotos desktop application
// =============================================================================
//
// Wails v2 creates a native desktop window powered by the system WebView
// (WebView2 on Windows, WebKit on macOS/Linux). The Go backend exposes
// methods directly to JavaScript — no HTTP server, no network ports.
//
// HOW WAILS WORKS:
//   1. wails.Run() creates the native window and loads index.html from the
//      embedded filesystem.
//   2. The Wails runtime is automatically injected into the page. This makes
//      all public methods on *App available as window.go.main.App.MethodName().
//   3. JS calls Go methods asynchronously (they return Promises).
//   4. Go structs are automatically serialised to/from JavaScript objects.
//
// BUILD COMMANDS:
//   wails build          — Production binary for the current OS
//   wails dev            — Development mode with live reload
//   go build ./...       — Verify Go compilation only (no Wails window)
// =============================================================================

package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// assets embeds the entire static/ directory into the compiled binary.
// "all:" includes hidden files and empty directories (needed for Wails).
//
//go:embed all:static
var assets embed.FS

// main is the entry point. It creates the Wails application window and
// registers the App struct so its methods are callable from JavaScript.
func main() {
	// Create the App instance. All public methods on *App will be exposed
	// to the JS frontend as window.go.main.App.MethodName().
	app := NewApp()

	// fs.Sub strips the "static/" prefix from the embedded filesystem.
	// Without this, files would be served at /static/css/... instead of /css/...
	// Wails looks for index.html at the root of the provided FS.
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("Failed to create sub-filesystem: %v", err)
	}

	// wails.Run() creates the window and starts the application event loop.
	// This function blocks until the window is closed.
	err = wails.Run(&options.App{
		Title:     "DedupPhotos — Duplicate Photo Finder",
		Width:     1280,
		Height:    900,
		MinWidth:  900,
		MinHeight: 600,

		// AssetServer serves the embedded frontend files (HTML, CSS, JS).
		// After sub-FS stripping, static/index.html is served as the root page,
		// static/css/base.css is at /css/base.css, etc.
		AssetServer: &assetserver.Options{
			Assets: sub,
		},

		// OnStartup is called after the window is created but before the page loads.
		// We pass the Wails context to the App so it can call runtime APIs.
		OnStartup: app.startup,

		// Bind registers *App so all its public methods appear as
		// window.go.main.App.* in the JavaScript frontend.
		Bind: []interface{}{app},
	})

	if err != nil {
		log.Fatalf("Error starting application: %v", err)
	}
}
