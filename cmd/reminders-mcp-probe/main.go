// reminders-mcp-probe is a verifier/client for stdio and HTTP MCP endpoints.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(copy)
}

func main() {
	transport := flag.String("transport", "http", "http or stdio")
	endpoint := flag.String("endpoint", "http://127.0.0.1:9181/mcp", "HTTP endpoint")
	resolveIP := flag.String("resolve-ip", "", "Connect to this IP while preserving endpoint hostname/TLS SNI")
	token := flag.String("token", os.Getenv("REMINDERS_MCP_TOKEN"), "Bearer token (or REMINDERS_MCP_TOKEN)")
	binary := flag.String("binary", "reminders", "reminders binary for stdio")
	tool := flag.String("tool", "lists", "tool to call")
	args := flag.String("args", `{}`, "JSON object arguments")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	flag.Parse()

	var arguments map[string]any
	if err := json.Unmarshal([]byte(*args), &arguments); err != nil {
		fatal("invalid --args JSON: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "reminders-verifier", Version: "1"}, nil)
	var mcpTransport mcp.Transport
	switch *transport {
	case "stdio":
		mcpTransport = &mcp.CommandTransport{Command: exec.Command(*binary, "mcp", "--transport", "stdio")}
	case "http":
		if *token == "" {
			fatal("--token or REMINDERS_MCP_TOKEN is required for HTTP")
		}
		base := http.DefaultTransport
		if *resolveIP != "" {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(*resolveIP, port))
			}
			base = transport
		}
		httpClient := &http.Client{Transport: bearerTransport{token: *token, base: base}}
		mcpTransport = &mcp.StreamableClientTransport{Endpoint: *endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}
	default:
		fatal("unsupported transport %q", *transport)
	}
	session, err := client.Connect(ctx, mcpTransport, nil)
	if err != nil {
		fatal("connect: %v", err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: *tool, Arguments: arguments})
	if err != nil {
		fatal("call: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		fatal("marshal: %v", err)
	}
	fmt.Println(string(data))
	if result.IsError {
		os.Exit(3)
	}
}

func fatal(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(2) }
