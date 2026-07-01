package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"llmc/internal/config"
	"llmc/internal/provider"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: llmc <command> [arguments]

commands:
  ask          send a prompt and print the reply
  config init  create default config.toml`)
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "ask":
		err = runAsk(os.Args[2:])
	case "config":
		err = runConfig(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: llmc config <subcommand>")
	}
	switch args[0] {
	case "init":
		force := len(args) > 1 && args[1] == "--force"
		path, err := config.Init(force)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "config written to %s\n", path)
		return nil
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

// runAsk loads the config, resolves the provider's API key, and sends
// one prompt to the default provider, printing the streaming response.
func runAsk(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(`usage: llmc ask "<prompt>"`)
	}
	prompt := strings.Join(args, " ")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg.DefaultProvider == "" {
		return fmt.Errorf("no default_provider set in config.toml")
	}

	pc, ok := cfg.Providers[cfg.DefaultProvider]
	if !ok {
		return fmt.Errorf("default_provider %q has no matching [providers.%s] section", cfg.DefaultProvider, cfg.DefaultProvider)
	}

	key, err := cfg.ResolveKey(cfg.DefaultProvider)
	if err != nil {
		return err
	}

	p, err := provider.FromType(pc.Type, cfg.DefaultProvider, pc.Endpoint, key)
	if err != nil {
		return err
	}

	tokens, err := p.Stream(context.Background(), cfg.DefaultModel, []provider.Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return err
	}

	for tok := range tokens {
		if tok.Err != nil {
			return tok.Err
		}
		if tok.Text != "" {
			fmt.Print(tok.Text)
		}
		if tok.Done {
			fmt.Println()
		}
	}
	return nil
}
