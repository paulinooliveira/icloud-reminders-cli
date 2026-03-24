package cmd

import (
	"os"
	"strconv"
	"strings"

	"icloud-reminders/internal/applebridge"
)

func loadOptionalValidatorBridge() (*applebridge.Bridge, *applebridge.Config, error) {
	validatorType := strings.TrimSpace(os.Getenv("ICLOUD_REMINDERS_VALIDATOR_TYPE"))
	if validatorType == "" {
		return nil, nil, nil
	}
	if validatorType != "apple-ssh" {
		return nil, nil, nil
	}
	cfg := &applebridge.Config{
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
		return nil, nil, nil
	}
	return applebridge.New(cfg), cfg, nil
}
