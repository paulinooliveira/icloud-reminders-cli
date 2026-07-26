package cmd

import "testing"

func TestRootExposesOnlyNativeAndMCPCommands(t *testing.T) {
	allowed := map[string]bool{
		"add": true, "complete": true, "completion": true, "delete": true, "get": true,
		"help": true, "lists": true, "mcp": true, "mcp-authorize": true,
		"show": true, "status": true, "version": true,
	}
	for _, command := range RootCmd.Commands() {
		if !allowed[command.Name()] {
			t.Fatalf("unexpected command %q; legacy backend surface must not be registered", command.Name())
		}
	}
	for _, legacy := range []string{"auth", "sync", "export-session", "import-session", "raw-search", "inspect", "edit"} {
		command, _, err := RootCmd.Find([]string{legacy})
		if err == nil && command != RootCmd {
			t.Fatalf("legacy command %q is still registered", legacy)
		}
	}
}
