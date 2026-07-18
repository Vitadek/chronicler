package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type ServerRouter struct {
	cfg       *config.Config
	database  *sql.DB
	collabHub *collab.Hub
}

func NewServerRouter(cfg *config.Config, database *sql.DB, collabHub *collab.Hub) *ServerRouter {
	return &ServerRouter{
		cfg:       cfg,
		database:  database,
		collabHub: collabHub,
	}
}

func (sr *ServerRouter) Init() http.Handler {
	r := chi.NewRouter()

	// Base middlewares
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	// Collaboration WS dumb relay
	r.Handle("/collab", sr.collabHub)
	r.Handle("/collab/*", sr.collabHub)

	// Health check (unauthenticated)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":   true,
			"time": time.Now().UnixNano() / int64(time.Millisecond),
		})
	})

	// Readyz (database probe)
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		var one int
		err := sr.database.QueryRow("SELECT 1").Scan(&one)
		if err != nil {
			fmt.Printf("[readyz] database probe failed: %v\n", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ready":    false,
				"database": "unavailable",
				"error":    "Database unavailable",
				"time":     time.Now().UnixNano() / int64(time.Millisecond),
			})
			return
		}

		// Stub replica status for now (Phase 5 will implement full Nextcloud/S3 replica clients)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ready":    true,
			"database": "ready",
			"replica": map[string]interface{}{
				"provider":    sr.cfg.Storage.Replica,
				"state":       "healthy",
				"initialized": true,
				"pending":     0,
				"deadLetters": 0,
			},
			"time": time.Now().UnixNano() / int64(time.Millisecond),
		})
	})

	// Auth complete bounce page
	r.Get("/auth/complete", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html>
<html><head><meta charset="utf-8"><title>Signing in…</title></head>
<body style="font-family:system-ui;padding:2rem;color:#444">
<p>Signing you in…</p>
<script>
  (function() {
    var h = (location.hash || '').replace(/^#/, '');
    var p = new URLSearchParams(h);
    var t = p.get('token');
    if (t) localStorage.setItem('chronicle_token', t);
    location.replace('/');
  })();
</script>
</body></html>`))
	})

	// Mounting API sub-routers
	r.Route("/api", func(apiRouter chi.Router) {
		// Mount Auth router (Mixed endpoints, start & callback are unauthenticated)
		authH := NewAuthHandler(sr.cfg, sr.database)
		apiRouter.Mount("/auth", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Sub-router for auth
			subR := chi.NewRouter()
			authH.Mount(subR)
			subR.ServeHTTP(w, r)
		}))

		// Authenticated Routes
		apiRouter.Group(func(authGroup chi.Router) {
			authGroup.Use(auth.AuthMiddleware(sr.cfg, sr.database))

			// Sync Endpoints
			syncH := NewSyncHandler(sr.cfg, sr.database)
			authGroup.Mount("/sync", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				syncH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Manuscripts CRUD
			msH := NewManuscriptsHandler(sr.cfg, sr.database)
			authGroup.Mount("/manuscripts", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				msH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Settings Endpoints
			setH := NewSettingsHandler(sr.cfg, sr.database)
			authGroup.Mount("/settings", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				setH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// AI Endpoints
			aiH := NewAiHandler(sr.cfg, sr.database)
			authGroup.Mount("/ai", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				aiH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Covers Endpoints
			coversH := NewCoversHandler(sr.cfg, sr.database)
			authGroup.Mount("/covers", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				coversH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))
		})

		// 404 handler for unmatched /api routes
		apiRouter.NotFound(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "API endpoint not found"})
		})
	})

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Forward-Auth-Secret")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
