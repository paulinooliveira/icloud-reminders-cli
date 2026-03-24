package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOptionalValidatorBridgeIgnoresLegacyConfigWithoutEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_TYPE", "")
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_HOST", "")
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_USER", "")
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_IDENTITY_PATH", "")

	configDir := filepath.Join(home, ".config", "icloud-reminders")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "apple-bridge.json")
	configJSON := `{"host":"example-mac","user":"paulino","identity_path":"/tmp/id_ed25519","sebastian_list_name":"Sebastian","sebastian_list_id":"List/abc"}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	bridge, cfg, err := loadOptionalValidatorBridge()
	if err != nil {
		t.Fatalf("loadOptionalValidatorBridge: %v", err)
	}
	if bridge != nil || cfg != nil {
		t.Fatalf("expected validator bridge to stay disabled without env, got bridge=%v cfg=%v", bridge, cfg)
	}
}
