package claude

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	appdb "commit-notification-app/backend/internal/db"
)

// maxAttempts bounds retries for a commit Claude keeps failing on (rate
// limit, transient network error, etc.) - after this many failures it's
// marked "failed" for good instead of being retried forever.
const maxAttempts = 3

// Worker periodically picks up commits with summaryStatus "pending" and
// fills in their AI summary.
type Worker struct {
	client   *Client
	db       *mongo.Database
	interval time.Duration
}

func NewWorker(client *Client, db *mongo.Database, interval time.Duration) *Worker {
	return &Worker{client: client, db: db, interval: interval}
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
	cursor, err := w.db.Collection("commits").Find(ctx, bson.M{"summaryStatus": appdb.SummaryStatusPending})
	if err != nil {
		log.Printf("summary worker: querying pending commits failed: %v", err)
		return
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var c appdb.Commit
		if err := cursor.Decode(&c); err != nil {
			log.Printf("summary worker: decoding commit failed: %v", err)
			continue
		}
		w.summarizeOne(ctx, c)
	}
}

func (w *Worker) summarizeOne(ctx context.Context, c appdb.Commit) {
	summary, err := w.client.SummarizeCommitMessage(ctx, c.Message)
	if err != nil {
		attempts := c.Attempts + 1
		update := bson.M{"attempts": attempts}
		if attempts >= maxAttempts {
			update["summaryStatus"] = appdb.SummaryStatusFailed
			log.Printf("summary worker: giving up on commit %s after %d attempts: %v", c.CommitHash, attempts, err)
		} else {
			log.Printf("summary worker: attempt %d/%d failed for commit %s: %v (will retry next tick)",
				attempts, maxAttempts, c.CommitHash, err)
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
