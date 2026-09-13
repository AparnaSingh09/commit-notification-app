package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	Port     string
	MongoURI string
	MongoDB  string
	// JWTSecret only derives the key that encrypts stored Bitbucket tokens
	// at rest (see internal/crypto) - it does NOT sign session cookies.
	// Session JWTs are signed with a separate, ephemeral, in-memory secret
	// (see auth.NewEphemeralSessionSecret) generated fresh on every
	// backend startup, so this value must stay stable across restarts or
	// previously encrypted tokens become undecryptable.
	JWTSecret string

	BitbucketClientID     string
	BitbucketClientSecret string
	BitbucketCallbackURL  string

	AnthropicAPIKey string
	ClaudeModel     string

	// FrontendURL is where the browser is sent after a successful OAuth callback.
	FrontendURL string

	// PollInterval is how often the commit-polling worker checks each
	// subscribed repo for new commits.
	PollInterval time.Duration

	// SummaryInterval is how often the Claude summary worker checks for
	// pending commits to summarize.
	SummaryInterval time.Duration
}

// Load reads a .env file if present (dev convenience) and returns the parsed Config.
// Missing required values are left empty; callers should validate before using them.
//
// It searches upward from the current working directory for a .env file
// (stopping once it reaches go.mod, the backend module root) rather than
// only checking cwd - this way `go run ./cmd/server` behaves the same
// whether invoked from backend/, backend/cmd/server/, or an IDE run
// configuration that defaults its working directory to the package folder.
func Load() Config {
	if path := findEnvFile(); path != "" {
		if err := godotenv.Load(path); err != nil {
			log.Printf("found %s but failed to load it: %v", path, err)
		} else {
			log.Printf("loaded env from %s", path)
		}
	} else {
		log.Println("no .env file found in or above the working directory, relying on process environment")
	}

	return Config{
		Port:      getEnv("PORT", "8080"),
		MongoURI:  getEnv("MONGO_URI", "mongodb://localhost:27017/?replicaSet=rs0"),
		MongoDB:   getEnv("MONGO_DB", "commit_notification_app"),
		JWTSecret: os.Getenv("JWT_SECRET"),

		BitbucketClientID:     os.Getenv("BITBUCKET_CLIENT_ID"),
		BitbucketClientSecret: os.Getenv("BITBUCKET_CLIENT_SECRET"),
		BitbucketCallbackURL:  os.Getenv("BITBUCKET_CALLBACK_URL"),

		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		ClaudeModel:     getEnv("CLAUDE_MODEL", "claude-haiku-4-5"),

		FrontendURL: getEnv("FRONTEND_URL", "http://localhost:5173"),

		PollInterval:    getEnvDuration("POLL_INTERVAL_SECONDS", 60*time.Second),
		SummaryInterval: getEnvDuration("SUMMARY_INTERVAL_SECONDS", 15*time.Second),
	}
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	secs, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("invalid %s=%q, using default of %s", key, v, fallback)
		return fallback
	}
	return time.Duration(secs) * time.Second
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// findEnvFile walks upward from the working directory looking for a .env
// file, stopping (without finding one) once it passes go.mod - so it never
// searches outside the backend module.
func findEnvFile() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		candidate := filepath.Join(dir, ".env")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return "" // reached the module root with no .env - stop here
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached filesystem root
		}
		dir = parent
	}
}
