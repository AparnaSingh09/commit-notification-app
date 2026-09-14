// Package groq generates one-sentence commit summaries via Groq's
// OpenAI-compatible Chat Completions API, running open-weight models
// (currently Qwen - see config.GroqModel), from the commit message and
// (when available) its diff - see bitbucket.FetchDiff for the size cap
// and fallback behavior.
package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"commit-notification-app/backend/internal/llmerr"
)

const (
	apiURL = "https://api.groq.com/openai/v1/chat/completions"

	systemPrompt = "You summarize a single git commit for a commit feed, in one short, specific " +
		"sentence. You may be given just the commit message, or the message plus its diff. When a diff " +
		"is provided, base your summary primarily on what the diff actually changes - the message is " +
		"just supporting context and may be vague or inaccurate. When no diff is provided, summarize the " +
		"message as given. Respond with only the summary sentence - no preamble, no quotation marks, no " +
		"trailing period-less fragments, no restating that it's a commit. Keep it under 15 words. If " +
		"nothing meaningful can be determined (e.g. an uninformative message like \"wip\" and no diff), " +
		"keep the summary equally brief instead of explaining that it's unclear."
)

// Client wraps Groq's Chat Completions API for the one thing this app needs:
// turning a commit (message + optional diff) into a short summary.
type Client struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func New(apiKey, model string) *Client {
	return &Client{apiKey: apiKey, model: model, httpClient: &http.Client{}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// SummarizeCommit returns a one-sentence summary of a commit. diff may be
// empty (see bitbucket.FetchDiff), in which case the summary is based on
// commitMessage alone. Same signature as claude.Client.SummarizeCommit, so
// the two are interchangeable behind the claude.Summarizer interface.
func (c *Client) SummarizeCommit(ctx context.Context, commitMessage, diff string) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildUserContent(commitMessage, diff)},
		},
		MaxTokens: 100,
	})
	if err != nil {
		return "", fmt.Errorf("encoding groq request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("building groq request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling groq: %w", err)
	}
	defer resp.Body.Close()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decoding groq response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := "(no error detail in response body)"
		if parsed.Error != nil {
			msg = parsed.Error.Message
		}
		return "", &llmerr.APIError{Provider: "groq", StatusCode: resp.StatusCode, Message: msg}
	}

	if len(parsed.Choices) == 0 {
		return "", errors.New("groq response contained no choices")
	}
	summary := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if summary == "" {
		return "", errors.New("groq response contained no text")
	}
	return summary, nil
}

func buildUserContent(commitMessage, diff string) string {
	if diff == "" {
		return commitMessage
	}
	return "Commit message:\n" + commitMessage + "\n\nDiff:\n" + diff
}
