package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	Auth    AuthConfig    `mapstructure:"auth"`
	LDAP    LDAPConfig    `mapstructure:"ldap"`
	Sync    SyncConfig    `mapstructure:"sync"`
}

type ServerConfig struct {
	ListenAddr  string `mapstructure:"listen_addr"`
	GrafanaAddr string `mapstructure:"grafana_addr"`
}

type AuthConfig struct {
	JwtSecret          string        `mapstructure:"jwt_secret"`
	JwtExpiry          string        `mapstructure:"jwt_expiry"`
	JwtExpiryDuration  time.Duration `mapstructure:"-"`
	CookieName         string        `mapstructure:"cookie_name"`
	CookieSecure       bool          `mapstructure:"cookie_secure"`
	JwtPrivateKey      string        `mapstructure:"jwt_private_key"`
	LocalAdminUsername string        `mapstructure:"local_admin_username"`
	LocalAdminPassword string        `mapstructure:"local_admin_password"`
	GrafanaAPIToken    string        `mapstructure:"grafana_api_token"`
}

type LDAPConfig struct {
	Host            string `mapstructure:"host"`
	Port            int    `mapstructure:"port"`
	BindDN          string `mapstructure:"bind_dn"`
	BindPassword    string `mapstructure:"bind_password"`
	BaseDN          string `mapstructure:"base_dn"`
	UserFilter      string `mapstructure:"user_filter"`
	UserAttr        string `mapstructure:"user_attr"`
	EmailAttr       string `mapstructure:"email_attr"`
	GroupBaseDN     string `mapstructure:"group_base_dn"`
	GroupFilter     string `mapstructure:"group_filter"`
	GroupMemberAttr string `mapstructure:"group_member_attr"`
	GroupNameAttr   string `mapstructure:"group_attr"`
}

func (l *LDAPConfig) URL() string {
	return fmt.Sprintf("ldap://%s:%d", l.Host, l.Port)
}

type GroupMapping struct {
	ADGroup     string `mapstructure:"ad_group"`
	GrafanaTeam string `mapstructure:"grafana_team"`
}

type SyncConfig struct {
	Enabled             bool               `mapstructure:"enabled"`
	IntervalMinutes     int                `mapstructure:"interval_minutes"`
	StartupDelaySeconds int                `mapstructure:"startup_delay_seconds"`
	GroupsMapping       []GroupMapping     `mapstructure:"groups_mapping"`
	FolderPermissions   []FolderPermission `mapstructure:"folder_permissions"`
	Interval            time.Duration      `mapstructure:"-"`
}

type FolderPermission struct {
	Folder     string `mapstructure:"folder"`
	Team       string `mapstructure:"team"`
	Permission string `mapstructure:"permission"`
}

func LoadConfig() (*Config, error) {
	v := viper.New()

	// Config file resolution: config.yaml in current dir, ./config, or /etc/savras
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	v.AddConfigPath("/etc/savras")

	// Environment variables: SAVRAS_ prefix with dot -> underscore mapping
	v.SetEnvPrefix("SAVRAS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Defaults
	v.SetDefault("server.listen_addr", ":4181")
	v.SetDefault("server.grafana_addr", "http://localhost:3000")
	v.SetDefault("auth.cookie_name", "savras_session")
	v.SetDefault("auth.cookie_secure", false)
	v.SetDefault("sync.enabled", false)
	v.SetDefault("sync.interval_minutes", 15)
	v.SetDefault("sync.startup_delay_seconds", 10)
	v.SetDefault("ldap.host", "localhost")
	v.SetDefault("ldap.port", 389)

	var cfg Config
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Override sensitive fields from environment variables (Kubernetes Secrets support).
	// These take precedence over values in config.yaml.
	if v := os.Getenv("SAVRAS_LDAP_BIND_PASSWORD"); v != "" {
		cfg.LDAP.BindPassword = v
	}
	if v := os.Getenv("SAVRAS_AUTH_JWT_SECRET"); v != "" {
		cfg.Auth.JwtSecret = v
	}
	if v := os.Getenv("SAVRAS_AUTH_JWT_PRIVATE_KEY"); v != "" {
		cfg.Auth.JwtPrivateKey = v
	}
	if v := os.Getenv("SAVRAS_AUTH_LOCAL_ADMIN_PASSWORD"); v != "" {
		cfg.Auth.LocalAdminPassword = v
	}
	if v := os.Getenv("SAVRAS_GRAFANA_API_TOKEN"); v != "" {
		cfg.Auth.GrafanaAPIToken = v
	}

	// Post-process durations
	if cfg.Auth.JwtExpiry != "" {
		if d, err := time.ParseDuration(cfg.Auth.JwtExpiry); err == nil {
			cfg.Auth.JwtExpiryDuration = d
		} else {
			// fallback default
			cfg.Auth.JwtExpiryDuration = 24 * time.Hour
		}
	} else {
		cfg.Auth.JwtExpiryDuration = 24 * time.Hour
	}

	if cfg.Sync.IntervalMinutes > 0 {
		cfg.Sync.Interval = time.Duration(cfg.Sync.IntervalMinutes) * time.Minute
	} else {
		cfg.Sync.Interval = 15 * time.Minute
	}

	return &cfg, nil
}
