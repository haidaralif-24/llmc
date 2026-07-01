package provider

import "context"

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
