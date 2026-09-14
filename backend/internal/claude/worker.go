package claude

import (
	"context"
	"errors"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	appdb "commit-notification-app/backend/internal/db"
	"commit-notification-app/backend/internal/llmerr"
)

// Summarizer is anything that can turn a commit message (and, when
// available, its diff) into a one-sentence summary - implemented by
// *claude.Client and *groq.Client (interchangeably; see
// cmd/server/main.go's SUMMARY_PROVIDER switch). This interface lives in
// package claude simply because the worker was built here first - despite
// the package name, Worker itself has no Claude-specific logic.
//
// diff may be empty (fetch failed, or exceeded the size cap - see
// bitbucket.FetchDiff) - implementations must produce a sensible
// message-only summary in that case, not error out. A non-2xx provider
// response should be returned as *llmerr.APIError so permanent failures
// (bad request, auth, billing) don't waste retries - see summarizeOne.
type Summarizer interface {
	SummarizeCommit(ctx context.Context, commitMessage, diff string) (string, error)
}

// Worker periodically picks up commits with summaryStatus "pending" and
// fills in their AI summary, via whichever Summarizer it was given.
type Worker struct {
	client      Summarizer
	db          *mongo.Database
	interval    time.Duration
	maxAttempts int
	maxPerTick  int
}

func NewWorker(client Summarizer, db *mongo.Database, interval time.Duration, maxAttempts, maxPerTick int) *Worker {
	return &Worker{client: client, db: db, interval: interval, maxAttempts: maxAttempts, maxPerTick: maxPerTick}
}

// Run blocks, ticking every w.interval, until ctx is canceled.
func (w *Worker) Run(ctx context.Context) {
	w.tick(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("summary worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	// Capped so a large backlog (e.g. after being offline for a while)
	// summarizes gradually across ticks instead of firing an unbounded
	// burst of LLM calls in one go - the rest are picked up next tick.
	opts := options.Find().SetLimit(int64(w.maxPerTick))
	cursor, err := w.db.Collection("commits").Find(ctx, bson.M{"summaryStatus": appdb.SummaryStatusPending}, opts)
	if err != nil {
		log.Printf("summary worker: querying pending commits failed: %v", err)
		return
	}
	defer cursor.Close(ctx)

	n := 0
	for cursor.Next(ctx) {
		var c appdb.Commit
		if err := cursor.Decode(&c); err != nil {
			log.Printf("summary worker: decoding commit failed: %v", err)
			continue
		}
		w.summarizeOne(ctx, c)
		n++
	}
	if n == w.maxPerTick {
		log.Printf("summary worker: hit the %d-per-tick cap - more pending commits will be picked up next tick", w.maxPerTick)
	}
}

func (w *Worker) summarizeOne(ctx context.Context, c appdb.Commit) {
	summary, err := w.client.SummarizeCommit(ctx, c.Message, c.Diff)
	if err != nil {
		// A permanent error (bad request, auth, billing, a bad model ID -
		// 4xx other than 429) will fail identically no matter how many
		// times we retry, so don't waste attempts on it - fail fast.
		var apiErr *llmerr.APIError
		permanent := errors.As(err, &apiErr) && !apiErr.Retryable()

		attempts := c.Attempts + 1
		update := bson.M{"attempts": attempts}
		if permanent {
			update["summaryStatus"] = appdb.SummaryStatusFailed
			log.Printf("summary worker: permanent error for commit %s, not retrying: %v", c.CommitHash, err)
		} else if attempts >= w.maxAttempts {
			update["summaryStatus"] = appdb.SummaryStatusFailed
			log.Printf("summary worker: giving up on commit %s after %d attempts: %v", c.CommitHash, attempts, err)
		} else {
			log.Printf("summary worker: attempt %d/%d failed for commit %s: %v (will retry next tick)",
				attempts, w.maxAttempts, c.CommitHash, err)
		}
		if _, uerr := w.db.Collection("commits").UpdateOne(ctx, bson.M{"_id": c.ID}, bson.M{"$set": update}); uerr != nil {
			log.Printf("summary worker: recording failed attempt for %s failed: %v", c.CommitHash, uerr)
		}
		return
	}

	_, err = w.db.Collection("commits").UpdateOne(ctx,
		bson.M{"_id": c.ID},
		bson.M{"$set": bson.M{"aiSummary": summary, "summaryStatus": appdb.SummaryStatusDone}},
	)
	if err != nil {
		log.Printf("summary worker: saving summary for %s failed: %v", c.CommitHash, err)
	}
}
