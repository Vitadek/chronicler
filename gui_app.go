//go:build !headless

package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
)

const guiSupported = true

func runGUI(server *http.Server, addr string) {
	targetURL := fmt.Sprintf("http://%s", addr)
	fmt.Printf("Launching Chronicler Workstation app window at %s...\n", targetURL)

	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "linux":
		// Prefer native GTK Epiphany (GNOME Web) app mode, then Chromium/Chrome/Brave standalone app mode
		if path, err := exec.LookPath("epiphany"); err == nil {
			cmd = exec.Command(path, fmt.Sprintf("--app=%s", targetURL))
		} else if path, err := exec.LookPath("google-chrome"); err == nil {
			cmd = exec.Command(path, fmt.Sprintf("--app=%s", targetURL))
		} else if path, err := exec.LookPath("chromium"); err == nil {
			cmd = exec.Command(path, fmt.Sprintf("--app=%s", targetURL))
		} else if path, err := exec.LookPath("chromium-browser"); err == nil {
			cmd = exec.Command(path, fmt.Sprintf("--app=%s", targetURL))
		} else if path, err := exec.LookPath("brave-browser"); err == nil {
			cmd = exec.Command(path, fmt.Sprintf("--app=%s", targetURL))
		} else {
			cmd = exec.Command("xdg-open", targetURL)
		}
	case "darwin":
		cmd = exec.Command("open", targetURL)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}

	if err := cmd.Start(); err != nil {
		fmt.Printf("Failed to launch application window: %v\n", err)
	}
}
