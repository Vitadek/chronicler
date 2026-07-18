package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"chronicle-server/pkg/auth"
	"chronicle-server/pkg/config"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
)

type AuthHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewAuthHandler(cfg *config.Config, database *sql.DB) *AuthHandler {
	return &AuthHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *AuthHandler) Mount(r chi.Router) {
	r.Get("/config", h.getConfig)
	r.Get("/oidc/start", h.startOIDC)
	r.Get("/oidc/callback", h.callbackOIDC)
	r.Get("/nextcloud/start", h.startNextcloud)
	r.Get("/nextcloud/callback", h.callbackNextcloud)
	r.Get("/me", h.getMe)
	r.Post("/logout", h.logout)
}

func (h *AuthHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	aiAvailable := h.cfg.OpenAIKey != "" || h.cfg.AnthropicKey != "" || h.cfg.GeminiKey != ""
	out := map[string]interface{}{
		"mode":        h.cfg.Auth.Mode,
		"aiAvailable": aiAvailable,
		"aiProviders": map[string]bool{
			"openai":    h.cfg.OpenAIKey != "",
			"anthropic": h.cfg.AnthropicKey != "",
			"gemini":    h.cfg.GeminiKey != "",
		},
	}

	if h.cfg.Auth.Mode == config.AuthModeOIDC {
		out["loginUrl"] = "/api/auth/oidc/start"
		out["logoutUrl"] = "/api/auth/logout"
	} else if h.cfg.Auth.Mode == config.AuthModeToken {
		out["requiresToken"] = true
	}

	if h.cfg.Nextcloud.Enabled {
		out["nextcloudConnectUrl"] = "/api/auth/nextcloud/start"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

type oidcKVState struct {
	CodeVerifier string `json:"codeVerifier"`
	Nonce        string `json:"nonce"`
}

func (h *AuthHandler) startOIDC(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Auth.Mode != config.AuthModeOIDC {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "OIDC not enabled"})
		return
	}

	ctx := r.Context()
	helper, err := auth.GetOIDCHelper(ctx, h.cfg)
	if err != nil {
		fmt.Printf("[auth/oidc] get OIDC client failed: %v\n", err)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("OIDC provider unreachable. Try again in a moment."))
		return
	}

	codeVerifier := oauth2.GenerateVerifier()
	codeChallenge := oauth2.S256ChallengeFromVerifier(codeVerifier)
	
	stateBytes := make([]byte, 24)
	rand.Read(stateBytes)
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	nonceBytes := make([]byte, 24)
	rand.Read(nonceBytes)
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	kvVal, _ := json.Marshal(oidcKVState{CodeVerifier: codeVerifier, Nonce: nonce})
	expiresAt := time.Now().UnixNano()/int64(time.Millisecond) + 10*60*1000 // 10 mins

	_, err = h.database.Exec("INSERT INTO kv (k, v, expires_at) VALUES (?, ?, ?)", "oidc:"+state, string(kvVal), expiresAt)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	authURL := helper.Oauth2Config.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("nonce", nonce),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *AuthHandler) callbackOIDC(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Auth.Mode != config.AuthModeOIDC {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "OIDC not enabled"})
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if state == "" || code == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Missing state or code"))
		return
	}

	var jsonVal string
	var expiresAt int64
	err := h.database.QueryRow("SELECT v, expires_at FROM kv WHERE k = ?", "oidc:"+state).Scan(&jsonVal, &expiresAt)
	now := time.Now().UnixNano() / int64(time.Millisecond)

	if err != nil || expiresAt < now {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid or expired state"))
		return
	}

	h.database.Exec("DELETE FROM kv WHERE k = ?", "oidc:"+state)

	var kvState oidcKVState
	if err := json.Unmarshal([]byte(jsonVal), &kvState); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to parse state metadata"))
		return
	}

	ctx := r.Context()
	helper, err := auth.GetOIDCHelper(ctx, h.cfg)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("OIDC helper error"))
		return
	}

	token, err := helper.Oauth2Config.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", kvState.CodeVerifier))
	if err != nil {
		fmt.Printf("[auth/oidc] exchange failed: %v\n", err)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("OIDC token exchange failed"))
		return
	}

	// Verify nonce match
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("OIDC response missing id_token"))
		return
	}
	idToken, errVerify := helper.Verifier.Verify(ctx, rawIDToken)
	if errVerify != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("ID Token verification failed"))
		return
	}
	if idToken.Nonce != kvState.Nonce {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Nonce mismatch"))
		return
	}

	user, errExt := auth.ExtractUser(ctx, helper, token)
	if errExt != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to extract claims: " + errExt.Error()))
		return
	}

	userId, errUpsert := auth.UpsertExternalUser(h.database, "oidc", h.cfg.Auth.OIDC.IssuerUrl, user.Sub, user.Email, &user.DisplayName)
	if errUpsert != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Database error inserting user"))
		return
	}

	sessionToken, errSession := auth.CreateSession(h.database, h.cfg, userId, nil)
	if errSession != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to create session"))
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/auth/complete#token=%s", url.QueryEscape(sessionToken)), http.StatusFound)
}

func (h *AuthHandler) startNextcloud(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Nextcloud.Enabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Nextcloud not configured"})
		return
	}

	stateBytes := make([]byte, 24)
	rand.Read(stateBytes)
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	expiresAt := time.Now().UnixNano()/int64(time.Millisecond) + 10*60*1000 // 10 mins
	_, err := h.database.Exec("INSERT INTO kv (k, v, expires_at) VALUES (?, '1', ?)", "ncoauth:"+state, expiresAt)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", h.cfg.Nextcloud.ClientId)
	params.Set("redirect_uri", h.cfg.Nextcloud.RedirectUri)
	params.Set("state", state)

	authURL := fmt.Sprintf("%s/index.php/apps/oauth2/authorize?%s", h.cfg.Nextcloud.Url, params.Encode())
	http.Redirect(w, r, authURL, http.StatusFound)
}

type nextcloudTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	UserID       string `json:"user_id"`
}

func (h *AuthHandler) callbackNextcloud(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Nextcloud.Enabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Nextcloud not configured"})
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Missing code or state"))
		return
	}

	var unused string
	now := time.Now().UnixNano() / int64(time.Millisecond)
	err := h.database.QueryRow("SELECT k FROM kv WHERE k = ? AND (expires_at IS NULL OR expires_at > ?)", "ncoauth:"+state, now).Scan(&unused)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid or expired state"))
		return
	}

	h.database.Exec("DELETE FROM kv WHERE k = ?", "ncoauth:"+state)

	// Exchange code for tokens
	body := url.Values{}
	body.Set("grant_type", "authorization_code")
	body.Set("code", code)
	body.Set("redirect_uri", h.cfg.Nextcloud.RedirectUri)
	body.Set("client_id", h.cfg.Nextcloud.ClientId)
	body.Set("client_secret", h.cfg.Nextcloud.ClientSecret)

	tokenURL := fmt.Sprintf("%s/index.php/apps/oauth2/api/v1/token", h.cfg.Nextcloud.Url)
	resp, errPost := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(body.Encode()))
	if errPost != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("Failed to reach Nextcloud"))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("Nextcloud token exchange failed"))
		return
	}

	var tokens nextcloudTokenResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&tokens); errDecode != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to parse Nextcloud token response"))
		return
	}

	displayName := tokens.UserID
	var email *string

	// Query Nextcloud profile info
	profileURL := fmt.Sprintf("%s/ocs/v2.php/cloud/user?format=json", h.cfg.Nextcloud.Url)
	reqUI, errReq := http.NewRequest("GET", profileURL, nil)
	if errReq == nil {
		reqUI.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		reqUI.Header.Set("OCS-APIRequest", "true")
		respUI, errUI := http.DefaultClient.Do(reqUI)
		if errUI == nil {
			defer respUI.Body.Close()
			var profileData map[string]interface{}
			if errDecodeUI := json.NewDecoder(respUI.Body).Decode(&profileData); errDecodeUI == nil {
				if ocs, ok := profileData["ocs"].(map[string]interface{}); ok {
					if data, ok := ocs["data"].(map[string]interface{}); ok {
						if dName, ok := data["displayname"].(string); ok && dName != "" {
							displayName = dName
						}
						if em, ok := data["email"].(string); ok && em != "" {
							email = &em
						}
					}
				}
			}
		}
	}

	var userId string
	errQuery := h.database.QueryRow("SELECT id FROM users WHERE nc_url = ? AND nc_user_id = ?", h.cfg.Nextcloud.Url, tokens.UserID).Scan(&userId)
	if errQuery == nil {
		// Existing user
		_, _ = h.database.Exec("UPDATE users SET display_name = ?, email = COALESCE(?, email) WHERE id = ?", displayName, email, userId)
	} else if errQuery == sql.ErrNoRows {
		// Create new user
		rawBytes := make([]byte, 12)
		rand.Read(rawBytes)
		userId = base64.RawURLEncoding.EncodeToString(rawBytes)
		
		_, errInsert := h.database.Exec(`
			INSERT INTO users (id, email, display_name, nc_user_id, nc_url, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, userId, email, displayName, tokens.UserID, h.cfg.Nextcloud.Url, now)
		if errInsert != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Database error inserting Nextcloud user"))
			return
		}
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(errQuery.Error()))
		return
	}

	sessionToken, errSession := auth.CreateSession(h.database, h.cfg, userId, &auth.NextcloudOAuth{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresInSec: tokens.ExpiresIn,
	})
	if errSession != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to create session"))
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/auth/complete#token=%s", url.QueryEscape(sessionToken)), http.StatusFound)
}

func (h *AuthHandler) getMe(w http.ResponseWriter, r *http.Request) {
	userId := auth.GetUserID(r.Context())
	authVia := auth.GetAuthVia(r.Context())

	if userId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Not authenticated"})
		return
	}

	var user struct {
		ID               string  `json:"id"`
		Email            *string `json:"email"`
		DisplayName      string  `json:"display_name"`
		ExternalProvider *string `json:"external_provider"`
		ExternalIssuer   *string `json:"external_issuer"`
		NcUserID         *string `json:"nc_user_id"`
		NcURL            *string `json:"nc_url"`
	}

	err := h.database.QueryRow(`
		SELECT id, email, display_name, external_provider, external_issuer, nc_user_id, nc_url
		  FROM users WHERE id = ?
	`, userId).Scan(&user.ID, &user.Email, &user.DisplayName, &user.ExternalProvider, &user.ExternalIssuer, &user.NcUserID, &user.NcURL)

	if err != nil {
		// Fallback to minimal identity
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":       userId,
			"authVia":  authVia,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":                user.ID,
		"email":             user.Email,
		"display_name":      user.DisplayName,
		"external_provider": user.ExternalProvider,
		"external_issuer":   user.ExternalIssuer,
		"nc_user_id":        user.NcUserID,
		"nc_url":            user.NcURL,
		"authVia":           authVia,
	})
}

func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	sessionToken := auth.GetSessionToken(r.Context())
	if sessionToken != "" {
		_ = auth.RevokeSession(h.database, sessionToken)
	}

	// RP-initiated OIDC logout if configured
	if h.cfg.Auth.Mode == config.AuthModeOIDC && h.cfg.Auth.OIDC.PostLogoutRedirectUri != "" {
		ctx := r.Context()
		helper, err := auth.GetOIDCHelper(ctx, h.cfg)
		if err == nil {
			// Read the end_session_endpoint discovery parameters (go-oidc does not natively wrap endSessionUrl out of the box,
			// but we can query discovery document or default back to returning ok)
			var claims struct {
				EndSessionURL string `json:"end_session_endpoint"`
			}
			if errDiscovery := helper.Provider.Claims(&claims); errDiscovery == nil && claims.EndSessionURL != "" {
				logoutURL := fmt.Sprintf("%s?post_logout_redirect_uri=%s", claims.EndSessionURL, url.QueryEscape(h.cfg.Auth.OIDC.PostLogoutRedirectUri))
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "postLogoutUrl": logoutURL})
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
