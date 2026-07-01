package provider

import "context"

type Anthropic struct{}

func (p *Anthropic) Name() string { return "anthropic" }

func (p *Anthropic) Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error) {
	return nil, nil
}
