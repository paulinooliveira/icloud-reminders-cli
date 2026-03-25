package cmd

import "icloud-reminders/internal/applebridge"

func loadOptionalValidatorBridge() (*applebridge.Bridge, *applebridge.Config, error) {
	cfg := applebridge.LoadValidatorConfigFromEnv()
	if cfg == nil {
		return nil, nil, nil
	}
	return applebridge.New(cfg), cfg, nil
}
