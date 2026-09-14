package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration. Values come from two places,
// deliberately kept separate:
//
//   - Secrets and deployment-specific values (DB location, ports, OAuth
//     callback URLs) come from environment variables / .env - things that
//     differ per environment or must never be committed.
//   - Non-secret application settings (model choice, retry counts, safety
//     limits) come from config.json - safe to commit, easier to discover
//     and review than env vars scattered across a gitignored file, and
//     still just as easy to change.
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
	GroqAPIKey      string

	// FrontendURL is where the browser is sent after a successful OAuth callback.
	FrontendURL string

	// --- Everything below comes from config.json - see fileSettings ---

	// SummaryProvider selects which LLM generates commit summaries: "claude"
	// or "groq".
	SummaryProvider string
	ClaudeModel     string
	GroqModel       string

	// SummarizeWithDiff toggles whether the poller fetches each new commit's
	// diff for the summarizer to use. When false, only the commit message is
	// ever used (skips the extra Bitbucket API call per commit too). When
	// true, a diff over MaxDiffBytes or a failed fetch still falls back to
	// message-only automatically - this toggle controls whether that
	// attempt happens at all, not the fallback behavior itself.
	SummarizeWithDiff bool

	// PollInterval is how often the commit-polling worker checks each
	// subscribed repo for new commits.
	PollInterval time.Duration
	// SummaryInterval is how often the summary worker checks for pending
	// commits to summarize.
	SummaryInterval time.Duration

	// MaxCommitsPerRepoPerPoll bounds how many commits a single poll of one
	// repo will walk back through looking for the previous cursor, so a
	// repo with a huge burst of activity (or a rewritten history that never
	// reconnects with our cursor) can't make one tick run forever.
	MaxCommitsPerRepoPerPoll int
	// MaxCommitsPerPollTick bounds total commits collected across ALL repos
	// in one poll tick - repos beyond the budget are simply picked up on
	// the next tick, rather than one tick processing an unbounded amount of
	// work (e.g. after the backend was offline for a long time).
	MaxCommitsPerPollTick int
	// MaxDiffBytes caps how much diff text is ever fetched or used per
	// commit (see bitbucket.FetchDiff) - a diff past this size falls back
	// to message-only rather than bloating the summarizer prompt.
	MaxDiffBytes int
	// MaxSummaryAttempts bounds retries for a commit the summarizer keeps
	// failing on before it's marked "failed" for good. Only applies to
	// retryable errors - a permanent one (bad request, auth, billing) is
	// never retried regardless of this value. See internal/llmerr.
	MaxSummaryAttempts int
	// MaxSummariesPerTick bounds how many pending commits the summary
	// worker will call the LLM for in one tick, so a large backlog (e.g.
	// after being offline for a while) doesn't fire off an unbounded burst
	// of API calls in one go - the rest are picked up on the next tick.
	MaxSummariesPerTick int

	// FeedDefaultLimit / FeedMaxLimit bound the Commit Feed API's page
	// size (?limit=) - the default when unspecified, and the hard cap
	// regardless of what a client requests.
	FeedDefaultLimit int
	FeedMaxLimit     int

	// MaxReposPerUser caps how many repos a single user can subscribe to.
	// Exists mainly because MaxCommitsPerPollTick is a *global* budget
	// shared across every user's repos on one backend - without a per-user
	// cap, one user subscribing to many repos could starve everyone else's
	// repos of poll budget in the same tick.
	MaxReposPerUser int
}

// fileSettings is the shape of config.json. Every field has a hardcoded
// default (see defaultFileSettings) applied before the file is parsed, so a
// missing file, or a file missing some fields, still yields a fully valid
// config - only the fields actually present in the JSON override the
// defaults.
type fileSettings struct {
	SummaryProvider          string `json:"summaryProvider"`
	ClaudeModel              string `json:"claudeModel"`
	GroqModel                string `json:"groqModel"`
	SummarizeWithDiff        bool   `json:"summarizeWithDiff"`
	PollIntervalSeconds      int    `json:"pollIntervalSeconds"`
	SummaryIntervalSeconds   int    `json:"summaryIntervalSeconds"`
	MaxCommitsPerRepoPerPoll int    `json:"maxCommitsPerRepoPerPoll"`
	MaxCommitsPerPollTick    int    `json:"maxCommitsPerPollTick"`
	MaxDiffBytes             int    `json:"maxDiffBytes"`
	MaxSummaryAttempts       int    `json:"maxSummaryAttempts"`
	MaxSummariesPerTick      int    `json:"maxSummariesPerTick"`
	FeedDefaultLimit         int    `json:"feedDefaultLimit"`
	FeedMaxLimit             int    `json:"feedMaxLimit"`
	MaxReposPerUser          int    `json:"maxReposPerUser"`
}

func defaultFileSettings() fileSettings {
	return fileSettings{
		SummaryProvider:          "groq",
		ClaudeModel:              "claude-haiku-4-5",
		GroqModel:                "qwen/qwen3.8-27b",
		SummarizeWithDiff:        true,
		PollIntervalSeconds:      60,
		SummaryIntervalSeconds:   15,
		MaxCommitsPerRepoPerPoll: 200,
		MaxCommitsPerPollTick:    500,
		MaxDiffBytes:             20_000,
		MaxSummaryAttempts:       3,
		MaxSummariesPerTick:      20,
		FeedDefaultLimit:         30,
		FeedMaxLimit:             100,
		MaxReposPerUser:          10,
	}
}

// Load reads .env (secrets/deployment values) and config.json (non-secret
// application settings) if present, and returns the merged Config. Missing
// required values are left empty; callers should validate before using
// them. Neither file is required to exist - Load falls back to built-in
// defaults for anything missing.
//
// Both searches walk upward from the current working directory (stopping
// once they reach go.mod, the backend module root) rather than only
// checking cwd - this way `go run ./cmd/server` behaves the same whether
// invoked from backend/, backend/cmd/server/, or an IDE run configuration
// that defaults its working directory to the package folder.
func Load() Config {
	if path := findFileUpward(".env"); path != "" {
		if err := godotenv.Load(path); err != nil {
			log.Printf("found %s but failed to load it: %v", path, err)
		} else {
			log.Printf("loaded env from %s", path)
		}
	} else {
		log.Println("no .env file found in or above the working directory, relying on process environment")
	}

	fileCfg := loadFileSettings()

	return Config{
		Port:      getEnv("PORT", "8080"),
		MongoURI:  getEnv("MONGO_URI", "mongodb://localhost:27017/?replicaSet=rs0"),
		MongoDB:   getEnv("MONGO_DB", "commit_notification_app"),
		JWTSecret: os.Getenv("JWT_SECRET"),

		BitbucketClientID:     os.Getenv("BITBUCKET_CLIENT_ID"),
		BitbucketClientSecret: os.Getenv("BITBUCKET_CLIENT_SECRET"),
		BitbucketCallbackURL:  os.Getenv("BITBUCKET_CALLBACK_URL"),

		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		GroqAPIKey:      os.Getenv("GROQ_API_KEY"),

		FrontendURL: getEnv("FRONTEND_URL", "http://localhost:5173"),

		SummaryProvider:   fileCfg.SummaryProvider,
		ClaudeModel:       fileCfg.ClaudeModel,
		GroqModel:         fileCfg.GroqModel,
		SummarizeWithDiff: fileCfg.SummarizeWithDiff,

		PollInterval:    time.Duration(fileCfg.PollIntervalSeconds) * time.Second,
		SummaryInterval: time.Duration(fileCfg.SummaryIntervalSeconds) * time.Second,

		MaxCommitsPerRepoPerPoll: fileCfg.MaxCommitsPerRepoPerPoll,
		MaxCommitsPerPollTick:    fileCfg.MaxCommitsPerPollTick,
		MaxDiffBytes:             fileCfg.MaxDiffBytes,
		MaxSummaryAttempts:       fileCfg.MaxSummaryAttempts,
		MaxSummariesPerTick:      fileCfg.MaxSummariesPerTick,

		FeedDefaultLimit: fileCfg.FeedDefaultLimit,
		FeedMaxLimit:     fileCfg.FeedMaxLimit,

		MaxReposPerUser: fileCfg.MaxReposPerUser,
	}
}

// loadFileSettings reads config.json, applying each field on top of
// defaultFileSettings() so a missing file - or a file missing some fields -
// still yields a fully valid result.
func loadFileSettings() fileSettings {
	settings := defaultFileSettings()

	path := findFileUpward("config.json")
	if path == "" {
		log.Println("no config.json found in or above the working directory, using built-in defaults")
		return settings
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("found %s but failed to read it: %v - using built-in defaults", path, err)
		return defaultFileSettings()
	}

	if err := json.Unmarshal(data, &settings); err != nil {
		log.Printf("found %s but failed to parse it: %v - using built-in defaults", path, err)
		return defaultFileSettings()
	}

	log.Printf("loaded config from %s", path)
	return settings
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// findFileUpward walks upward from the working directory looking for a
// file named filename, stopping (without finding one) once it passes
// go.mod - so it never searches outside the backend module.
func findFileUpward(filename string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		candidate := filepath.Join(dir, filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return "" // reached the module root with no match - stop here
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached filesystem root
		}
		dir = parent
	}
}
