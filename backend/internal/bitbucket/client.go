// Package bitbucket wraps the Bitbucket REST API calls this app needs.
package bitbucket

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ErrRepoNotFound means Bitbucket returned 404 for a repo lookup - either the
// workspace/repo-slug doesn't exist, or the authenticated user has no access
// to it. Bitbucket doesn't distinguish the two (to avoid leaking the
// existence of private repos), so neither do we.
var ErrRepoNotFound = errors.New("repo not found or not accessible")

// ErrDiffTooLarge means the commit's diff exceeded the caller's size cap.
// Not a real error - callers should treat this as "no diff available" and
// fall back to summarizing the commit message alone.
var ErrDiffTooLarge = errors.New("diff exceeds size cap")

const apiBase = "https://api.bitbucket.org/2.0"

// RateLimitError means Bitbucket returned 429. Callers (the poller) should
// back off this repo until RetryAfter has elapsed rather than hammering it
// again next tick.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("bitbucket rate limit hit, retry after %s", e.RetryAfter)
}

// defaultRetryAfter is used when Bitbucket returns 429 without a usable
// Retry-After header.
const defaultRetryAfter = 60 * time.Second

// checkRateLimit returns a *RateLimitError if resp is a 429, else nil.
func checkRateLimit(resp *http.Response) error {
	if resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	retryAfter := defaultRetryAfter
	if raw := resp.Header.Get("Retry-After"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			retryAfter = time.Duration(secs) * time.Second
		}
	}
	return &RateLimitError{RetryAfter: retryAfter}
}

// User is the subset of Bitbucket's /2.0/user response we care about.
type User struct {
	UUID        string `json:"uuid"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Links       struct {
		Avatar struct {
			Href string `json:"href"`
		} `json:"avatar"`
	} `json:"links"`
}

// FetchCurrentUser calls GET /2.0/user using the given HTTP client, which is
// expected to be an oauth2.Config.Client(ctx, token) - i.e. one that already
// attaches the bearer token to every request.
func FetchCurrentUser(httpClient *http.Client) (*User, error) {
	resp, err := httpClient.Get(apiBase + "/user")
	if err != nil {
		return nil, fmt.Errorf("calling bitbucket /user: %w", err)
	}
	defer resp.Body.Close()

	if err := checkRateLimit(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitbucket /user returned status %d", resp.StatusCode)
	}

	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decoding bitbucket /user response: %w", err)
	}
	return &u, nil
}

// Repo is the subset of Bitbucket's repository object we care about.
type Repo struct {
	UUID      string `json:"uuid"`
	FullName  string `json:"full_name"`
	IsPrivate bool   `json:"is_private"`
}

// FetchRepo calls GET /2.0/repositories/{workspace}/{repoSlug} to confirm the
// repo exists and the authenticated user (via httpClient) can see it.
// Returns ErrRepoNotFound on a 404.
func FetchRepo(httpClient *http.Client, workspace, repoSlug string) (*Repo, error) {
	path := fmt.Sprintf("%s/repositories/%s/%s", apiBase, url.PathEscape(workspace), url.PathEscape(repoSlug))
	resp, err := httpClient.Get(path)
	if err != nil {
		return nil, fmt.Errorf("calling bitbucket repo lookup: %w", err)
	}
	defer resp.Body.Close()

	if err := checkRateLimit(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrRepoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitbucket repo lookup returned status %d", resp.StatusCode)
	}

	var r Repo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding bitbucket repo response: %w", err)
	}
	return &r, nil
}

// Commit is the subset of Bitbucket's commit object we care about. Bitbucket
// returns commits across all branches, newest first.
type Commit struct {
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	Date    time.Time `json:"date"`
	Author  struct {
		Raw  string `json:"raw"`
		User struct {
			DisplayName string `json:"display_name"`
		} `json:"user"`
	} `json:"author"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// CommitsPage is one page of Bitbucket's paginated commits response.
// Next, if non-empty, is the full URL to fetch the next page.
type CommitsPage struct {
	Values []Commit `json:"values"`
	Next   string   `json:"next"`
}

// CommitsURL builds the first-page URL for a repo's commit history,
// requesting the largest page size Bitbucket allows to minimize round-trips.
func CommitsURL(workspace, repoSlug string) string {
	return fmt.Sprintf("%s/repositories/%s/%s/commits?pagelen=50",
		apiBase, url.PathEscape(workspace), url.PathEscape(repoSlug))
}

// FetchCommitsPage fetches one page of commits from pageURL (either the
// result of CommitsURL, or a previous page's Next). Returns *RateLimitError
// on a 429.
func FetchCommitsPage(httpClient *http.Client, pageURL string) (*CommitsPage, error) {
	resp, err := httpClient.Get(pageURL)
	if err != nil {
		return nil, fmt.Errorf("calling bitbucket commits: %w", err)
	}
	defer resp.Body.Close()

	if err := checkRateLimit(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitbucket commits returned status %d", resp.StatusCode)
	}

	var page CommitsPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decoding bitbucket commits response: %w", err)
	}
	return &page, nil
}

// FetchDiff returns the unified diff for a single commit (against its first
// parent), for feeding to the summarizer alongside the commit message.
// Returns ErrDiffTooLarge if the diff exceeds maxBytes - callers should
// treat that as "no diff", not a hard failure. Returns *RateLimitError on a
// 429.
func FetchDiff(httpClient *http.Client, workspace, repoSlug, commitHash string, maxBytes int) (string, error) {
	path := fmt.Sprintf("%s/repositories/%s/%s/diff/%s",
		apiBase, url.PathEscape(workspace), url.PathEscape(repoSlug), url.PathEscape(commitHash))
	resp, err := httpClient.Get(path)
	if err != nil {
		return "", fmt.Errorf("calling bitbucket diff: %w", err)
	}
	defer resp.Body.Close()

	if err := checkRateLimit(resp); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bitbucket diff returned status %d", resp.StatusCode)
	}

	// Read at most maxBytes+1: if we get exactly that much, the real diff
	// is at or past the cap, so treat it as too large without ever
	// buffering the whole thing.
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", fmt.Errorf("reading bitbucket diff: %w", err)
	}
	if len(data) > maxBytes {
		return "", ErrDiffTooLarge
	}
	return string(data), nil
}
