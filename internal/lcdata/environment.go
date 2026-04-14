package lcdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// EnvironmentConfigs is the top-level structure of lcdataenv.json
type EnvironmentConfigs struct {
	Environments map[string]EnvironmentConfig `json:"environments"`
}

// EnvironmentConfig holds credentials and connection strings for one environment
type EnvironmentConfig struct {
	AnthropicKey   string            `json:"anthropicKey"`
	OllamaEndpoint string            `json:"ollamaEndpoint"`
	OpenAIKey      string            `json:"openaiKey"`
	ElevenLabsKey  string            `json:"elevenlabsKey"`
	DeepgramKey    string            `json:"deepgramKey"`
	BraveKey       string            `json:"braveKey"`
	SearxngEndpoint string           `json:"searxngEndpoint"`
	DBConnections  map[string]string `json:"dbConnections"`
}

// LoadEnvironmentConfigs looks for lcdataenv.json in the home dir, then ./nodes/env.json
func LoadEnvironmentConfigs() (EnvironmentConfigs, error) {
	usr, err := user.Current()
	if err != nil {
		return EnvironmentConfigs{}, fmt.Errorf("failed to get home directory: %w", err)
	}

	paths := []string{
		filepath.Join(usr.HomeDir, "lcdataenv.json"),
		filepath.Join("nodes", "env.json"),
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return EnvironmentConfigs{}, fmt.Errorf("failed to read env config %s: %w", p, err)
		}

		var cfg EnvironmentConfigs
		if err := json.Unmarshal(data, &cfg); err != nil {
			return EnvironmentConfigs{}, fmt.Errorf("failed to parse env config %s: %w", p, err)
		}
		return cfg, nil
	}

	// Return empty config — env vars can still be used by executor implementations
	return EnvironmentConfigs{
		Environments: map[string]EnvironmentConfig{
			"default": {},
		},
	}, nil
}

func (ec EnvironmentConfigs) GetEnvironment(name string) (EnvironmentConfig, error) {
	env, ok := ec.Environments[name]
	if !ok {
		return EnvironmentConfig{}, errors.New("environment not found: " + name)
	}
	// Fall back to OS env vars if fields are empty
	if env.AnthropicKey == "" {
		env.AnthropicKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if env.OllamaEndpoint == "" {
		env.OllamaEndpoint = os.Getenv("OLLAMA_ENDPOINT")
	}
	if env.OllamaEndpoint == "" {
		env.OllamaEndpoint = "http://localhost:11434"
	}
	if env.OpenAIKey == "" {
		env.OpenAIKey = os.Getenv("OPENAI_API_KEY")
	}
	if env.ElevenLabsKey == "" {
		env.ElevenLabsKey = os.Getenv("ELEVENLABS_API_KEY")
	}
	if env.DeepgramKey == "" {
		env.DeepgramKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if env.BraveKey == "" {
		env.BraveKey = os.Getenv("BRAVE_API_KEY")
	}
	if env.SearxngEndpoint == "" {
		env.SearxngEndpoint = os.Getenv("SEARXNG_ENDPOINT")
	}
	return env, nil
}
