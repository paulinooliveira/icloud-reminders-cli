package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"icloud-reminders/internal/mcpserver"
	"icloud-reminders/internal/reminders"
	"icloud-reminders/internal/remotepolicy"
)

var mcpTransport string
var mcpListen string
var mcpKeysFile string
var mcpAllowedHosts []string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve iCloud Reminders over MCP (stdio or loopback HTTP)",
	RunE: func(cmd *cobra.Command, args []string) error {
		service, err := reminders.NewService()
		if err != nil {
			return err
		}
		defer service.Close()
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		if os.Getenv("REMINDERS_MCP_REQUIRE_SSH_PARENT") == "1" {
			var cancel context.CancelFunc
			ctx, cancel = parentBoundContext(ctx, os.Getppid, 250*time.Millisecond)
			defer cancel()
		}
		switch mcpTransport {
		case "stdio":
			return mcpserver.New(service, mcpserver.LocalAccess(), version).Run(ctx, &mcp.StdioTransport{})
		case "http":
			if mcpKeysFile == "" {
				return fmt.Errorf("--keys-file is required for HTTP transport")
			}
			store, err := remotepolicy.NewStore(mcpKeysFile)
			if err != nil {
				return err
			}
			return mcpserver.ServeHTTP(ctx, service, store, mcpListen, version, mcpAllowedHosts...)
		default:
			return fmt.Errorf("unsupported MCP transport %q", mcpTransport)
		}
	},
}

func parentBoundContext(parent context.Context, parentPID func() int, interval time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if parentPID() <= 1 {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func init() {
	mcpCmd.Flags().StringVar(&mcpTransport, "transport", "stdio", "MCP transport: stdio or http")
	mcpCmd.Flags().StringVar(&mcpListen, "listen", "127.0.0.1:9181", "Loopback listen address for HTTP transport")
	mcpCmd.Flags().StringVar(&mcpKeysFile, "keys-file", "", "0600 JSON bearer-key file (HTTP only)")
	mcpCmd.Flags().StringSliceVar(&mcpAllowedHosts, "allowed-host", nil, "Additional public Host header accepted by HTTP MCP")
	RootCmd.AddCommand(mcpCmd)
}
