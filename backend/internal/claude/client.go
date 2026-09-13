// Package claude generates one-sentence commit summaries via the Anthropic
// Messages API, using the commit message only (no diff) - see the README
// for why: diffs cost 5-50x more per commit with no cap on worst-case size,
// for a marginal quality gain on a one-sentence summary.
package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const systemPrompt = "You summarize a single git commit message in one short, specific sentence " +
	"for a commit feed. Respond with only the summary sentence - no preamble, no quotation marks, " +
	"no trailing period-less fragments, no restating that it's a commit."

// Client wraps the Anthropic SDK for the one thing this app needs from it:
// turning a commit message into a short summary.
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

// SummarizeCommitMessage returns a one-sentence summary of a commit message.
func (c *Client) SummarizeCommitMessage(ctx context.Context, commitMessage string) (string, error) {
	resp, err := c.sdk.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 100,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(commitMessage)),
		},
	})
	if err != nil {
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
