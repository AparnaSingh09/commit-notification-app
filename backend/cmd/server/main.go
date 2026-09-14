package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"commit-notification-app/backend/internal/auth"
	"commit-notification-app/backend/internal/claude"
	"commit-notification-app/backend/internal/commits"
	"commit-notification-app/backend/internal/config"
	"commit-notification-app/backend/internal/db"
	"commit-notification-app/backend/internal/groq"
	"commit-notification-app/backend/internal/repos"
)

func main() {
	cfg := config.Load()

	// Canceled on Ctrl+C / SIGTERM - lets the polling worker stop cleanly
	// instead of being killed mid-tick.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}

	if err := db.EnsureIndexes(ctx, database); err != nil {
		log.Fatalf("mongo ensure indexes: %v", err)
	}

	// Ephemeral, regenerated every startup - see NewEphemeralSessionSecret's
	// doc comment. This also means restarting the backend logs everyone out,
	// on top of the explicit logout endpoint below.
	sessionSecret, err := auth.NewEphemeralSessionSecret()
	if err != nil {
		log.Fatalf("generating session secret: %v", err)
	}

	oauthCfg := auth.NewOAuthConfig(cfg)
	authHandlers := auth.NewHandlers(cfg, oauthCfg, database, sessionSecret)
	repoHandlers := repos.NewHandlers(cfg, oauthCfg, database)

	commitHandlers := commits.NewHandlers(database, cfg.FeedDefaultLimit, cfg.FeedMaxLimit)

	poller := commits.NewPoller(cfg, oauthCfg, database, cfg.PollInterval)
	go poller.Run(ctx)
	log.Printf("commit polling worker started, interval=%s", cfg.PollInterval)

	startSummaryWorker(ctx, cfg, database)

	router := gin.Default()

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{cfg.FrontendURL},
		AllowMethods:     []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Content-Type"},
		AllowCredentials: true, // required so the browser sends/receives the session cookie
	}))

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	router.GET("/auth/login", authHandlers.Login)
	router.GET("/auth/callback", authHandlers.Callback)
	router.POST("/auth/logout", authHandlers.Logout)

	api := router.Group("/api")
	api.Use(auth.Middleware(sessionSecret))
	api.GET("/me", authHandlers.Me)
	api.POST("/repos", repoHandlers.AddRepo)
	api.GET("/repos", repoHandlers.ListRepos)
	api.DELETE("/repos/:id", repoHandlers.RemoveRepo)
	api.GET("/commits", commitHandlers.ListFeed)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: router}
	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
}

// startSummaryWorker picks the LLM provider named by SUMMARY_PROVIDER
// ("claude" or "groq") and starts the background worker that fills in
// commit summaries. If the chosen provider's API key isn't set, it logs a
// warning and skips starting the worker entirely rather than crashing -
// commits just stay "pending" until it's configured.
func startSummaryWorker(ctx context.Context, cfg config.Config, database *mongo.Database) {
	provider := strings.ToLower(strings.TrimSpace(cfg.SummaryProvider))

	var summarizer claude.Summarizer
	var label string

	switch provider {
	case "groq":
		if cfg.GroqAPIKey == "" {
			log.Println("SUMMARY_PROVIDER=groq but GROQ_API_KEY not set - skipping summary worker; commits will stay \"pending\" until it's configured")
			return
		}
		summarizer = groq.New(cfg.GroqAPIKey, cfg.GroqModel)
		label = "groq model=" + cfg.GroqModel

	case "claude", "":
		if cfg.AnthropicAPIKey == "" {
			log.Println("SUMMARY_PROVIDER=claude but ANTHROPIC_API_KEY not set - skipping summary worker; commits will stay \"pending\" until it's configured")
			return
		}
		summarizer = claude.New(cfg.AnthropicAPIKey, cfg.ClaudeModel)
		label = "claude model=" + cfg.ClaudeModel

	default:
		log.Printf("SUMMARY_PROVIDER=%q not recognized (expected \"claude\" or \"groq\") - skipping summary worker", cfg.SummaryProvider)
		return
	}

	summaryWorker := claude.NewWorker(summarizer, database, cfg.SummaryInterval, cfg.MaxSummaryAttempts, cfg.MaxSummariesPerTick)
	go summaryWorker.Run(ctx)
	log.Printf("summary worker started, provider=%s interval=%s", label, cfg.SummaryInterval)
}
