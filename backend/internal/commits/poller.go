// Package commits implements the background polling worker that detects new
// commits on subscribed repos and exposes the Commit Feed API.
package commits

import (
	"context"
	"errors"
	"fmt"
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

// Poller periodically checks every actively-subscribed repo for commits
// newer than the last one it saw, and records them (with summaryStatus
// "pending" - the summary worker picks those up to generate AI summaries).
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

	// Bounds total commits collected across ALL repos this tick, so a large
	// backlog (many repos, or the backend having been offline a while)
	// doesn't process an unbounded amount of work in one go - repos beyond
	// the budget just get their turn on the next tick.
	budget := p.cfg.MaxCommitsPerPollTick

	for cursor.Next(ctx) {
		var repo appdb.Repo
		if err := cursor.Decode(&repo); err != nil {
			log.Printf("poller: decoding repo failed: %v", err)
			continue
		}

		if repo.RateLimitedUntil != nil && time.Now().Before(*repo.RateLimitedUntil) {
			continue // still backing off from a recent 429 - see checkRateLimit below
		}

		if budget <= 0 {
			log.Printf("poller: per-tick commit budget (%d) exhausted - remaining repos will be checked next tick",
				p.cfg.MaxCommitsPerPollTick)
			break
		}

		inserted, err := p.pollRepo(ctx, repo, budget)
		if err != nil {
			log.Printf("poller: %s/%s: %v", repo.Workspace, repo.RepoSlug, err)
			continue
		}
		budget -= inserted
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

// pollRepo polls one repo, inserting at most tickBudget new commits.
// Returns how many commits were actually inserted, so the caller can debit
// its tick budget.
//
// If a repo has more new commits than fit in one poll's cap, this does NOT
// jump the cursor straight to the newest commit (that would permanently
// skip everything between the old cursor and the cap boundary - Bitbucket's
// commits API only pages newest-to-oldest, so simply restarting from the
// tip next tick would re-discover the same newest commits forever and
// never progress toward the backlog). Instead it saves how far it got
// (CatchUpResumeURL) and resumes from exactly there next tick, walking a
// large backlog incrementally - possibly over many ticks - without ever
// losing commits in between.
func (p *Poller) pollRepo(ctx context.Context, repo appdb.Repo, tickBudget int) (int, error) {
	httpClient, err := p.workingHTTPClient(ctx, repo)
	if err != nil {
		return 0, err
	}

	var page *bitbucket.CommitsPage
	var newest string
	if repo.CatchUpResumeURL != "" {
		page, err = bitbucket.FetchCommitsPage(httpClient, repo.CatchUpResumeURL)
		newest = repo.CatchUpNewest
	} else {
		page, err = bitbucket.FetchCommitsPage(httpClient, bitbucket.CommitsURL(repo.Workspace, repo.RepoSlug))
		newest = repo.LastSeenCommitHash
		if len(page.Values) > 0 {
			newest = page.Values[0].Hash
		}
	}
	if err != nil {
		var rateLimitErr *bitbucket.RateLimitError
		if errors.As(err, &rateLimitErr) {
			until := time.Now().Add(rateLimitErr.RetryAfter)
			log.Printf("poller: %s/%s: rate limited, backing off until %s",
				repo.Workspace, repo.RepoSlug, until.Format(time.RFC3339))
			if _, uerr := p.db.Collection("repos").UpdateOne(ctx,
				bson.M{"_id": repo.ID}, bson.M{"$set": bson.M{"rateLimitedUntil": until}},
			); uerr != nil {
				return 0, uerr
			}
			return 0, nil // not a "real" error - handled
		}
		return 0, err
	}

	if repo.LastSeenCommitHash == "" {
		// First poll ever for this repo: per product decision, no backfill -
		// just establish the baseline so only commits from here on count as "new".
		// (CatchUpResumeURL is never set at this point - see pollRepo's caller.)
		return 0, p.updateCursor(ctx, repo.ID, newest)
	}

	effectiveCap := p.cfg.MaxCommitsPerRepoPerPoll
	if tickBudget < effectiveCap {
		effectiveCap = tickBudget
	}

	newCommits, found, resumeURL, err := p.collectNewCommits(httpClient, page, repo.LastSeenCommitHash, effectiveCap)
	if err != nil {
		return 0, err
	}

	// newCommits is newest-first; insert oldest-first so createdAt ordering
	// in the DB matches commit chronology.
	inserted := 0
	for i := len(newCommits) - 1; i >= 0; i-- {
		if err := p.insertCommit(ctx, httpClient, repo, newCommits[i]); err != nil {
			log.Printf("poller: %s/%s: inserting commit %s failed: %v",
				repo.Workspace, repo.RepoSlug, newCommits[i].Hash, err)
			continue
		}
		inserted++
	}

	if resumeURL != "" {
		log.Printf("poller: %s/%s: hit the %d-commit cap with more backlog to walk - "+
			"resuming from here next poll (%d commits captured this round)",
			repo.Workspace, repo.RepoSlug, effectiveCap, len(newCommits))
		return inserted, p.saveCatchUpProgress(ctx, repo.ID, resumeURL, newest)
	}
	if !found {
		log.Printf("poller: %s/%s: reached the end of available history without finding the "+
			"previous cursor commit (likely a rewritten history) - treating everything walked as new",
			repo.Workspace, repo.RepoSlug)
	}
	return inserted, p.updateCursor(ctx, repo.ID, newest)
}

// workingHTTPClient tries each of repo's subscribers in turn (oldest
// subscription first) until it finds one whose token can actually be used
// to reach the repo, so one subscriber losing access doesn't stop the repo
// from polling for everyone else still subscribed.
func (p *Poller) workingHTTPClient(ctx context.Context, repo appdb.Repo) (*http.Client, error) {
	users, err := p.subscriberUsers(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, errors.New("no active subscribers")
	}

	var lastErr error
	for _, user := range users {
		token, err := auth.ValidToken(ctx, p.cfg, p.oauthCfg, p.db, user)
		if err != nil {
			lastErr = fmt.Errorf("user %s: %w", user.Username, err)
			continue
		}
		httpClient := p.oauthCfg.Client(ctx, token)

		// Confirm the token actually works for this repo before committing
		// to it - a token can refresh fine but still lack access (e.g. the
		// user lost repo permissions without their Bitbucket session itself
		// being revoked).
		if _, err := bitbucket.FetchRepo(httpClient, repo.Workspace, repo.RepoSlug); err != nil {
			lastErr = fmt.Errorf("user %s: %w", user.Username, err)
			continue
		}
		return httpClient, nil
	}
	return nil, fmt.Errorf("no subscriber's token worked for %s/%s, last error: %w", repo.Workspace, repo.RepoSlug, lastErr)
}

// collectNewCommits walks pages (starting from an already-fetched first
// page) collecting commits until it finds lastSeenHash, or runs out of
// pages, or finishes a page with at least maxCommits collected.
//
// The cap is only checked at page boundaries, never mid-page: stopping
// partway through a page would make its remainder unrecoverable (resuming
// from that page's Next skips whatever was left unprocessed in it). This
// means a poll can collect somewhat more than maxCommits (up to one page's
// worth over, currently 50 - see CommitsURL's pagelen), which is a fine
// trade for never silently losing commits.
//
// Returns (commits, found, resumeURL, err). When found is false and
// resumeURL is non-empty, there's more backlog beyond what was collected -
// the caller should resume from resumeURL next time rather than treating
// this as done. resumeURL is empty when either lastSeenHash was found, or
// history was exhausted (page.Next == "") without finding it.
func (p *Poller) collectNewCommits(httpClient *http.Client, firstPage *bitbucket.CommitsPage, lastSeenHash string, maxCommits int) ([]bitbucket.Commit, bool, string, error) {
	var newCommits []bitbucket.Commit
	page := firstPage

	for {
		for _, c := range page.Values {
			if c.Hash == lastSeenHash {
				return newCommits, true, "", nil
			}
			newCommits = append(newCommits, c)
		}
		if len(newCommits) >= maxCommits {
			return newCommits, false, page.Next, nil // may itself be "" if this was the last page
		}
		if page.Next == "" {
			return newCommits, false, "", nil
		}
		next, err := bitbucket.FetchCommitsPage(httpClient, page.Next)
		if err != nil {
			return newCommits, false, "", err
		}
		page = next
	}
}

// insertCommit fetches the commit's diff, unless SUMMARIZE_WITH_DIFF=false
// (best-effort when enabled - a fetch failure or an over-cap diff just
// means Diff stays empty, not a failed insert), and stores the commit,
// pending a summary.
func (p *Poller) insertCommit(ctx context.Context, httpClient *http.Client, repo appdb.Repo, c bitbucket.Commit) error {
	var diff string
	if p.cfg.SummarizeWithDiff {
		var err error
		diff, err = bitbucket.FetchDiff(httpClient, repo.Workspace, repo.RepoSlug, c.Hash, p.cfg.MaxDiffBytes)
		if err != nil {
			if errors.Is(err, bitbucket.ErrDiffTooLarge) {
				log.Printf("poller: %s/%s: commit %s diff exceeds size cap, summarizing from message only",
					repo.Workspace, repo.RepoSlug, c.Hash)
			} else {
				log.Printf("poller: %s/%s: fetching diff for commit %s failed, summarizing from message only: %v",
					repo.Workspace, repo.RepoSlug, c.Hash, err)
			}
			diff = ""
		}
	}

	doc := appdb.Commit{
		RepoID:        repo.ID,
		CommitHash:    c.Hash,
		Message:       c.Message,
		AuthorName:    c.Author.User.DisplayName,
		AuthorRaw:     c.Author.Raw,
		CommittedAt:   c.Date,
		BitbucketURL:  c.Links.HTML.Href,
		Diff:          diff,
		SummaryStatus: appdb.SummaryStatusPending,
		CreatedAt:     time.Now(),
	}
	filter := bson.M{"repoId": repo.ID, "commitHash": c.Hash}
	update := bson.M{"$setOnInsert": doc}
	_, err := p.db.Collection("commits").UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	return err
}

// updateCursor finalizes a repo as fully caught up: advances the cursor to
// newestHash and clears any catch-up state (there's nothing left to resume).
func (p *Poller) updateCursor(ctx context.Context, repoID bson.ObjectID, newestHash string) error {
	now := time.Now()
	_, err := p.db.Collection("repos").UpdateOne(ctx,
		bson.M{"_id": repoID},
		bson.M{
			"$set":   bson.M{"lastSeenCommitHash": newestHash, "lastPolledAt": now},
			"$unset": bson.M{"catchUpResumeUrl": "", "catchUpNewest": ""},
		},
	)
	return err
}

// saveCatchUpProgress records that a repo hit its per-poll cap with more
// backlog still to walk - lastSeenCommitHash is deliberately left
// unchanged (not caught up yet); resumeURL is where the next poll should
// continue from, and newest is preserved so it can be applied once catch-up
// finishes (see pollRepo).
func (p *Poller) saveCatchUpProgress(ctx context.Context, repoID bson.ObjectID, resumeURL, newest string) error {
	now := time.Now()
	_, err := p.db.Collection("repos").UpdateOne(ctx,
		bson.M{"_id": repoID},
		bson.M{"$set": bson.M{
			"lastPolledAt":     now,
			"catchUpResumeUrl": resumeURL,
			"catchUpNewest":    newest,
		}},
	)
	return err
}

// findSubscriberUser returns any one user currently subscribed to repoID.
// Used where only a "some valid token" is needed (e.g. verification/tests),
// not the full multi-subscriber fallback - see workingHTTPClient for that.
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

// subscriberUsers returns every user currently subscribed to repoID, oldest
// subscription first - the order workingHTTPClient tries them in.
func (p *Poller) subscriberUsers(ctx context.Context, repoID bson.ObjectID) ([]*appdb.User, error) {
	opts := options.Find().SetSort(bson.M{"createdAt": 1})
	cursor, err := p.db.Collection("repoSubscriptions").Find(ctx, bson.M{"repoId": repoID}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var subs []appdb.RepoSubscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, err
	}

	users := make([]*appdb.User, 0, len(subs))
	for _, sub := range subs {
		var user appdb.User
		if err := p.db.Collection("users").FindOne(ctx, bson.M{"_id": sub.UserID}).Decode(&user); err != nil {
			log.Printf("poller: loading subscriber %s failed, skipping: %v", sub.UserID.Hex(), err)
			continue
		}
		users = append(users, &user)
	}
	return users, nil
}
