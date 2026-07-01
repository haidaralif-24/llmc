package tui

import (
	"context"
	"llmc/internal/config"
	"llmc/internal/provider"
	"llmc/internal/session"
)

type model struct {
	streaming       bool
	cancelFunc      context.CancelFunc
	pendingModel    *string
	pendingProvider *string
	messageQueue    []string
}

func Run(cfg *config.Config, store *session.Store, provs map[string]provider.Provider) error {
	return nil
}
