package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

// Stream sends a streaming request and returns a channel of incremental
// tokens as they arrive via SSE.
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
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.APIKey != "" {
		httpReq.Header.Set("x-api-key", p.APIKey)
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	ch := make(chan Token, 8)
	go func() {
		defer close(ch)

		resp, err := client.Do(httpReq)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ch <- Token{Err: fmt.Errorf("anthropic: %s", resp.Status)}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		eventType := ""
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				eventType = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data := strings.TrimPrefix(line, "data: ")
				evt, parseErr := parseAnthropicEvent(eventType, data)
				if parseErr != nil {
					continue
				}
				if evt.text != "" {
					ch <- Token{Text: evt.text}
				}
				if evt.done {
					ch <- Token{Done: true}
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- Token{Err: fmt.Errorf("anthropic: read stream: %w", err)}
		} else {
			ch <- Token{Done: true}
		}
	}()

	return ch, nil
}

type anthropicParsedEvent struct {
	text string
	done bool
}

func parseAnthropicEvent(eventType, data string) (anthropicParsedEvent, error) {
	var raw struct {
		Type string `json:"type"`
		Delta *struct {
			Text string `json:"text"`
		} `json:"delta,omitempty"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return anthropicParsedEvent{}, err
	}

	switch raw.Type {
	case "content_block_delta":
		if raw.Delta != nil {
			return anthropicParsedEvent{text: raw.Delta.Text}, nil
		}
	case "error":
		msg := raw.Type
		if raw.Error != nil {
			msg = fmt.Sprintf("%s: %s", raw.Error.Type, raw.Error.Message)
		}
		return anthropicParsedEvent{}, fmt.Errorf("%s", msg)
	case "message_stop":
		return anthropicParsedEvent{done: true}, nil
	}
	return anthropicParsedEvent{}, nil
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
