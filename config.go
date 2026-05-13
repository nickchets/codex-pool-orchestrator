package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ConfigFile represents the config.toml structure.
type ConfigFile struct {
	ListenAddr                           string  `toml:"listen_addr"`
	PoolDir                              string  `toml:"pool_dir"`
	DBPath                               string  `toml:"db_path"`
	MaxAttempts                          int     `toml:"max_attempts"`
	ForceCodexRequiredPlan               string  `toml:"force_codex_required_plan"`
	GitLabCodexDiscoveryModels           string  `toml:"gitlab_codex_discovery_models"`
	DisableRefresh                       bool    `toml:"disable_refresh"`
	RefreshProxyURL                      string  `toml:"refresh_proxy_url"` // HTTP proxy for refresh operations
	Debug                                bool    `toml:"debug"`
	PublicURL                            string  `toml:"public_url"`
	FriendCode                           string  `toml:"friend_code"`
	FriendName                           string  `toml:"friend_name"`
	FriendTagline                        string  `toml:"friend_tagline"`
	AdminToken                           string  `toml:"admin_token"`
	TierThreshold                        float64 `toml:"tier_threshold"`                           // Secondary usage % threshold for tier preference (default 0.15)
	LowHeadroomReservePct                float64 `toml:"low_headroom_reserve_pct"`                 // Legacy unified minimum quota headroom reserved from new admissions (default 0.05)
	PrimaryLowHeadroomReservePct         float64 `toml:"primary_low_headroom_reserve_pct"`         // Minimum primary-window quota headroom reserved from new admissions (default 0.05)
	SecondaryLowHeadroomReservePct       float64 `toml:"secondary_low_headroom_reserve_pct"`       // Minimum secondary-window quota headroom reserved from new admissions (default 0.02)
	PoolAPIDefaultMaxOutputTokens        int     `toml:"pool_api_default_max_output_tokens"`       // Default output-token reservation for virtual pool API key TPM policy
	StreamContinuation                   string  `toml:"stream_continuation"`                      // Experimental plain-text SSE continuation bridge (default off)
	StreamContinuationMaxAttempts        int     `toml:"stream_continuation_max_attempts"`         // Max continuation segments after a mid-stream failure (default 1)
	CodexChatCompletionsResponsesAdapter bool    `toml:"codex_chat_completions_responses_adapter"` // Feature-flagged OpenAI Chat -> Codex Responses adapter (default off)

	PoolUsers PoolUsersConfig `toml:"pool_users"`
}

// getFriendName returns the configured friend name for the landing page.
func getFriendName() string {
	if v := os.Getenv("FRIEND_NAME"); v != "" {
		return v
	}
	if globalConfigFile != nil && globalConfigFile.FriendName != "" {
		return globalConfigFile.FriendName
	}
	return "PP" // default
}

// getFriendTagline returns the configured tagline for the landing page.
func getFriendTagline() string {
	if v := os.Getenv("FRIEND_TAGLINE"); v != "" {
		return v
	}
	if globalConfigFile != nil && globalConfigFile.FriendTagline != "" {
		return globalConfigFile.FriendTagline
	}
	return "For the few who know, the pool awaits. Unlimited resources. Zero friction."
}

// PoolUsersConfig is the [pool_users] section.
type PoolUsersConfig struct {
	JWTSecret   string `toml:"jwt_secret"`
	StoragePath string `toml:"storage_path"`
}

// loadConfigFile loads config.toml if it exists.
// Returns nil if the file doesn't exist.
func loadConfigFile(path string) (*ConfigFile, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	var cfg ConfigFile
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// getConfigString returns the config value with priority: env var > config file > default.
func getConfigString(envKey string, configValue string, defaultValue string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if configValue != "" {
		return configValue
	}
	return defaultValue
}

// getConfigInt returns the config value with priority: env var > config file > default.
func getConfigInt(envKey string, configValue int, defaultValue int) int {
	if v := os.Getenv(envKey); v != "" {
		if n, err := parseInt64(v); err == nil && n > 0 {
			return int(n)
		}
	}
	if configValue > 0 {
		return configValue
	}
	return defaultValue
}

// getConfigFloat64 returns the config value with priority: env var > config file > default.
func getConfigFloat64(envKey string, configValue float64, defaultValue float64) float64 {
	if v := os.Getenv(envKey); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	if configValue > 0 {
		return configValue
	}
	return defaultValue
}

// getConfigBool returns the config value with priority: env var > config file > default.
func getConfigBool(envKey string, configValue bool, defaultValue bool) bool {
	if v := os.Getenv(envKey); v != "" {
		return v == "1" || v == "true"
	}
	if configValue {
		return true
	}
	return defaultValue
}

func parseCSVEnvList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}
