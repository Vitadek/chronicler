//go:build !headless && cgo

package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/webview/webview_go"
)

const guiSupported = true

func runGUI(server *http.Server, addr string) {
	fmt.Printf("Launching Chronicler UI window...\n")
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Chronicler Workstation")
	w.SetSize(1200, 800, webview.HintNone)
	w.Navigate(fmt.Sprintf("http://%s", addr))
	w.Run()

	// WebView window closed: shut down server
	fmt.Println("[shutdown] UI window closed; stopping server")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
