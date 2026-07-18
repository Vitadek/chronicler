package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/db"
)

type ContextKey string

const (
	UserIDKey       ContextKey = "userID"
	SessionTokenKey ContextKey = "sessionToken"
	AuthViaKey      ContextKey = "authVia"
)

func GetUserID(ctx context.Context) string {
	val, _ := ctx.Value(UserIDKey).(string)
	return val
}

func GetSessionToken(ctx context.Context) string {
	val, _ := ctx.Value(SessionTokenKey).(string)
	return val
}

func GetAuthVia(ctx context.Context) string {
	val, _ := ctx.Value(AuthViaKey).(string)
	return val
}

type NextcloudOAuth struct {
	AccessToken  string
	RefreshToken string
	ExpiresInSec int
}

func GenerateRandomToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func CreateSession(database *sql.DB, cfg *config.Config, userID string, nc *NextcloudOAuth) (string, error) {
	token, err := GenerateRandomToken()
	if err != nil {
		return "", err
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)
	expiresAt := now + cfg.SessionTtlMs

	var ncAccessToken, ncRefreshToken *string
	var ncExpiresAt *int64

	if nc != nil {
		ncAccessToken = &nc.AccessToken
		ncRefreshToken = &nc.RefreshToken
		exp := now + int64(nc.ExpiresInSec*1000)
		ncExpiresAt = &exp
	}

	_, err = database.Exec(`
		INSERT INTO sessions (token, user_id, nc_access_token, nc_refresh_token, nc_expires_at, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, token, userID, ncAccessToken, ncRefreshToken, ncExpiresAt, expiresAt, now)
	if err != nil {
		return "", err
	}

	return token, nil
}

func RevokeSession(database *sql.DB, token string) error {
	_, err := database.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

func UpsertExternalUser(database *sql.DB, provider string, issuer string, externalID string, email *string, displayName *string) (string, error) {
	var id string
	err := database.QueryRow(`
		SELECT id FROM users
		WHERE external_provider = ? AND external_issuer = ? AND external_id = ?
	`, provider, issuer, externalID).Scan(&id)

	if err == nil {
		// User exists, update displayName/email if provided
		_, errUpdate := database.Exec(`
			UPDATE users
			   SET display_name = COALESCE(?, display_name),
			       email        = COALESCE(?, email)
			 WHERE id = ?
		`, displayName, email, id)
		return id, errUpdate
	} else if err != sql.ErrNoRows {
		return "", err
	}

	// Adopt-by-email check
	if email != nil && *email != "" {
		var byEmailID string
		errEmail := database.QueryRow("SELECT id FROM users WHERE email = ?", *email).Scan(&byEmailID)
		if errEmail == nil {
			_, errUpdate := database.Exec(`
				UPDATE users
				   SET external_provider = ?, external_issuer = ?, external_id = ?,
				       display_name = COALESCE(?, display_name)
				 WHERE id = ?
			`, provider, issuer, externalID, displayName, byEmailID)
			return byEmailID, errUpdate
		} else if errEmail != sql.ErrNoRows {
			return "", errEmail
		}
	}

	// Create new user
	rawBytes := make([]byte, 12)
	if _, errRand := rand.Read(rawBytes); errRand != nil {
		return "", errRand
	}
	id = base64.RawURLEncoding.EncodeToString(rawBytes)

	finalDisplayName := externalID
	if displayName != nil && *displayName != "" {
		finalDisplayName = *displayName
	}

	now := time.Now().UnixNano() / int64(time.Millisecond)
	_, errInsert := database.Exec(`
		INSERT INTO users (id, email, display_name, external_provider, external_issuer, external_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, email, finalDisplayName, provider, issuer, externalID, now)
	if errInsert != nil {
		return "", errInsert
	}

	return id, nil
}

func timingSafeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" || !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

func AuthMiddleware(cfg *config.Config, database *sql.DB) func(http.Handler) http.Handler {
	trustedFilter := ParseTrustedProxies(cfg.Auth.Forward.TrustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			switch cfg.Auth.Mode {
			case config.AuthModeNone:
				ctx = context.WithValue(ctx, UserIDKey, db.LocalUserID)
				ctx = context.WithValue(ctx, AuthViaKey, "none")
				next.ServeHTTP(w, r.WithContext(ctx))

			case config.AuthModeToken:
				t := bearerFromHeader(r)
				if t == "" || !timingSafeEq(t, cfg.Auth.Token) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "Invalid or missing token"})
					return
				}
				ctx = context.WithValue(ctx, UserIDKey, db.LocalUserID)
				ctx = context.WithValue(ctx, AuthViaKey, "token")
				next.ServeHTTP(w, r.WithContext(ctx))

			case config.AuthModeOIDC:
				t := bearerFromHeader(r)
				if t == "" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "Not authenticated", "loginUrl": "/api/auth/oidc/start"})
					return
				}

				var userID string
				var expiresAt int64
				err := database.QueryRow("SELECT user_id, expires_at FROM sessions WHERE token = ?", t).Scan(&userID, &expiresAt)
				now := time.Now().UnixNano() / int64(time.Millisecond)

				if err != nil || expiresAt < now {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "Session expired", "loginUrl": "/api/auth/oidc/start"})
					return
				}

				ctx = context.WithValue(ctx, UserIDKey, userID)
				ctx = context.WithValue(ctx, SessionTokenKey, t)
				ctx = context.WithValue(ctx, AuthViaKey, "oidc")
				next.ServeHTTP(w, r.WithContext(ctx))

			case config.AuthModeForward:
				// Validate peer IP matches trusted proxies
				remoteIP := r.RemoteAddr
				if !trustedFilter.Matches(remoteIP) {
					fmt.Printf("[auth/forward] rejected untrusted peer %s\n", remoteIP)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					json.NewEncoder(w).Encode(map[string]string{"error": "Untrusted proxy"})
					return
				}

				// Check optional shared secret
				if cfg.Auth.Forward.SharedSecret != "" {
					secHeader := cfg.Auth.Forward.SharedSecretHeader
					if secHeader == "" {
						secHeader = "X-Forward-Auth-Secret"
					}
					got := r.Header.Get(secHeader)
					if !timingSafeEq(got, cfg.Auth.Forward.SharedSecret) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						json.NewEncoder(w).Encode(map[string]string{"error": "Missing or bad shared secret"})
						return
					}
				}

				username := r.Header.Get(cfg.Auth.Forward.HeaderUser)
				if username == "" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "No identity header from proxy"})
					return
				}

				email := r.Header.Get(cfg.Auth.Forward.HeaderEmail)
				var emailPtr *string
				if email != "" {
					emailPtr = &email
				}

				displayName := r.Header.Get(cfg.Auth.Forward.HeaderName)
				var dispPtr *string
				if displayName != "" {
					dispPtr = &displayName
				} else {
					dispPtr = &username
				}

				userID, err := UpsertExternalUser(database, "forward", "proxy", username, emailPtr, dispPtr)
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}

				ctx = context.WithValue(ctx, UserIDKey, userID)
				ctx = context.WithValue(ctx, AuthViaKey, "forward")
				next.ServeHTTP(w, r.WithContext(ctx))
			}
		})
	}
}
