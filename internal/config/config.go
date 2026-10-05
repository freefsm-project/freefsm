package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

var (
	Version      = "dev"
	Commit       = "none"
	ExactRelease = "false"
	BuildKind    = "development"
)

func init() {
	// Plain go build may have a revision even without our linker metadata.
	// go run may have none; development identity does not require one.
	if Commit == "none" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					Commit = setting.Value
				}
			}
		}
	}
}

type Config struct {
	DBHost     string
	DBPort     int
	DBName     string
	DBUser     string
	DBPassword string
	DBSSLMode  string

	Addr          string
	LogLevel      string
	LogFile       string
	SessionSecret string
	SetupToken    string
	PublicURL     string

	UploadDir     string
	StateDir      string
	MaxUploadSize int64
	TileURL       string
	GeocoderURL   string
}

func Load() (*Config, error) {
	godotenv.Load()
	uploadDefault := "/var/lib/freefsm/uploads"
	if runtime.GOOS == "freebsd" {
		uploadDefault = "/var/db/freefsm/uploads"
	}

	cfg := &Config{
		DBHost:        getEnv("FREEFSM_DB_HOST", "localhost"),
		DBPort:        getEnvInt("FREEFSM_DB_PORT", 5432),
		DBName:        getEnv("FREEFSM_DB_NAME", "freefsm"),
		DBUser:        getEnv("FREEFSM_DB_USER", "freefsm"),
		DBPassword:    getEnv("FREEFSM_DB_PASSWORD", ""),
		DBSSLMode:     getEnv("FREEFSM_DB_SSLMODE", "disable"),
		Addr:          getEnv("FREEFSM_ADDR", ":3000"),
		LogLevel:      getEnv("FREEFSM_LOG_LEVEL", "info"),
		LogFile:       getEnv("FREEFSM_LOG_FILE", ""),
		SessionSecret: getEnv("FREEFSM_SESSION_SECRET", ""),
		SetupToken:    getEnv("FREEFSM_SETUP_TOKEN", ""),
		PublicURL:     strings.TrimRight(getEnv("FREEFSM_PUBLIC_URL", ""), "/"),
		UploadDir:     getEnv("FREEFSM_UPLOAD_DIR", uploadDefault),
		MaxUploadSize: getEnvInt64("FREEFSM_MAX_UPLOAD_SIZE", 26214400),
		TileURL:       getEnv("FREEFSM_TILE_URL", "https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"),
		GeocoderURL:   strings.TrimRight(getEnv("FREEFSM_GEOCODER_URL", ""), "/"),
	}
	cfg.StateDir = getEnv("FREEFSM_STATE_DIR", filepath.Join(filepath.Dir(filepath.Clean(cfg.UploadDir)), "state"))

	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("FREEFSM_SESSION_SECRET is required")
	}
	if cfg.SetupToken == "" {
		return nil, fmt.Errorf("FREEFSM_SETUP_TOKEN is required")
	}
	if cfg.PublicURL != "" {
		parsed, err := url.Parse(cfg.PublicURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("FREEFSM_PUBLIC_URL must be a valid http(s) URL")
		}
	}

	return cfg, nil
}

// Declared releases require the exact-tag assertion and matching clean VCS
// metadata. Development builds need neither tags nor VCS metadata.
func BackupDisabledReason() string {
	if BuildKind == "development" {
		return ""
	}
	if BuildKind != "release" {
		return "Backup build kind is invalid. Rebuild with release or development metadata."
	}
	info, ok := debug.ReadBuildInfo()
	if ok && ExactRelease == "true" {
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if settings["vcs"] == "git" && settings["vcs.modified"] == "false" && settings["vcs.revision"] == Commit {
			return ""
		}
	}
	return "The declared release lacks matching clean tagged VCS metadata. Rebuild with correct build metadata."
}

func (c *Config) DSN() string {
	ssl := c.DBSSLMode
	if ssl == "" {
		ssl = "disable"
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.DBUser, c.DBPassword), Host: net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort)), Path: "/" + c.DBName}
	q := url.Values{"sslmode": {ssl}}
	if strings.HasPrefix(c.DBHost, "/") {
		u.Host = ""
		q.Set("host", c.DBHost)
		q.Set("port", strconv.Itoa(c.DBPort))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

func getEnvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return n
		}
	}
	return def
}
