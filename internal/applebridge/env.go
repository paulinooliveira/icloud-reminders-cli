package applebridge

import (
	"os"
	"strconv"
	"strings"
)

// LoadValidatorConfigFromEnv returns the optional validator bridge config.
// An empty or incomplete env configuration means validation is disabled.
func LoadValidatorConfigFromEnv() *Config {
	validatorType := strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_TYPE"))
	if validatorType == "" || validatorType != "apple-ssh" {
		return nil
	}

	cfg := &Config{
		Host:              strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_HOST")),
		User:              strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_USER")),
		IdentityPath:      strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_IDENTITY_PATH")),
		SebastianListName: strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_SEBASTIAN_LIST_NAME")),
		SebastianListID:   strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_SEBASTIAN_LIST_ID")),
	}
	if timeoutRaw := strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_TIMEOUT_SECONDS")); timeoutRaw != "" {
		if timeout, err := strconv.Atoi(timeoutRaw); err == nil {
			cfg.TimeoutSeconds = timeout
		}
	}
	if cfg.Host == "" || cfg.User == "" || cfg.IdentityPath == "" {
		return nil
	}
	return cfg
}
