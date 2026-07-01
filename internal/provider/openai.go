package provider

import "context"

type OpenAI struct{}

func (p *OpenAI) Name() string { return "openai" }

func (p *OpenAI) Stream(ctx context.Context, model string, messages []Message) (<-chan Token, error) {
	return nil, nil
}
