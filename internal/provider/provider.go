package provider

import (
	"context"
	"fmt"
)

type Message struct {
	Role    string
	Content string
}

type Token struct {
	Text string
	Done bool
	Err  error
}

type Provider interface {
	Name() string
	Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error)
}

// FromType constructs a Provider from the config type string.
// Supported types: "anthropic", "openai_compatible".
func FromType(typ, name, endpoint, apiKey string) (Provider, error) {
	switch typ {
	case "anthropic":
		return &Anthropic{Endpoint: endpoint, APIKey: apiKey}, nil
	case "openai_compatible":
		return &OpenAICompatible{BaseURL: endpoint, APIKey: apiKey, Label: name}, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", typ)
	}
}
