package config

type ProviderConfig struct {
	Endpoint   string
	APIKeyEnv  string
}

type Config struct {
	DefaultProvider string
	DefaultModel    string
	Providers       map[string]ProviderConfig
}

func Load() (*Config, error) { return nil, nil }
