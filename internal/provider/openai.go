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
	Model    string           `json:"model"`
	Messages []openAIMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *OpenAICompatible) Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("%s: no endpoint configured", p.Label)
	}

	body, err := json.Marshal(openAIRequest{
		Model:    model,
		Messages: toOpenAIMessages(messages),
		Stream:   false,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", p.Label, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", p.Label, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
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
			ch <- Token{Err: fmt.Errorf("%s: request failed: %w", p.Label, err)}
			return
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			ch <- Token{Err: fmt.Errorf("%s: read response: %w", p.Label, err)}
			return
		}

		var parsed openAIResponse
		if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
			ch <- Token{Err: fmt.Errorf("%s: decode response (status %s): %w", p.Label, resp.Status, jsonErr)}
			return
		}

		if resp.StatusCode != http.StatusOK {
			msg := resp.Status
			if parsed.Error != nil {
				msg = parsed.Error.Message
			}
			ch <- Token{Err: fmt.Errorf("%s: %s", p.Label, msg)}
			return
		}

		var text string
		for _, choice := range parsed.Choices {
			text += choice.Message.Content
		}
		ch <- Token{Text: text}
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
