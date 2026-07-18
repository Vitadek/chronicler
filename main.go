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
	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
	"chronicle-server/pkg/replica"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Fatal configuration error: %v\n", err)
		os.Exit(1)
	}

	// Apply staged backup import swap if pending
	db.ApplyPendingImport(cfg.DataDir)

	database, err := db.InitDB(cfg.DataDir)
	if err != nil {
		fmt.Printf("Fatal database error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create replica manager
	repManager, err := replica.NewManager(cfg, database)
	if err != nil {
		fmt.Printf("Fatal replica error: %v\n", err)
		os.Exit(1)
	}
	defer repManager.Close()

	// If a command line administrative subcommand is requested, execute it and exit
	if len(os.Args) > 1 {
		if replica.RunCLI(cfg, database, repManager, os.Args[1:]) {
			return
		}
	}

	// Start database garbage collection routine
	db.StartGCLoop()

	// Create collaboration WS dumb relay
	collabHub := collab.NewHub(database, cfg)
	defer collabHub.Close()

	// Reconcile replica target on startup (seeds/cleans queue if configuration changed)
	changed, seeded := repManager.ReconcileReplicaTarget()
	if changed {
		fmt.Printf("[replica] Target changed, seeded %d objects into manifest\n", seeded)
	}

	// Start replica manager background queue drain
	repManager.Start()

	// Initialize AI service key validators and cache
	api.InitAI(cfg)

	// Create and initialize the central HTTP router
	router := api.NewServerRouter(cfg, database, collabHub, repManager)
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
