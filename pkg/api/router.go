package api

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/collab"
	"chronicle-server/pkg/config"
	"chronicle-server/pkg/replica"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type ServerRouter struct {
	cfg            *config.Config
	database       *sql.DB
	collabHub      *collab.Hub
	replicaManager *replica.Manager
	webFS          embed.FS
}

func NewServerRouter(cfg *config.Config, database *sql.DB, collabHub *collab.Hub, replicaManager *replica.Manager, webFS embed.FS) *ServerRouter {
	return &ServerRouter{
		cfg:            cfg,
		database:       database,
		collabHub:      collabHub,
		replicaManager: replicaManager,
		webFS:          webFS,
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

		// This endpoint is intentionally unauthenticated for orchestrators, so
		// it exposes a deliberately narrow projection of the replica status —
		// NOT the whole thing. GetStatus() carries lastError, which contains
		// provider exception text and can leak internal hostnames, bucket
		// names or account details. The authenticated CLI (`chronicle-server
		// status`) keeps the full diagnostic.
		//
		// Passing the map through wholesale was a real leak, caught by
		// tests/formal ("readiness reports SQLite and healthy S3 without
		// leaking endpoint details", which asserts lastError is absent and
		// that the body never mentions the S3 endpoint host).
		repStatus := map[string]interface{}{
			"provider":    "none",
			"state":       "disabled",
			"initialized": true,
			"pending":     0,
			"deadLetters": 0,
		}
		if sr.replicaManager != nil {
			full := sr.replicaManager.GetStatus()
			for _, k := range []string{"provider", "state", "initialized", "pending", "deadLetters"} {
				repStatus[k] = full[k]
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ready":    true,
			"database": "ready",
			"replica":  repStatus,
			"time":     time.Now().UnixNano() / int64(time.Millisecond),
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

			// Covers Endpoints
			coversH := NewCoversHandler(sr.cfg, sr.database)
			authGroup.Mount("/covers", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				coversH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Grammar Endpoints — the built-in prose checker that replaces the
			// Node server's LanguageTool sidecar proxy. A load failure is
			// logged, not fatal: the handler then returns 503 and the editor
			// carries on without squiggles.
			grammarH, grammarErr := NewGrammarHandler()
			if grammarErr != nil {
				log.Printf("[grammar] dictionary unavailable, checker disabled: %v", grammarErr)
			}
			authGroup.Mount("/grammar", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				grammarH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Plugins Endpoints
			pluginsH := NewPluginsHandler(sr.cfg, sr.database)
			authGroup.Mount("/plugins", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				pluginsH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Hugo Publish Endpoints — lets plugins commit/push manuscript
			// content to an external Hugo site repo. No DB dependency: the
			// git credential travels per-request from the calling plugin,
			// never persisted here.
			hugoH := NewHugoPublishHandler()
			authGroup.Mount("/hugopublish", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subR := chi.NewRouter()
				hugoH.Mount(subR)
				subR.ServeHTTP(w, r)
			}))

			// Backup Endpoints (only enabled if LocalAdmin is true)
			if sr.cfg.LocalAdmin {
				backupH := NewBackupHandler(sr.cfg, sr.database)
				authGroup.Mount("/backup", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					subR := chi.NewRouter()
					backupH.Mount(subR)
					subR.ServeHTTP(w, r)
				}))
			}
		})

		// 404 handler for unmatched /api routes
		apiRouter.NotFound(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "API endpoint not found"})
		})
	})

	// Serve static web interface assets (Fallback for SPA client-side routing)
	subFS, err := fs.Sub(sr.webFS, "web")
	if err == nil {
		fileServer := http.FileServer(http.FS(subFS))
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			path := req.URL.Path
			// Check if file exists in embed FS
			if f, errOpen := subFS.Open(strings.TrimPrefix(path, "/")); errOpen == nil {
				f.Close()
				// Everything under /assets/ is content-hashed by vite, so a
				// given URL's bytes never change and it can be cached
				// permanently. index.html must NOT be: it is the only
				// unhashed file, and it is what points at the current asset
				// hashes — caching it would pin clients to a stale build
				// across deploys.
				setStaticCacheHeaders(w, path)
				fileServer.ServeHTTP(w, req)
				return
			}
			// Otherwise serve index.html (fallback for SPA client-side routing)
			w.Header().Set("Cache-Control", "no-cache")
			req.URL.Path = "/"
			fileServer.ServeHTTP(w, req)
		})
	}

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

// setStaticCacheHeaders mirrors the cache policy the Node server used
// (server/index.ts): content-hashed assets are immutable and cached for a
// year; index.html is never cached, because it is the unhashed entry point
// that references the current asset hashes.
func setStaticCacheHeaders(w http.ResponseWriter, urlPath string) {
	if strings.HasPrefix(urlPath, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	if urlPath == "/" || strings.HasSuffix(urlPath, "/index.html") {
		w.Header().Set("Cache-Control", "no-cache")
	}
}
