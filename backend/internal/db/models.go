package db

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// User is the "users" collection document. OAuth tokens are stored encrypted
// (see internal/crypto) - never store them in plaintext.
type User struct {
	ID              bson.ObjectID `bson:"_id,omitempty"`
	BitbucketUUID   string        `bson:"bitbucketUuid"`
	Username        string        `bson:"username"`
	DisplayName     string        `bson:"displayName"`
	AvatarURL       string        `bson:"avatarUrl"`
	AccessTokenEnc  []byte        `bson:"accessTokenEnc"`
	RefreshTokenEnc []byte        `bson:"refreshTokenEnc"`
	TokenExpiresAt  time.Time     `bson:"tokenExpiresAt"`
	CreatedAt       time.Time     `bson:"createdAt"`
	UpdatedAt       time.Time     `bson:"updatedAt"`
}

// Repo is the "repos" collection document - one per Bitbucket repo, shared
// across every user subscribed to it (not duplicated per subscription).
type Repo struct {
	ID                 bson.ObjectID `bson:"_id,omitempty"`
	Workspace          string        `bson:"workspace"`
	RepoSlug           string        `bson:"repoSlug"`
	LastPolledAt       *time.Time    `bson:"lastPolledAt,omitempty"`
	LastSeenCommitHash string        `bson:"lastSeenCommitHash,omitempty"`
	// RateLimitedUntil, when set and in the future, means Bitbucket 429'd
	// us for this repo - the poller skips it until this time passes,
	// instead of hammering the same rate limit every tick.
	RateLimitedUntil *time.Time `bson:"rateLimitedUntil,omitempty"`
	// CatchUpResumeURL, when set, means a previous poll hit the per-poll
	// commit cap before reaching LastSeenCommitHash, with more (older)
	// pages still unwalked. The poller resumes paging from here next tick
	// instead of restarting from the newest commit - walking a large
	// backlog incrementally across ticks without ever permanently skipping
	// the commits in between (restarting from the tip every tick would
	// just re-discover the same newest commits forever and never progress
	// toward the backlog). CatchUpNewest freezes what "newest" was when
	// catch-up began, since resumed pages no longer include it.
	CatchUpResumeURL string    `bson:"catchUpResumeUrl,omitempty"`
	CatchUpNewest    string    `bson:"catchUpNewest,omitempty"`
	CreatedAt        time.Time `bson:"createdAt"`
}

// RepoSubscription links a user to a repo they've subscribed to.
type RepoSubscription struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	UserID    bson.ObjectID `bson:"userId"`
	RepoID    bson.ObjectID `bson:"repoId"`
	CreatedAt time.Time     `bson:"createdAt"`
}

// Commit is the "commits" collection document - one per commit per repo
// (shared across every user subscribed to that repo), not per subscription.
type Commit struct {
	ID           bson.ObjectID `bson:"_id,omitempty"`
	RepoID       bson.ObjectID `bson:"repoId"`
	CommitHash   string        `bson:"commitHash"`
	Message      string        `bson:"message"`
	AuthorName   string        `bson:"authorName"`
	AuthorRaw    string        `bson:"authorRaw"`
	CommittedAt  time.Time     `bson:"committedAt"`
	BitbucketURL string        `bson:"bitbucketUrl"`
	// Diff is the commit's unified diff (capped, see config.MaxDiffBytes),
	// fetched once when the commit is first discovered. Empty if the diff
	// was too large or the fetch failed - the summarizer falls back to the
	// commit message alone in that case.
	Diff          string    `bson:"diff,omitempty"`
	AISummary     string    `bson:"aiSummary,omitempty"`
	SummaryStatus string    `bson:"summaryStatus"` // "pending" | "done" | "failed"
	Attempts      int       `bson:"attempts"`      // failed summary attempts so far - see config.MaxSummaryAttempts
	CreatedAt     time.Time `bson:"createdAt"`
}

const (
	SummaryStatusPending = "pending"
	SummaryStatusDone    = "done"
	SummaryStatusFailed  = "failed"
)
