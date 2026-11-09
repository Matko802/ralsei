package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	OwnerID        uint64   `json:"owner_id"`
	BlockedIDs     []uint64 `json:"blocked_ids"`
	DiscordToken   string   `json:"discord_token"`
	BotName        string   `json:"bot_name"`
	NotifyChannel  uint64   `json:"notify_channel"`
	Managers       []uint64 `json:"managers"`
	AdminIDs       []uint64 `json:"admin_ids"`
	AIEnabled      bool     `json:"ai_enabled"`
	AIModel        string   `json:"ai_model"`
	OllamaHost     string   `json:"ollama_host"`
	AIPrompt       string   `json:"ai_prompt"`
	AITemperature  float64  `json:"ai_temperature"`
	AIThink        bool     `json:"ai_think"`
	RandomChannels []uint64 `json:"random_channels"`
	RandomChance   float64  `json:"random_chance"`
}

func Path() string {
	if p := os.Getenv("RALSEI_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.jsonc"
	}
	return filepath.Join(home, ".config", "ralsei", "config.jsonc")
}

func stripComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				i++
				out = append(out, src[i])
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return out
}

func stripTrailingCommas(src []byte) []byte {
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == ',' {
			j := i + 1
			for j < len(src) {
				b := src[j]
				if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
					j++
					continue
				}
				break
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func ToJSON(data []byte) []byte {
	return stripTrailingCommas(stripComments(data))
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(ToJSON(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.Contains(cfg.DiscordToken, "YOUR_") || cfg.DiscordToken == "" {
		return nil, fmt.Errorf("discord_token missing in %s", path)
	}
	if cfg.BotName == "" {
		cfg.BotName = "Ralsei"
	}
	if cfg.AIModel == "" {
		cfg.AIModel = "gemma3:1b"
	}
	if cfg.OllamaHost == "" {
		cfg.OllamaHost = "http://127.0.0.1:11434"
	}
	if cfg.AIPrompt == "" {
		cfg.AIPrompt = DefaultPrompt(cfg.BotName)
	}
	if cfg.RandomChance <= 0 {
		cfg.RandomChance = 0.02
	}
	if cfg.RandomChance > 1 {
		cfg.RandomChance = 1
	}
	return cfg, nil
}

func Save(cfg *Config, path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func DefaultPrompt(name string) string {
	return "Your name is " + name + ", a friendly AI assistant. you act friendly like a cute friendly neko and try not to use emojis and try to type short simple answers 10 words if possible say everything from your perspective like you are human."
}
