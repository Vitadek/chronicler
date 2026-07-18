package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chronicle-server/pkg/api"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Fatal configuration error: %v\n", err)
		os.Exit(1)
	}

	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		fmt.Printf("Fatal database error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Start database garbage collection routine
	db.StartGCLoop()

	// Create and initialize the central HTTP router
	router := api.NewServerRouter(cfg, database)
	handler := router.Init()

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	go func() {
		fmt.Printf("Chronicle server listening on http://%s\n", addr)
		fmt.Printf("  data dir: %s\n", cfg.DataDir)
		fmt.Printf("  auth mode: %s\n", cfg.Auth.Mode)
		if errServe := server.ListenAndServe(); errServe != nil && errServe != http.ErrServerClosed {
			fmt.Printf("Server listen error: %v\n", errServe)
		}
	}()

	// Graceful shutdown listener
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	fmt.Println("\n[shutdown] signal received; closing Chronicle")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if errShutdown := server.Shutdown(ctx); errShutdown != nil {
		fmt.Printf("Graceful shutdown failed: %v\n", errShutdown)
		os.Exit(1)
	}

	fmt.Println("Chronicle stopped successfully")
}
