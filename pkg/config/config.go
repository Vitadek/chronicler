package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type AuthMode string

const (
	AuthModeNone    AuthMode = "none"
	AuthModeToken   AuthMode = "token"
	AuthModeForward AuthMode = "forward"
	AuthModeOIDC    AuthMode = "oidc"
)

type StorageReplica string

const (
	StorageReplicaNone      StorageReplica = "none"
	StorageReplicaNextcloud StorageReplica = "nextcloud"
	StorageReplicaS3        StorageReplica = "s3"
)

type Config struct {
	Port         int
	Host         string
	DataDir      string
	IsProd       bool
	LocalAdmin   bool
	SessionTtlMs int64
	Auth         AuthConfig
	Nextcloud    NextcloudConfig
	Storage      StorageConfig
	S3           S3Config
}

type AuthConfig struct {
	Mode                AuthMode
	AllowInsecureNoAuth bool
	Token               string
	Forward             ForwardAuthConfig
	OIDC                OIDCConfig
}

type ForwardAuthConfig struct {
	HeaderUser         string
	HeaderEmail        string
	HeaderName         string
	HeaderGroups       string
	TrustedProxies     string
	SharedSecretHeader string
	SharedSecret       string
	AdminGroup         string
}

type OIDCConfig struct {
	IssuerUrl               string
	ClientId                string
	ClientSecret            string
	RedirectUri             string
	Scopes                  string
	PostLogoutRedirectUri   string
	TokenEndpointAuthMethod string
}

type NextcloudConfig struct {
	Enabled           bool
	Url               string
	AllowInsecureHttp bool
	ClientId          string
	ClientSecret      string
	RedirectUri       string
	User              string
	Pass              string
	StorageDir        string
}

type StorageConfig struct {
	Replica         StorageReplica
	RetryIntervalMs int
	MaxAttempts     int
}

type S3Config struct {
	Bucket               string
	Region               string
	Endpoint             string
	Prefix               string
	ForcePathStyle       bool
	AllowInsecureHttp    bool
	ServerSideEncryption string
	KmsKeyId             string
}

func envString(name string, fallback string) string {
	val := os.Getenv(name)
	if val == "" {
		return fallback
	}
	return val
}

func envBoolean(name string, fallback bool) bool {
	val := os.Getenv(name)
	if val == "" {
		return fallback
	}
	return strings.ToLower(val) == "true"
}

func envInt(name string, fallback int) int {
	val := os.Getenv(name)
	if val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return parsed
}

func envPositiveInt(name string, fallback int) int {
	val := envInt(name, fallback)
	if val <= 0 {
		return fallback
	}
	return val
}

func LoadConfig() (*Config, error) {
	// Defaults port to 3000, host to 0.0.0.0, data directory to ./data
	port := envInt("PORT", 3000)
	host := envString("HOST", "0.0.0.0")
	cwd, _ := os.Getwd()
	dataDir := envString("DATA_DIR", filepath.Join(cwd, "data"))
	isProd := os.Getenv("NODE_ENV") == "production"

	rawMode := strings.ToLower(envString("AUTH_MODE", "none"))
	var authMode AuthMode
	switch rawMode {
	case "token":
		authMode = AuthModeToken
	case "forward":
		authMode = AuthModeForward
	case "oidc":
		authMode = AuthModeOIDC
	default:
		authMode = AuthModeNone
	}

	legacyStorageProvider := strings.TrimSpace(strings.ToLower(envString("STORAGE_PROVIDER", "")))
	configuredStorageReplica := strings.TrimSpace(strings.ToLower(envString("STORAGE_REPLICA", "")))

	var rawStorageReplica StorageReplica
	if configuredStorageReplica != "" {
		rawStorageReplica = StorageReplica(configuredStorageReplica)
	} else {
		if legacyStorageProvider == "" || legacyStorageProvider == "sqlite" {
			rawStorageReplica = StorageReplicaNone
		} else if legacyStorageProvider == "hybrid" {
			rawStorageReplica = StorageReplicaNextcloud
		} else {
			rawStorageReplica = StorageReplica(legacyStorageProvider)
		}
	}

	cfg := &Config{
		Port:         port,
		Host:         host,
		DataDir:      dataDir,
		IsProd:       isProd,
		LocalAdmin:   envBoolean("LOCAL_ADMIN", false),
		SessionTtlMs: 1000 * 60 * 60 * 24 * 30, // 30 days
		Auth: AuthConfig{
			Mode:                authMode,
			AllowInsecureNoAuth: envBoolean("ALLOW_INSECURE_NO_AUTH", false),
			Token:               envString("AUTH_TOKEN", ""),
			Forward: ForwardAuthConfig{
				HeaderUser:         envString("AUTH_FORWARD_HEADER_USER", "Remote-User"),
				HeaderEmail:        envString("AUTH_FORWARD_HEADER_EMAIL", "Remote-Email"),
				HeaderName:         envString("AUTH_FORWARD_HEADER_NAME", "Remote-Name"),
				HeaderGroups:       envString("AUTH_FORWARD_HEADER_GROUPS", "Remote-Groups"),
				TrustedProxies:     envString("AUTH_FORWARD_TRUSTED_PROXIES", "loopback,linklocal,uniquelocal"),
				SharedSecretHeader: envString("AUTH_FORWARD_SECRET_HEADER", ""),
				SharedSecret:       envString("AUTH_FORWARD_SECRET", ""),
				AdminGroup:         envString("AUTH_FORWARD_ADMIN_GROUP", ""),
			},
			OIDC: OIDCConfig{
				IssuerUrl:               envString("AUTH_OIDC_ISSUER_URL", ""),
				ClientId:                envString("AUTH_OIDC_CLIENT_ID", ""),
				ClientSecret:            envString("AUTH_OIDC_CLIENT_SECRET", ""),
				RedirectUri:             envString("AUTH_OIDC_REDIRECT_URI", ""),
				Scopes:                  envString("AUTH_OIDC_SCOPES", "openid profile email"),
				PostLogoutRedirectUri:   envString("AUTH_OIDC_POST_LOGOUT_REDIRECT_URI", ""),
				TokenEndpointAuthMethod: envString("AUTH_OIDC_TOKEN_AUTH_METHOD", "auto"),
			},
		},
		Nextcloud: NextcloudConfig{
			Enabled:           os.Getenv("NEXTCLOUD_URL") != "",
			Url:               strings.TrimSuffix(envString("NEXTCLOUD_URL", ""), "/"),
			AllowInsecureHttp: envBoolean("NEXTCLOUD_ALLOW_INSECURE_HTTP", false),
			ClientId:          envString("NEXTCLOUD_CLIENT_ID", ""),
			ClientSecret:      envString("NEXTCLOUD_CLIENT_SECRET", ""),
			RedirectUri:       envString("NEXTCLOUD_REDIRECT_URI", ""),
			User:              envString("NC_USER", ""),
			Pass:              envString("NC_PASS", ""),
			StorageDir:        envString("NC_DIR", "Chronicle_Storage"),
		},
		Storage: StorageConfig{
			Replica:         rawStorageReplica,
			RetryIntervalMs: envPositiveInt("STORAGE_RETRY_INTERVAL_MS", 30000),
			MaxAttempts:     envPositiveInt("STORAGE_MAX_ATTEMPTS", 10),
		},
		S3: S3Config{
			Bucket:               envString("S3_BUCKET", ""),
			Region:               envString("S3_REGION", "us-east-1"),
			Endpoint:             strings.TrimSuffix(envString("S3_ENDPOINT", ""), "/"),
			Prefix:               strings.Trim(envString("S3_PREFIX", "chronicle"), "/"),
			ForcePathStyle:       envBoolean("S3_FORCE_PATH_STYLE", false),
			AllowInsecureHttp:    envBoolean("S3_ALLOW_INSECURE_HTTP", false),
			ServerSideEncryption: envString("S3_SERVER_SIDE_ENCRYPTION", ""),
			KmsKeyId:             envString("S3_KMS_KEY_ID", ""),
		},
	}

	// Validate configuration at boot
	if err := cfg.Validate(true); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate(listening bool) error {
	a := c.Auth
	rawMode := strings.ToLower(os.Getenv("AUTH_MODE"))
	if rawMode == "" {
		rawMode = "none"
	}
	if rawMode != "none" && rawMode != "token" && rawMode != "forward" && rawMode != "oidc" {
		return fmt.Errorf("Invalid AUTH_MODE=\"%s\". Expected none, token, forward, or oidc.", rawMode)
	}

	loopbackHosts := map[string]bool{"127.0.0.1": true, "::1": true, "[::1]": true, "localhost": true}
	if c.IsProd && listening && a.Mode == AuthModeNone && !loopbackHosts[strings.ToLower(c.Host)] && !a.AllowInsecureNoAuth {
		return errors.New("Production AUTH_MODE=none may only bind to loopback. For an explicitly trusted network, set ALLOW_INSECURE_NO_AUTH=true.")
	}

	if a.Mode == AuthModeToken && a.Token == "" {
		return errors.New("AUTH_MODE=token requires AUTH_TOKEN to be set.")
	}

	if a.Mode == AuthModeOIDC {
		var missing []string
		if a.OIDC.IssuerUrl == "" {
			missing = append(missing, "AUTH_OIDC_ISSUER_URL")
		}
		if a.OIDC.ClientId == "" {
			missing = append(missing, "AUTH_OIDC_CLIENT_ID")
		}
		if a.OIDC.RedirectUri == "" {
			missing = append(missing, "AUTH_OIDC_REDIRECT_URI")
		}
		if len(missing) > 0 {
			return fmt.Errorf("AUTH_MODE=oidc requires: %s", strings.Join(missing, ", "))
		}
	}

	if a.Mode == AuthModeForward {
		if a.Forward.TrustedProxies == "" {
			return errors.New("AUTH_MODE=forward requires AUTH_FORWARD_TRUSTED_PROXIES (or leave unset for the safe default).")
		}
	}

	if c.Storage.Replica != StorageReplicaNone && c.Storage.Replica != StorageReplicaNextcloud && c.Storage.Replica != StorageReplicaS3 {
		return fmt.Errorf("Invalid STORAGE_REPLICA=\"%s\". Expected none, nextcloud, or s3.", c.Storage.Replica)
	}

	if envBoolean("NEXTCLOUD_MIRROR", false) || os.Getenv("NEXTCLOUD_MIRROR_ROOT") != "" {
		return errors.New("NEXTCLOUD_MIRROR and NEXTCLOUD_MIRROR_ROOT are retired. Remove both legacy settings and use STORAGE_REPLICA=nextcloud.")
	}

	if c.Nextcloud.Url != "" {
		u, err := url.Parse(c.Nextcloud.Url)
		if err != nil {
			return errors.New("NEXTCLOUD_URL must be a valid absolute URL.")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("NEXTCLOUD_URL must use http or https.")
		}
		if u.Scheme != "https" && !c.Nextcloud.AllowInsecureHttp {
			return errors.New("NEXTCLOUD_URL must use HTTPS. For a trusted LAN only, set NEXTCLOUD_ALLOW_INSECURE_HTTP=true.")
		}
	}

	if c.Storage.Replica == StorageReplicaNextcloud {
		if c.Nextcloud.Url == "" || c.Nextcloud.User == "" || c.Nextcloud.Pass == "" {
			return errors.New("STORAGE_REPLICA=nextcloud requires NEXTCLOUD_URL, NC_USER, and NC_PASS (App Password).")
		}
	}

	if c.Storage.Replica == StorageReplicaS3 {
		if c.S3.Bucket == "" {
			return errors.New("STORAGE_REPLICA=s3 requires S3_BUCKET.")
		}
		if c.S3.Endpoint != "" {
			u, err := url.Parse(c.S3.Endpoint)
			if err != nil {
				return errors.New("S3_ENDPOINT must be a valid absolute URL.")
			}
			if u.Scheme != "https" && !c.S3.AllowInsecureHttp {
				return errors.New("S3_ENDPOINT must use HTTPS. For a trusted LAN only, set S3_ALLOW_INSECURE_HTTP=true.")
			}
			if u.Scheme != "https" && u.Scheme != "http" {
				return errors.New("S3_ENDPOINT must use http or https.")
			}
		}
		if c.S3.ServerSideEncryption != "" && c.S3.ServerSideEncryption != "AES256" && c.S3.ServerSideEncryption != "aws:kms" {
			return errors.New("S3_SERVER_SIDE_ENCRYPTION must be empty, AES256, or aws:kms.")
		}
		if c.S3.ServerSideEncryption == "aws:kms" && c.S3.KmsKeyId == "" {
			return errors.New("S3_SERVER_SIDE_ENCRYPTION=aws:kms requires S3_KMS_KEY_ID.")
		}
	}

	return nil
}
