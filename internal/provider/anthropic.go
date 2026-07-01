package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	anthropicVersion   = "2023-06-01"
	anthropicMaxTokens = 4096 // no per-call override yet; fine for v1
)

// Anthropic implements Provider for api.anthropic.com's native /v1/messages
// wire format — the "genuine outlier" from DESIGN.md. Everything
// OpenAI-compatible goes through OpenAICompatible instead.
type Anthropic struct {
	Endpoint string       // e.g. https://api.anthropic.com/v1/messages
	APIKey   string       // BYOK: resolved from config's api_key_env by the caller
	Client   *http.Client // optional; defaults to http.DefaultClient
}

func (p *Anthropic) Name() string { return "anthropic" }

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Stream proves out BYOK + config loading (milestone 1): it makes a single
// non-streaming request and delivers the whole reply as one Token, followed
// by a Done token. Real incremental SSE streaming is milestone 2 — the
// channel-based signature is already shaped for that, only the request body
// (stream: true) and response handling change.
func (p *Anthropic) Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error) {
	if p.Endpoint == "" {
		return nil, fmt.Errorf("anthropic: no endpoint configured")
	}

	system, msgs := splitSystem(messages)

	body, err := json.Marshal(anthropicRequest{
		Model:     model,
		System:    system,
		Messages:  msgs,
		MaxTokens: anthropicMaxTokens,
		Stream:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if p.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.APIKey)
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	ch := make(chan Token, 2)
	go func() {
		defer close(ch)

		resp, err := client.Do(httpReq)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: read response: %w", err)}
			return
		}

		var parsed anthropicResponse
		if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: decode response (status %s): %w", resp.Status, jsonErr)}
			return
		}

		if resp.StatusCode != http.StatusOK {
			msg := resp.Status
			if parsed.Error != nil {
				msg = fmt.Sprintf("%s: %s", parsed.Error.Type, parsed.Error.Message)
			}
			ch <- Token{Err: fmt.Errorf("anthropic: %s", msg)}
			return
		}

		var text string
		for _, block := range parsed.Content {
			if block.Type == "text" {
				text += block.Text
			}
		}
		ch <- Token{Text: text}
		ch <- Token{Done: true}
	}()

	return ch, nil
}

// splitSystem pulls a leading role:"system" message out of the slice, since
// Anthropic's API takes system as a top-level field, not a message.
func splitSystem(messages []Message) (system string, rest []anthropicMessage) {
	rest = make([]anthropicMessage, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" && system == "" {
			system = m.Content
			continue
		}
		rest = append(rest, anthropicMessage{Role: m.Role, Content: m.Content})
	}
	return system, rest
}
