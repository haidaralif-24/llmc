package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OpenAICompatible implements Provider for any OpenAI-compatible chat
// completions endpoint (OpenAI, OpenRouter, Groq, Ollama, etc.).
type OpenAICompatible struct {
	BaseURL string       // e.g. https://api.openai.com/v1/chat/completions
	APIKey  string       // BYOK: resolved from config
	Label   string       // display name: "openai", "openrouter", etc.
	Client  *http.Client // optional; defaults to http.DefaultClient
}

func (p *OpenAICompatible) Name() string { return p.Label }

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIError struct {
	Message string `json:"message"`
}

// openAIStreamChunk covers a single SSE "data:" payload from the chat
// completions streaming format: one or more choices, each carrying an
// incremental delta.
type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *openAIError `json:"error"`
}

const sseDoneSentinel = "[DONE]"

// Stream sends model + messages with stream: true and emits one Token per
// content delta as SSE chunks arrive, followed by a final Done token.
func (p *OpenAICompatible) Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("%s: no endpoint configured", p.Label)
	}

	body, err := json.Marshal(openAIRequest{
		Model:    model,
		Messages: toOpenAIMessages(messages),
		Stream:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", p.Label, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", p.Label, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	ch := make(chan Token, 4)
	go func() {
		defer close(ch)

		resp, err := client.Do(httpReq)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("%s: request failed: %w", p.Label, err)}
			return
		}
		defer resp.Body.Close()

		// A non-200 response is a plain JSON error body, not an SSE
		// stream — read it whole rather than trying to scan it as SSE.
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			var errBody struct {
				Error *openAIError `json:"error"`
			}
			msg := resp.Status
			if json.Unmarshal(raw, &errBody) == nil && errBody.Error != nil {
				msg = errBody.Error.Message
			}
			ch <- Token{Err: fmt.Errorf("%s: %s", p.Label, msg)}
			return
		}

		scanner := newSSEScanner(resp.Body)
		for {
			payload, ok := scanner.Next()
			if !ok {
				break
			}
			if payload == sseDoneSentinel {
				ch <- Token{Done: true}
				return
			}

			var chunk openAIStreamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				ch <- Token{Err: fmt.Errorf("%s: decode stream chunk: %w", p.Label, err)}
				return
			}
			if chunk.Error != nil {
				ch <- Token{Err: fmt.Errorf("%s: %s", p.Label, chunk.Error.Message)}
				return
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					ch <- Token{Text: choice.Delta.Content}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- Token{Err: fmt.Errorf("%s: reading stream: %w", p.Label, err)}
			return
		}
		// Some servers close the stream without sending [DONE]; don't
		// leave the caller hanging on the channel.
		ch <- Token{Done: true}
	}()

	return ch, nil
}

func toOpenAIMessages(messages []Message) []openAIMessage {
	msgs := make([]openAIMessage, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, openAIMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}
