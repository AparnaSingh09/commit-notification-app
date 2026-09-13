// Package commits implements the background polling worker that detects new
// commits on subscribed repos (step 4) and will expose the commit feed API
// (step 5's summaries feed into step 6's feed endpoint, both to come later).
package commits

import (
	"context"
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/oauth2"

	"commit-notification-app/backend/internal/auth"
	"commit-notification-app/backend/internal/bitbucket"
	"commit-notification-app/backend/internal/config"
	appdb "commit-notification-app/backend/internal/db"
)

// maxCommitsPerPoll bounds how many commits a single poll of one repo will
// walk back through looking for the previously-seen commit, so a repo with
// an unexpectedly huge burst of activity (or a rewritten history that never
// reconnects with our cursor) can't make one tick run forever.
const maxCommitsPerPoll = 200

// Poller periodically checks every actively-subscribed repo for commits
// newer than the last one it saw, and records them (with summaryStatus
// "pending" - step 5 picks those up to generate AI summaries).
type Poller struct {
	cfg      config.Config
	oauthCfg *oauth2.Config
	db       *mongo.Database
	interval time.Duration
}

func NewPoller(cfg config.Config, oauthCfg *oauth2.Config, db *mongo.Database, interval time.Duration) *Poller {
	return &Poller{cfg: cfg, oauthCfg: oauthCfg, db: db, interval: interval}
}

// Run blocks, ticking every p.interval, until ctx is canceled. It runs one
// tick immediately on start rather than waiting a full interval first.
func (p *Poller) Run(ctx context.Context) {
	p.tick(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("polling worker stopping")
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	repoIDs, err := p.activeRepoIDs(ctx)
	if err != nil {
		log.Printf("poller: listing actively-subscribed repos failed: %v", err)
		return
	}
	if len(repoIDs) == 0 {
		return
	}

	cursor, err := p.db.Collection("repos").Find(ctx, bson.M{"_id": bson.M{"$in": repoIDs}})
	if err != nil {
		log.Printf("poller: loading repos failed: %v", err)
		return
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var repo appdb.Repo
		if err := cursor.Decode(&repo); err != nil {
			log.Printf("poller: decoding repo failed: %v", err)
			continue
		}
		if err := p.pollRepo(ctx, repo); err != nil {
			log.Printf("poller: %s/%s: %v", repo.Workspace, repo.RepoSlug, err)
		}
	}
}

// activeRepoIDs returns the distinct set of repos that currently have at
// least one subscriber - a repo doc can outlive its last subscription (we
// don't delete it on unsubscribe), and there's no point polling it, or
// spending a now-unauthorized user's token on it, once nobody's watching it.
func (p *Poller) activeRepoIDs(ctx context.Context) ([]bson.ObjectID, error) {
	res := p.db.Collection("repoSubscriptions").Distinct(ctx, "repoId", bson.M{})
	var ids []bson.ObjectID
	if err := res.Decode(&ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (p *Poller) pollRepo(ctx context.Context, repo appdb.Repo) error {
	user, err := p.findSubscriberUser(ctx, repo.ID)
	if err != nil {
		return err
	}

	token, err := auth.ValidToken(ctx, p.cfg, p.oauthCfg, p.db, user)
	if err != nil {
		return err
	}
	httpClient := p.oauthCfg.Client(ctx, token)

	firstPage, err := bitbucket.FetchCommitsPage(httpClient, bitbucket.CommitsURL(repo.Workspace, repo.RepoSlug))
	if err != nil {
		return err
	}

	newest := repo.LastSeenCommitHash
	if len(firstPage.Values) > 0 {
		newest = firstPage.Values[0].Hash
	}

	if repo.LastSeenCommitHash == "" {
		// First poll ever for this repo: per product decision, no backfill -
		// just establish the baseline so only commits from here on count as "new".
		return p.updateCursor(ctx, repo.ID, newest)
	}

	newCommits, found, err := p.collectNewCommits(ctx, httpClient, firstPage, repo.LastSeenCommitHash)
	if err != nil {
		return err
	}
	if !found {
		log.Printf("poller: %s/%s: did not find previous cursor commit within %d commits - "+
			"history may have been rewritten; recording %d commits as new",
			repo.Workspace, repo.RepoSlug, maxCommitsPerPoll, len(newCommits))
	}

	// newCommits is newest-first; insert oldest-first so createdAt ordering
	// in the DB matches commit chronology.
	for i := len(newCommits) - 1; i >= 0; i-- {
		if err := p.insertCommit(ctx, repo.ID, newCommits[i]); err != nil {
			log.Printf("poller: %s/%s: inserting commit %s failed: %v",
				repo.Workspace, repo.RepoSlug, newCommits[i].Hash, err)
		}
	}

	return p.updateCursor(ctx, repo.ID, newest)
}

// collectNewCommits walks pages (starting from an already-fetched first
// page) collecting commits until it finds lastSeenHash, or hits
// maxCommitsPerPoll, or runs out of pages.
func (p *Poller) collectNewCommits(ctx context.Context, httpClient *http.Client, firstPage *bitbucket.CommitsPage, lastSeenHash string) ([]bitbucket.Commit, bool, error) {
	var newCommits []bitbucket.Commit
	page := firstPage

	for {
		for _, c := range page.Values {
			if c.Hash == lastSeenHash {
				return newCommits, true, nil
			}
			newCommits = append(newCommits, c)
			if len(newCommits) >= maxCommitsPerPoll {
				return newCommits, false, nil
			}
		}
		if page.Next == "" {
			return newCommits, false, nil
		}
		next, err := bitbucket.FetchCommitsPage(httpClient, page.Next)
		if err != nil {
			return newCommits, false, err
		}
		page = next
	}
}

func (p *Poller) insertCommit(ctx context.Context, repoID bson.ObjectID, c bitbucket.Commit) error {
	doc := appdb.Commit{
		RepoID:        repoID,
		CommitHash:    c.Hash,
		Message:       c.Message,
		AuthorName:    c.Author.User.DisplayName,
		AuthorRaw:     c.Author.Raw,
		CommittedAt:   c.Date,
		BitbucketURL:  c.Links.HTML.Href,
		SummaryStatus: appdb.SummaryStatusPending,
		CreatedAt:     time.Now(),
	}
	filter := bson.M{"repoId": repoID, "commitHash": c.Hash}
	update := bson.M{"$setOnInsert": doc}
	_, err := p.db.Collection("commits").UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	return err
}

func (p *Poller) updateCursor(ctx context.Context, repoID bson.ObjectID, newestHash string) error {
	now := time.Now()
	_, err := p.db.Collection("repos").UpdateOne(ctx,
		bson.M{"_id": repoID},
		bson.M{"$set": bson.M{"lastSeenCommitHash": newestHash, "lastPolledAt": now}},
	)
	return err
}

// findSubscriberUser returns any one user currently subscribed to repoID, so
// their Bitbucket token can be used to poll it. Known limitation: if that
// particular user's token turns out to be unusable (e.g. they revoked
// access), this poll fails for the whole repo even if another subscriber's
// token would work - fine for now, worth revisiting if it becomes a problem.
func (p *Poller) findSubscriberUser(ctx context.Context, repoID bson.ObjectID) (*appdb.User, error) {
	var sub appdb.RepoSubscription
	if err := p.db.Collection("repoSubscriptions").FindOne(ctx, bson.M{"repoId": repoID}).Decode(&sub); err != nil {
		return nil, err
	}
	var user appdb.User
	if err := p.db.Collection("users").FindOne(ctx, bson.M{"_id": sub.UserID}).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}
