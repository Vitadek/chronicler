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

// ForwardResolver authenticates requests made by a trusted forward-auth proxy.
// It is shared by the HTTP middleware and the collaboration WebSocket handshake
// so both entry points enforce exactly the same trust boundary.
type ForwardResolver struct {
	cfg           *config.Config
	database      *sql.DB
	trustedFilter *IPFilter
}

func NewForwardResolver(cfg *config.Config, database *sql.DB) *ForwardResolver {
	return &ForwardResolver{
		cfg:           cfg,
		database:      database,
		trustedFilter: ParseTrustedProxies(cfg.Auth.Forward.TrustedProxies),
	}
}

// Resolve returns the durable Chronicle user ID. On failure, status and message
// are safe to return to the client.
func (fr *ForwardResolver) Resolve(r *http.Request) (userID string, status int, message string) {
	if !fr.trustedFilter.Matches(r.RemoteAddr) {
		fmt.Printf("[auth/forward] rejected untrusted peer %s\n", r.RemoteAddr)
		return "", http.StatusForbidden, "Untrusted proxy"
	}

	if fr.cfg.Auth.Forward.SharedSecret != "" {
		secretHeader := fr.cfg.Auth.Forward.SharedSecretHeader
		if secretHeader == "" {
			secretHeader = "X-Forward-Auth-Secret"
		}
		if !timingSafeEq(r.Header.Get(secretHeader), fr.cfg.Auth.Forward.SharedSecret) {
			return "", http.StatusForbidden, "Missing or bad shared secret"
		}
	}

	username := r.Header.Get(fr.cfg.Auth.Forward.HeaderUser)
	if username == "" {
		return "", http.StatusUnauthorized, "No identity header from proxy"
	}

	email := r.Header.Get(fr.cfg.Auth.Forward.HeaderEmail)
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}

	displayName := r.Header.Get(fr.cfg.Auth.Forward.HeaderName)
	var displayNamePtr *string
	if displayName != "" {
		displayNamePtr = &displayName
	} else {
		displayNamePtr = &username
	}

	userID, err := UpsertExternalUser(fr.database, "forward", "proxy", username, emailPtr, displayNamePtr)
	if err != nil {
		return "", http.StatusInternalServerError, err.Error()
	}
	return userID, 0, ""
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
	forwardResolver := NewForwardResolver(cfg, database)

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
				userID, status, message := forwardResolver.Resolve(r)
				if status != 0 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(map[string]string{"error": message})
					return
				}

				ctx = context.WithValue(ctx, UserIDKey, userID)
				ctx = context.WithValue(ctx, AuthViaKey, "forward")
				next.ServeHTTP(w, r.WithContext(ctx))
			}
		})
	}
}
