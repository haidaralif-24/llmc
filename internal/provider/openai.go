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
	Model    string           `json:"model"`
	Messages []openAIMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

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

	ch := make(chan Token, 8)
	go func() {
		defer close(ch)

		resp, err := client.Do(httpReq)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("%s: request failed: %w", p.Label, err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ch <- Token{Err: fmt.Errorf("%s: %s", p.Label, resp.Status)}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- Token{Done: true}
				return
			}

			var chunk openAIChunk
			if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr != nil {
				continue
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
			ch <- Token{Err: fmt.Errorf("%s: read stream: %w", p.Label, err)}
		} else {
			ch <- Token{Done: true}
		}
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
