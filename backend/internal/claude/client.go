// Package claude generates one-sentence commit summaries via the Anthropic
// Messages API, from the commit message and (when available) its diff -
// see bitbucket.FetchDiff for the size cap and fallback behavior.
package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"commit-notification-app/backend/internal/llmerr"
)

const systemPrompt = "You summarize a single git commit for a commit feed, in one short, specific " +
	"sentence. You may be given just the commit message, or the message plus its diff. When a diff " +
	"is provided, base your summary primarily on what the diff actually changes - the message is " +
	"just supporting context and may be vague or inaccurate. When no diff is provided, summarize the " +
	"message as given. Respond with only the summary sentence - no preamble, no quotation marks, no " +
	"trailing period-less fragments, no restating that it's a commit. Keep it under 15 words. If " +
	"nothing meaningful can be determined (e.g. an uninformative message like \"wip\" and no diff), " +
	"keep the summary equally brief instead of explaining that it's unclear."

// Client wraps the Anthropic SDK for the one thing this app needs from it:
// turning a commit (message + optional diff) into a short summary.
type Client struct {
	sdk   anthropic.Client
	model string
}

func New(apiKey, model string) *Client {
	return &Client{
		sdk:   anthropic.NewClient(option.WithAPIKey(apiKey)),
		model: model,
	}
}

// SummarizeCommit returns a one-sentence summary of a commit. diff may be
// empty (see bitbucket.FetchDiff), in which case the summary is based on
// commitMessage alone.
func (c *Client) SummarizeCommit(ctx context.Context, commitMessage, diff string) (string, error) {
	resp, err := c.sdk.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 100,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildUserContent(commitMessage, diff))),
		},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", &llmerr.APIError{Provider: "claude", StatusCode: apiErr.StatusCode, Message: apiErr.Error()}
		}
		return "", fmt.Errorf("calling claude: %w", err)
	}

	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			summary := strings.TrimSpace(tb.Text)
			if summary != "" {
				return summary, nil
			}
		}
	}
	return "", errors.New("claude response contained no text")
}

func buildUserContent(commitMessage, diff string) string {
	if diff == "" {
		return commitMessage
	}
	return "Commit message:\n" + commitMessage + "\n\nDiff:\n" + diff
}
