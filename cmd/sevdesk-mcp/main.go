// Command sevdesk-mcp is a Model Context Protocol server for the sevDesk accounting API.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/virtualben89/sevdesk-mcp-go/internal/config"
	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
	"github.com/virtualben89/sevdesk-mcp-go/internal/tools"
)

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("sevdesk-mcp %s (%s)\n", version, commit)
			return
		case "healthcheck":
			os.Exit(healthcheck())
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// healthcheck probes the local /healthz endpoint (for Docker HEALTHCHECK in scratch images).
func healthcheck() int {
	if t := strings.ToLower(os.Getenv("MCP_TRANSPORT")); t == "" || t == "stdio" {
		return 0
	}
	addr := os.Getenv("MCP_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	// Always log to stderr: stdout belongs to the stdio transport.
	if strings.EqualFold(format, "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func toolOptions(cfg *config.Config) tools.Options {
	return tools.Options{
		DefaultToken:     cfg.APIToken,
		AllowClientToken: cfg.AllowClientToken,
		AllowWrite:       cfg.AllowWrite,
		AllowBooking:     cfg.AllowBooking,
		DryRun:           cfg.DryRun,
		DefaultLimit:     cfg.DefaultLimit,
		UploadDir:        cfg.UploadDir,
		MaxUploadBytes:   cfg.MaxUploadBytes,
		MaxDownloadBytes: cfg.MaxDownloadBytes,
		Version:          version,
	}
}

func newClient(cfg *config.Config) (*sevdesk.Client, error) {
	return sevdesk.New(sevdesk.Options{
		BaseURL:   cfg.BaseURL,
		Timeout:   cfg.Timeout,
		RateLimit: cfg.RateLimit,
		DryRun:    cfg.DryRun,
		UserAgent: "sevdesk-mcp-go/" + version,
	})
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(log)

	client, err := newClient(cfg)
	if err != nil {
		return err
	}
	if err := sevdesk.IndexError(); err != nil {
		return err
	}
	server, names := NewServer(client, toolOptions(cfg), log)
	tiers := "read"
	if cfg.AllowWrite {
		tiers += ",write"
	}
	if cfg.AllowBooking {
		tiers += ",booking"
	}
	log.Info("sevdesk-mcp starting", "version", version, "transport", cfg.Transport, "api", client.BaseURL(),
		"tiers", tiers, "dry_run", cfg.DryRun, "client_tokens", cfg.AllowClientToken, "tools", len(names))
	if cfg.AllowWrite {
		log.Warn("write tools are enabled: the sevDesk API token has FULL account access (no scopes); use a trial/test account for experiments")
	}
	if cfg.DryRun {
		log.Warn("SEVDESK_DRY_RUN=true: write requests are only previewed, never sent")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Transport == "stdio" {
		err := server.Run(ctx, &mcp.StdioTransport{})
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, io.EOF)) {
			return nil
		}
		return err
	}
	return serveHTTP(ctx, cfg, server, client, log)
}

// NewServer builds the MCP server with all tools allowed by opts.
func NewServer(client *sevdesk.Client, opts tools.Options, log *slog.Logger) (*mcp.Server, []string) {
	instr := "Access to the sevDesk accounting API (contacts, invoices, credit notes, orders/offers, parts, vouchers, bank accounts and transactions). " +
		"Call get_status first: it shows which safety tiers are enabled. Lists are slim and paginated (limit/offset; full=true for everything). " +
		"Dates are YYYY-MM-DD. Amounts and ids are numbers. " +
		"sevdesk_list_operations + sevdesk_call cover every other API endpoint. "
	if opts.AllowWrite {
		instr += "Write tools create DRAFTS only (invoices, credit notes, orders, vouchers). "
	} else {
		instr += "This server is read-only. "
	}
	if opts.AllowBooking {
		instr += "Booking tools (book_payment, document_action, send_document_email, delete_object) are enabled: they finalise, send, book or delete and cannot always be undone; only use them on explicit user request. "
	}
	if opts.DryRun {
		instr += "DRY RUN is active: write calls only return the request that would be sent. "
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "sevdesk-mcp", Title: "sevDesk", Version: version},
		&mcp.ServerOptions{Instructions: instr, Logger: log})
	names := tools.Register(server, client, opts)
	return server, names
}

// bearerVerifier checks the MCP bearer token. UserID binds Streamable HTTP
// sessions to the MCP token AND to the sevDesk token of the connection, so
// neither can be swapped inside a session. With client tokens enabled the
// validated sevDesk token travels to the tools in TokenInfo.Extra.
func bearerVerifier(tokens []string, allowClient bool) auth.TokenVerifier {
	hashes := make([][32]byte, len(tokens))
	for i, t := range tokens {
		hashes[i] = sha256.Sum256([]byte(t))
	}
	return func(_ context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
		h := sha256.Sum256([]byte(token))
		match := 0
		for _, ref := range hashes {
			match |= subtle.ConstantTimeCompare(h[:], ref[:])
		}
		if match != 1 {
			return nil, auth.ErrInvalidToken
		}
		uid := "token-" + hex.EncodeToString(h[:8])
		info := &auth.TokenInfo{Expiration: time.Now().Add(time.Hour)}
		sd := "|sd-server"
		if allowClient && r != nil {
			if ct := strings.TrimSpace(r.Header.Get(config.ClientTokenHeader)); ct != "" {
				sd = "|sd-" + sevdesk.Digest(ct)
				if sevdesk.ValidateToken(ct) == nil && len(r.Header.Values(config.ClientTokenHeader)) == 1 {
					info.Extra = map[string]any{tools.ExtraTokenKey: ct}
				}
			}
		}
		info.UserID = uid + sd
		return info, nil
	}
}

// clientTokenGuard rejects malformed or unwanted X-Sevdesk-Token headers
// after the MCP bearer check. A header that is not accepted must never be
// ignored silently: the caller would otherwise work on the server's account
// while believing to use their own.
func clientTokenGuard(allow bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vals := r.Header.Values(config.ClientTokenHeader)
		if len(vals) > 0 {
			switch {
			case !allow:
				http.Error(w, config.ClientTokenHeader+" is not accepted: the operator has not enabled SEVDESK_ALLOW_CLIENT_TOKEN. Remove the header to use the server's sevDesk account", http.StatusBadRequest)
				return
			case len(vals) != 1 || sevdesk.ValidateToken(strings.TrimSpace(vals[0])) != nil:
				http.Error(w, "invalid "+config.ClientTokenHeader+": expected exactly one 32-character hexadecimal sevDesk API token", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func serveHTTP(ctx context.Context, cfg *config.Config, server *mcp.Server, client *sevdesk.Client, log *slog.Logger) error {
	if len(cfg.AuthTokens) == 0 {
		log.Warn("HTTP transport running WITHOUT authentication (MCP_ALLOW_NO_AUTH=true); only Host headers localhost/loopback and MCP_ALLOWED_HOSTS are accepted",
			"addr", cfg.HTTPAddr, "allowed_hosts", strings.Join(cfg.AllowedHosts, ","))
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           buildHandler(cfg, server, client, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr, "path", cfg.HTTPPath, "sse", cfg.HTTPEnableSSE, "auth", len(cfg.AuthTokens) > 0)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// readyCacheTTL bounds how often /readyz probes sevDesk.
var readyCacheTTL = 5 * time.Second

// buildHandler assembles the HTTP routes: MCP endpoints (auth-protected) and health probes.
func buildHandler(cfg *config.Config, server *mcp.Server, client *sevdesk.Client, log *slog.Logger) http.Handler {
	getServer := func(*http.Request) *mcp.Server { return server }

	// Browsers: reject cross-origin requests (Sec-Fetch-Site / Origin checks).
	cop := http.NewCrossOriginProtection()
	hostGuard := func(h http.Handler) http.Handler { return h }
	// Without bearer auth the Host header is the only defence against DNS
	// rebinding, so it is always checked; with auth only when configured.
	ownHostCheck := len(cfg.AuthTokens) == 0 || len(cfg.AllowedHosts) > 0
	if ownHostCheck {
		hostGuard = func(h http.Handler) http.Handler { return requireHost(cfg.AllowedHosts, h) }
	}
	protect := func(h http.Handler) http.Handler {
		return hostGuard(cop.Handler(clientTokenGuard(cfg.AllowClientToken, h)))
	}
	if len(cfg.AuthTokens) > 0 {
		bearer := auth.RequireBearerToken(bearerVerifier(cfg.AuthTokens, cfg.AllowClientToken), &auth.RequireBearerTokenOptions{})
		protect = func(h http.Handler) http.Handler {
			return hostGuard(cop.Handler(bearer(clientTokenGuard(cfg.AllowClientToken, h))))
		}
	}

	mux := http.NewServeMux()
	streamable := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:                  cfg.HTTPStateless,
		JSONResponse:               cfg.HTTPJSONResponse,
		SessionTimeout:             cfg.HTTPSessionTimeout,
		Logger:                     log,
		DisableLocalhostProtection: ownHostCheck,
	})
	mux.Handle(cfg.HTTPPath, protect(streamable))
	if !strings.HasSuffix(cfg.HTTPPath, "/") {
		mux.Handle(cfg.HTTPPath+"/", protect(streamable))
	}
	// Legacy SSE cannot carry per-client sevDesk tokens (the SDK does not pass
	// the request's TokenInfo to tool calls over SSE), so it is never served
	// while client tokens are enabled: a client token must never be silently
	// replaced by the server token.
	if cfg.HTTPEnableSSE && !cfg.AllowClientToken {
		sse := newSSEBinder(mcp.NewSSEHandler(getServer, &mcp.SSEOptions{DisableLocalhostProtection: ownHostCheck}))
		mux.Handle("/sse", protect(sse))
		mux.Handle("/sse/", protect(sse))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	// /readyz asks sevDesk with the server-side token (if configured). It is
	// unauthenticated: results are cached briefly, and it reports only ok/fail.
	var (
		readyMu  sync.Mutex
		readyAt  time.Time
		readyErr error
	)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		code := http.StatusOK
		if cfg.APIToken != "" {
			readyMu.Lock()
			if time.Since(readyAt) > readyCacheTTL {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				_, readyErr = client.Do(ctx, cfg.APIToken, sevdesk.Request{Method: "GET", Path: "/Tools/bookkeepingSystemVersion"})
				cancel()
				readyAt = time.Now()
			}
			err := readyErr
			readyMu.Unlock()
			if err != nil {
				status, code = "sevdesk unreachable", http.StatusServiceUnavailable
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	})

	return logRequests(log, mux)
}

// sseBinder pins every legacy SSE session to the MCP token and sevDesk token
// that first used it (the SDK only binds Streamable HTTP sessions).
type sseBinder struct {
	next http.Handler
	mu   sync.Mutex
	pins map[string]pin
}

type pin struct {
	user string
	seen time.Time
}

const maxSSEPins = 4096

func newSSEBinder(next http.Handler) http.Handler {
	return &sseBinder{next: next, pins: map[string]pin{}}
}

func (b *sseBinder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if sid := r.URL.Query().Get("sessionid"); sid != "" {
			user := ""
			if ti := auth.TokenInfoFromContext(r.Context()); ti != nil {
				user = ti.UserID
			}
			b.mu.Lock()
			now := time.Now()
			if p, ok := b.pins[sid]; ok && p.user != user {
				b.mu.Unlock()
				http.Error(w, "session user mismatch", http.StatusForbidden)
				return
			}
			if len(b.pins) >= maxSSEPins {
				for k, p := range b.pins {
					if now.Sub(p.seen) > time.Hour {
						delete(b.pins, k)
					}
				}
				if len(b.pins) >= maxSSEPins {
					b.mu.Unlock()
					http.Error(w, "too many SSE sessions", http.StatusServiceUnavailable)
					return
				}
			}
			b.pins[sid] = pin{user: user, seen: now}
			b.mu.Unlock()
		}
	}
	b.next.ServeHTTP(w, r)
}

// requireHost only lets requests through whose Host header is a loopback
// name/address or listed in allowed (independent of the accepting interface).
func requireHost(allowed []string, next http.Handler) http.Handler {
	ok := map[string]bool{"localhost": true}
	for _, h := range allowed {
		ok[h] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
		ip := net.ParseIP(host)
		if !ok[host] && (ip == nil || !ip.IsLoopback()) && !strings.HasSuffix(host, ".localhost") {
			http.Error(w, fmt.Sprintf("Forbidden: Host %q not allowed (set MCP_ALLOWED_HOSTS)", r.Host), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		lvl := slog.LevelDebug
		if sw.status == http.StatusUnauthorized || sw.status == http.StatusForbidden {
			lvl = slog.LevelWarn
		}
		// Only method, path (never the query: SSE session ids) and status are logged; headers never.
		log.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"remote", r.RemoteAddr, "duration", time.Since(start).Round(time.Millisecond))
	})
}
