//go:build headless

package main

import (
	"fmt"
	"net/http"
)

const guiSupported = false

func runGUI(server *http.Server, addr string) {
	fmt.Println("GUI mode is disabled in this build.")
}
