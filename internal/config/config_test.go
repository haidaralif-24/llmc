package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `
default_provider = "anthropic"
default_model    = "claude-sonnet-4-6"

[providers.anthropic]
type        = "anthropic"
endpoint    = "https://api.anthropic.com/v1/messages"
api_key_env = "ANTHROPIC_API_KEY"

[providers.openrouter]
type        = "openai_compatible"
endpoint    = "https://openrouter.ai/api/v1/chat/completions"
api_key_env = "OPENROUTER_API_KEY"
`

func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

func TestLoadFile_ParsesTopLevelAndProviders(t *testing.T) {
	path := writeTemp(t, sample)

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if cfg.DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q, want %q", cfg.DefaultProvider, "anthropic")
	}
	if cfg.DefaultModel != "claude-sonnet-4-6" {
		t.Errorf("DefaultModel = %q, want %q", cfg.DefaultModel, "claude-sonnet-4-6")
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("len(Providers) = %d, want 2", len(cfg.Providers))
	}

	anthropic, ok := cfg.Providers["anthropic"]
	if !ok {
		t.Fatal(`missing "anthropic" provider`)
	}
	if anthropic.Type != "anthropic" {
		t.Errorf("anthropic.Type = %q, want %q", anthropic.Type, "anthropic")
	}
	if anthropic.Endpoint != "https://api.anthropic.com/v1/messages" {
		t.Errorf("anthropic.Endpoint = %q", anthropic.Endpoint)
	}
	if anthropic.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("anthropic.APIKeyEnv = %q", anthropic.APIKeyEnv)
	}

	openrouter, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatal(`missing "openrouter" provider`)
	}
	if openrouter.Type != "openai_compatible" {
		t.Errorf("openrouter.Type = %q, want %q", openrouter.Type, "openai_compatible")
	}
}

func TestLoadFile_IgnoresCommentsAndBlankLines(t *testing.T) {
	path := writeTemp(t, `
# a top-level comment
default_provider = "anthropic"

# a comment inside a section
[providers.anthropic]
type = "anthropic"        # trailing content after value is NOT stripped
`)
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q", cfg.DefaultProvider)
	}
}

func TestLoadFile_MissingFile(t *testing.T) {
	_, err := LoadFile("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestLoadFile_MalformedSectionHeader(t *testing.T) {
	path := writeTemp(t, "[providers.anthropic\ntype = \"anthropic\"\n")
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected an error for an unterminated section header, got nil")
	}
}

func TestLoadFile_ParsesLiteralAPIKey(t *testing.T) {
	path := writeTemp(t, `
[providers.anthropic]
type    = "anthropic"
api_key = "sk-ant-literal-123"
`)
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := cfg.Providers["anthropic"].APIKey; got != "sk-ant-literal-123" {
		t.Errorf("APIKey = %q, want %q", got, "sk-ant-literal-123")
	}
}

func TestResolveKey_ReadsEnvVar(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKeyEnv: "LLMC_TEST_KEY"},
		},
	}

	t.Setenv("LLMC_TEST_KEY", "sk-test-123")

	key, err := cfg.ResolveKey("anthropic")
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if key != "sk-test-123" {
		t.Errorf("key = %q, want %q", key, "sk-test-123")
	}
}

func TestResolveKey_LiteralKeyTakesPriorityOverEnv(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"anthropic": {
				APIKey:    "sk-literal-from-toml",
				APIKeyEnv: "LLMC_TEST_KEY_PRIORITY",
			},
		},
	}
	t.Setenv("LLMC_TEST_KEY_PRIORITY", "sk-from-env")

	key, err := cfg.ResolveKey("anthropic")
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if key != "sk-literal-from-toml" {
		t.Errorf("key = %q, want literal api_key to win over env var", key)
	}
}

func TestResolveKey_UnknownProvider(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderConfig{}}
	if _, err := cfg.ResolveKey("nope"); err == nil {
		t.Fatal("expected an error for an unknown provider, got nil")
	}
}

func TestInit_WritesDefaultConfigAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	orig := PathFn
	PathFn = func() (string, error) { return path, nil }
	defer func() { PathFn = orig }()

	gotPath, err := Init(false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if gotPath != path {
		t.Errorf("Init path = %q, want %q", gotPath, path)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile after Init: %v", err)
	}
	if cfg.DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q, want %q", cfg.DefaultProvider, "anthropic")
	}

	// Second call without force should refuse to clobber the existing file.
	if _, err := Init(false); err == nil {
		t.Fatal("expected Init(false) to refuse an existing file, got nil error")
	}

	// force=true should succeed.
	if _, err := Init(true); err != nil {
		t.Fatalf("Init(true) on existing file: %v", err)
	}
}
