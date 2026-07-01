package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProviderConfig is one [providers.<name>] table from config.toml.
type ProviderConfig struct {
	Type      string // "anthropic" | "openai_compatible" — selects the adapter
	Endpoint  string
	APIKey    string // optional literal key, checked before APIKeyEnv
	APIKeyEnv string // name of the env var holding the key, used if APIKey is unset
}

// DefaultConfig is the template written by `llmc config init`, matching
// the sample from DESIGN.md §5.
const DefaultConfig = `default_provider = "anthropic"
default_model    = "claude-sonnet-4-6"

[providers.anthropic]
type        = "anthropic"
endpoint    = "https://api.anthropic.com/v1/messages"
api_key_env = "ANTHROPIC_API_KEY"

[providers.openai]
type        = "openai_compatible"
endpoint    = "https://api.openai.com/v1/chat/completions"
api_key_env = "OPENAI_API_KEY"

[providers.openrouter]
type        = "openai_compatible"
endpoint    = "https://openrouter.ai/api/v1/chat/completions"
api_key_env = "OPENROUTER_API_KEY"

[providers.ollama]
type        = "openai_compatible"
endpoint    = "http://localhost:11434/v1/chat/completions"
# ollama runs locally and typically needs no key
`

// Init creates ~/.config/llmc/config.toml (or $XDG_CONFIG_HOME/llmc/config.toml)
// with the default provider entries. It is safe to call multiple times — it
// will not overwrite an existing file unless force is true.
func Init(force bool) (string, error) {
	path, err := PathFn()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(path); err == nil && !force {
		return path, fmt.Errorf("config: %s already exists (use --force to overwrite)", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("config: creating directory %s: %w", dir, err)
	}

	if err := os.WriteFile(path, []byte(DefaultConfig), 0o600); err != nil {
		return "", fmt.Errorf("config: writing %s: %w", path, err)
	}

	return path, nil
}

// Config is the parsed contents of ~/.config/llmc/config.toml.
type Config struct {
	DefaultProvider string
	DefaultModel    string
	Providers       map[string]ProviderConfig
}

// PathFn is a variable so tests can override it without touching $HOME.
var PathFn = Path

// Path returns the on-disk location of the config file, honoring
// XDG_CONFIG_HOME when set.
func Path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "llmc", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "llmc", "config.toml"), nil
}

// Load reads and parses the config file at the default PathFn().
func Load() (*Config, error) {
	path, err := PathFn()
	if err != nil {
		return nil, err
	}
	return LoadFile(path)
}

// LoadFile parses a config file at an explicit path. Split out from Load so
// tests (and things like `llmc --config <path>`, later) don't depend on
// $HOME.
//
// This is a hand-rolled parser for the small, flat subset of TOML llmc
// actually uses: top-level "key = value" pairs, plus one level of
// "[providers.<name>]" tables also containing "key = value" pairs. It is
// not a general TOML implementation — if the config format grows beyond
// this shape (arrays, nested tables, multi-line strings, etc.), swap in a
// real TOML library rather than extending this by hand.
func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()

	cfg := &Config{Providers: map[string]ProviderConfig{}}
	var section string // "" for top-level, else e.g. "providers.anthropic"

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("config:%d: malformed section header %q", lineNum, line)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		key, val, ok := splitKV(line)
		if !ok {
			return nil, fmt.Errorf("config:%d: expected \"key = value\", got %q", lineNum, line)
		}

		switch {
		case section == "" && key == "default_provider":
			cfg.DefaultProvider = val
		case section == "" && key == "default_model":
			cfg.DefaultModel = val
		case strings.HasPrefix(section, "providers."):
			name := strings.TrimPrefix(section, "providers.")
			pc := cfg.Providers[name] // zero value if new
			switch key {
			case "type":
				pc.Type = val
			case "endpoint":
				pc.Endpoint = val
			case "api_key":
				pc.APIKey = val
			case "api_key_env":
				pc.APIKeyEnv = val
			}
			cfg.Providers[name] = pc
			// Unrecognized keys within a provider table, or unrecognized
			// top-level keys / sections, are ignored rather than treated as
			// errors, so config files can carry fields future versions
			// understand without breaking older builds.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	return cfg, nil
}

// splitKV splits a "key = value" line and strips surrounding double quotes
// from the value if present.
func splitKV(line string) (key, val string, ok bool) {
	i := strings.Index(line, "=")
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	if key == "" {
		return "", "", false
	}
	val = strings.TrimSpace(line[i+1:])
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		val = val[1 : len(val)-1]
	}
	return key, val, true
}

// ResolveKey returns the API key for a provider: a literal api_key in
// config.toml takes priority if set, otherwise it falls back to reading the
// env var named in api_key_env. If neither is set (or the env var is
// unset), it returns "" with no error — some providers (local servers)
// legitimately don't need a key, so whether a blank key is fatal is left to
// the caller.
//
// Note: a literal api_key means the key lives in config.toml in plain
// text, which DESIGN.md deliberately avoided (see its BYOK rationale) —
// this is supported as an opt-in convenience, not the recommended default.
func (c *Config) ResolveKey(providerName string) (string, error) {
	pc, ok := c.Providers[providerName]
	if !ok {
		return "", fmt.Errorf("config: no [providers.%s] entry", providerName)
	}
	if pc.APIKey != "" {
		return pc.APIKey, nil
	}
	if pc.APIKeyEnv == "" {
		return "", nil
	}
	return os.Getenv(pc.APIKeyEnv), nil
}
