package auth

import (
	"context"
	"errors"
	"strings"
	"sync"

	"chronicle-server/pkg/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCHelper struct {
	Provider     *oidc.Provider
	Oauth2Config *oauth2.Config
	Verifier     *oidc.IDTokenVerifier
}

var oidcHelper *OIDCHelper
var oidcError error
var oidcOnce sync.Once

func GetOIDCHelper(ctx context.Context, cfg *config.Config) (*OIDCHelper, error) {
	oidcOnce.Do(func() {
		provider, err := oidc.NewProvider(ctx, cfg.Auth.OIDC.IssuerUrl)
		if err != nil {
			oidcError = err
			return
		}

		scopes := strings.Split(cfg.Auth.OIDC.Scopes, " ")
		oauth2Config := &oauth2.Config{
			ClientID:     cfg.Auth.OIDC.ClientId,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectUri,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		}

		verifier := provider.Verifier(&oidc.Config{ClientID: cfg.Auth.OIDC.ClientId})

		oidcHelper = &OIDCHelper{
			Provider:     provider,
			Oauth2Config: oauth2Config,
			Verifier:     verifier,
		}
	})

	if oidcError != nil {
		err := oidcError
		// Reset oidcOnce so we can retry discovery on next request
		oidcOnce = sync.Once{}
		oidcError = nil
		return nil, err
	}

	if oidcHelper == nil {
		return nil, errors.New("OIDC helper not initialized")
	}

	return oidcHelper, nil
}

type OidcUser struct {
	Sub         string
	Email       *string
	DisplayName string
}

func ExtractUser(ctx context.Context, helper *OIDCHelper, oauthToken *oauth2.Token) (*OidcUser, error) {
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("no id_token field in oauth2 token")
	}

	idToken, err := helper.Verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, err
	}

	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, err
	}

	// Try querying the UserInfo endpoint if access token is present
	userInfoClaims := make(map[string]interface{})
	userInfo, errUI := helper.Provider.UserInfo(ctx, oauth2.StaticTokenSource(oauthToken))
	if errUI == nil {
		_ = userInfo.Claims(&userInfoClaims)
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, errors.New("missing sub claim in ID token")
	}

	// Merge claims (UserInfo wins over ID token)
	merged := make(map[string]interface{})
	for k, v := range claims {
		merged[k] = v
	}
	for k, v := range userInfoClaims {
		merged[k] = v
	}

	var email *string
	if em, ok := merged["email"].(string); ok && em != "" {
		email = &em
	}

	displayName := sub
	if name, ok := merged["name"].(string); ok && name != "" {
		displayName = name
	} else if prefName, ok := merged["preferred_username"].(string); ok && prefName != "" {
		displayName = prefName
	} else if given, ok := merged["given_name"].(string); ok && given != "" {
		displayName = given
	}

	return &OidcUser{
		Sub:         sub,
		Email:       email,
		DisplayName: displayName,
	}, nil
}
