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

// anthropicError is the shape of a non-streaming error body, and also of
// the payload on an in-stream "event: error" (both nest error the same way).
type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// anthropicStreamEvent covers the union of fields used across the event
// types llmc cares about (content_block_delta, message_stop, error).
// Unrelated fields on other event types (message_start, ping,
// content_block_start, message_delta) are simply left unmarshaled.
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Error *anthropicError `json:"error"`
}

// Stream sends model + messages to Anthropic's /v1/messages endpoint with
// stream: true and emits one Token per text delta as they arrive over SSE,
// followed by a final Done token.
func (p *Anthropic) Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error) {
	if p.APIKey == "" {
		return nil, fmt.Errorf("anthropic: no API key configured (BYOK — set the env var named in config.toml)")
	}
	if p.Endpoint == "" {
		return nil, fmt.Errorf("anthropic: no endpoint configured")
	}

	system, msgs := splitSystem(messages)

	body, err := json.Marshal(anthropicRequest{
		Model:     model,
		System:    system,
		Messages:  msgs,
		MaxTokens: anthropicMaxTokens,
		Stream:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	ch := make(chan Token, 4)
	go func() {
		defer close(ch)

		resp, err := client.Do(httpReq)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		// A non-200 response is a plain JSON error body, not an SSE
		// stream — read it whole rather than trying to scan it as SSE.
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			var errBody struct {
				Error *anthropicError `json:"error"`
			}
			msg := resp.Status
			if json.Unmarshal(raw, &errBody) == nil && errBody.Error != nil {
				msg = fmt.Sprintf("%s: %s", errBody.Error.Type, errBody.Error.Message)
			}
			ch <- Token{Err: fmt.Errorf("anthropic: %s", msg)}
			return
		}

		scanner := newSSEScanner(resp.Body)
		for {
			payload, ok := scanner.Next()
			if !ok {
				break
			}

			var event anthropicStreamEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				ch <- Token{Err: fmt.Errorf("anthropic: decode stream event: %w", err)}
				return
			}

			switch event.Type {
			case "content_block_delta":
				if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
					ch <- Token{Text: event.Delta.Text}
				}
			case "error":
				msg := "stream error"
				if event.Error != nil {
					msg = fmt.Sprintf("%s: %s", event.Error.Type, event.Error.Message)
				}
				ch <- Token{Err: fmt.Errorf("anthropic: %s", msg)}
				return
			case "message_stop":
				ch <- Token{Done: true}
				return
				// message_start, ping, content_block_start,
				// content_block_stop, message_delta: nothing llmc needs
				// yet — ignored.
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: reading stream: %w", err)}
			return
		}
		// Stream ended without an explicit message_stop (shouldn't happen
		// in practice, but don't leave the caller hanging on the channel).
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
