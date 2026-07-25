package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"icloud-reminders/internal/reminders"
	"icloud-reminders/internal/remotepolicy"
)

type principalContextKey struct{}

func HTTPHandler(service reminders.Service, store *remotepolicy.Store, version string, allowedHosts ...string) http.Handler {
	hosts := map[string]bool{"127.0.0.1": true, "localhost": true, "[::1]": true}
	for _, host := range allowedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			hosts[host] = true
		}
	}
	streamable := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		policy, _ := req.Context().Value(principalContextKey{}).(remotepolicy.Policy)
		return New(service, Access{Remote: true, Policy: policy}, version)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/mcp" {
			writeHTTPError(w, http.StatusForbidden, "route_not_allowed")
			return
		}
		host := strings.ToLower(req.Host)
		if parsedHost, _, err := net.SplitHostPort(req.Host); err == nil {
			host = strings.ToLower(parsedHost)
		}
		if !hosts[host] {
			log.Printf("reminders MCP rejected Host header %q", req.Host)
			writeHTTPError(w, http.StatusForbidden, "host_not_allowed")
			return
		}
		token := bearerToken(req.Header.Get("Authorization"))
		policy, ok := store.Authenticate(token)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeHTTPError(w, http.StatusUnauthorized, "auth_error")
			return
		}
		ctx := context.WithValue(req.Context(), principalContextKey{}, policy)
		clean := req.Clone(ctx)
		clean.Header = req.Header.Clone()
		clean.Header.Del("Authorization")
		streamable.ServeHTTP(w, clean)
	})
}

func ServeHTTP(ctx context.Context, service reminders.Service, store *remotepolicy.Store, listen string, version string, allowedHosts ...string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("HTTP MCP must bind to loopback")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: HTTPHandler(service, store, version, allowedHosts...), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func bearerToken(value string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

func writeHTTPError(w http.ResponseWriter, status int, marker string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": marker})
}
