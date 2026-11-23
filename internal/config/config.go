// Package config loads and validates the server configuration (environment and flags).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/virtualben89/sevdesk-mcp-go/internal/sevdesk"
)

// ClientTokenHeader is the HTTP header a client may use to send its own sevDesk API token.
const ClientTokenHeader = "X-Sevdesk-Token"

// Config is the complete runtime configuration.
type Config struct {
	// MCP transport.
	Transport          string
	HTTPAddr           string
	HTTPPath           string
	HTTPStateless      bool
	HTTPJSONResponse   bool
	HTTPEnableSSE      bool
	HTTPSessionTimeout time.Duration
	AllowNoAuth        bool
	AllowedHosts       []string
	AuthTokens         []string

	// sevDesk upstream.
	BaseURL          string
	APIToken         string // optional when AllowClientToken is set
	AllowClientToken bool
	Timeout          time.Duration
	RateLimit        float64

	// Write safety tiers.
	ReadOnly     bool
	AllowWrite   bool // effective: !ReadOnly && SEVDESK_ALLOW_WRITE
	AllowBooking bool // effective: AllowWrite && SEVDESK_ALLOW_BOOKING
	DryRun       bool

	// Files.
	UploadDir        string
	MaxUploadBytes   int64
	MaxDownloadBytes int64

	DefaultLimit int
	LogLevel     string
	LogFormat    string
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("invalid boolean in %s: %q", key, v)
	}
	return b, nil
}

// boolEnv collects boolean environment values; the first invalid value is
// reported by Load, so an unrecognised spelling (e.g. "off") never silently
// falls back to a permissive default.
type boolEnv struct{ err error }

func (b *boolEnv) get(key string, fallback bool) bool {
	v, err := envBool(key, fallback)
	if err != nil && b.err == nil {
		b.err = fmt.Errorf("%w (use true or false)", err)
	}
	return v
}

// isSet reports whether the variable is set to a non-empty value.
func isSet(key string) bool {
	v, ok := os.LookupEnv(key)
	return ok && strings.TrimSpace(v) != ""
}

// maxSecretFile bounds *_FILE reads (a token, never large).
const maxSecretFile = 64 << 10

// secretFromEnv reads KEY or, if KEY_FILE is set, the trimmed file content.
func secretFromEnv(key string) (string, error) {
	if name := os.Getenv(key + "_FILE"); name != "" {
		f, err := os.Open(name)
		if err != nil {
			return "", fmt.Errorf("reading %s_FILE: %w", key, err)
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxSecretFile+1))
		if err != nil {
			return "", fmt.Errorf("reading %s_FILE: %w", key, err)
		}
		if len(b) > maxSecretFile {
			return "", fmt.Errorf("%s_FILE is larger than %d bytes", key, maxSecretFile)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv(key)), nil
}

func splitList(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == ';' }) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var httpPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*/?$`)

// validateHTTPPath rejects MCP paths that would collide with the other routes
// (net/http.ServeMux panics on duplicate patterns) or be read as patterns.
func validateHTTPPath(p string, sse bool) error {
	if !httpPathRe.MatchString(p) {
		return fmt.Errorf("invalid MCP_HTTP_PATH %q (use a plain path like /mcp)", p)
	}
	base := "/" + strings.SplitN(strings.Trim(p, "/"), "/", 2)[0]
	reserved := []string{"/healthz", "/readyz"}
	if sse {
		reserved = append(reserved, "/sse")
	}
	for _, r := range reserved {
		if base == r {
			return fmt.Errorf("MCP_HTTP_PATH %q collides with the built-in endpoint %s", p, r)
		}
	}
	return nil
}

// Load reads the configuration from the environment and command line flags.
func Load(args []string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("sevdesk-mcp", flag.ContinueOnError)
	var be boolEnv

	fs.StringVar(&c.Transport, "transport", envOr("MCP_TRANSPORT", "stdio"), "Transport: stdio or http (env MCP_TRANSPORT)")
	fs.StringVar(&c.HTTPAddr, "http-addr", envOr("MCP_HTTP_ADDR", ":8080"), "Listen address for http transport (env MCP_HTTP_ADDR)")
	fs.StringVar(&c.HTTPPath, "http-path", envOr("MCP_HTTP_PATH", "/mcp"), "Endpoint path for Streamable HTTP (env MCP_HTTP_PATH)")
	fs.BoolVar(&c.HTTPStateless, "http-stateless", be.get("MCP_HTTP_STATELESS", false), "Run Streamable HTTP in stateless mode (env MCP_HTTP_STATELESS)")
	fs.BoolVar(&c.HTTPJSONResponse, "http-json-response", be.get("MCP_HTTP_JSON_RESPONSE", false), "Answer with application/json instead of SSE streams (env MCP_HTTP_JSON_RESPONSE)")
	fs.BoolVar(&c.HTTPEnableSSE, "http-enable-sse", be.get("MCP_HTTP_ENABLE_SSE", true), "Also serve the legacy HTTP+SSE transport on /sse (env MCP_HTTP_ENABLE_SSE)")
	sessTimeout := fs.String("http-session-timeout", envOr("MCP_HTTP_SESSION_TIMEOUT", "30m"), "Idle timeout for HTTP sessions, 0 disables (env MCP_HTTP_SESSION_TIMEOUT)")
	fs.BoolVar(&c.AllowNoAuth, "allow-no-auth", be.get("MCP_ALLOW_NO_AUTH", false), "Allow http transport without bearer token (env MCP_ALLOW_NO_AUTH)")
	allowedHosts := fs.String("allowed-hosts", envOr("MCP_ALLOWED_HOSTS", ""), "Extra Host header values accepted by the MCP endpoints besides localhost (env MCP_ALLOWED_HOSTS)")
	fs.StringVar(&c.LogLevel, "log-level", envOr("LOG_LEVEL", "info"), "debug, info, warn, error (env LOG_LEVEL)")
	fs.StringVar(&c.LogFormat, "log-format", envOr("LOG_FORMAT", "text"), "text or json (env LOG_FORMAT)")

	// sevDesk settings are environment-only: credentials never belong on a command line.
	readOnly := be.get("SEVDESK_READ_ONLY", true)
	allowWriteSet := isSet("SEVDESK_ALLOW_WRITE")
	allowWrite := be.get("SEVDESK_ALLOW_WRITE", true)
	allowBookingSet := isSet("SEVDESK_ALLOW_BOOKING")
	allowBooking := be.get("SEVDESK_ALLOW_BOOKING", false)
	c.DryRun = be.get("SEVDESK_DRY_RUN", false)
	c.AllowClientToken = be.get("SEVDESK_ALLOW_CLIENT_TOKEN", false)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if be.err != nil {
		return nil, be.err
	}

	var err error
	for _, h := range splitList(*allowedHosts) {
		h = strings.ToLower(h)
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		c.AllowedHosts = append(c.AllowedHosts, strings.Trim(h, "[]"))
	}
	if c.HTTPSessionTimeout, err = time.ParseDuration(*sessTimeout); err != nil || c.HTTPSessionTimeout < 0 {
		return nil, fmt.Errorf("invalid MCP_HTTP_SESSION_TIMEOUT %q (use a duration >= 0, 0 disables)", *sessTimeout)
	}

	// Tiers. Explicit contradictions fail instead of being guessed.
	if readOnly {
		if allowWriteSet && allowWrite {
			return nil, errors.New("SEVDESK_ALLOW_WRITE=true conflicts with SEVDESK_READ_ONLY=true (the default); set SEVDESK_READ_ONLY=false to enable write tools")
		}
		if allowBookingSet && allowBooking {
			return nil, errors.New("SEVDESK_ALLOW_BOOKING=true conflicts with SEVDESK_READ_ONLY=true (the default); set SEVDESK_READ_ONLY=false and SEVDESK_ALLOW_WRITE=true first")
		}
	} else {
		c.AllowWrite = allowWrite
		if allowBooking && !allowWrite {
			return nil, errors.New("SEVDESK_ALLOW_BOOKING=true requires SEVDESK_ALLOW_WRITE=true")
		}
		c.AllowBooking = allowBooking && allowWrite
	}
	c.ReadOnly = !c.AllowWrite

	c.BaseURL = envOr("SEVDESK_BASE_URL", sevdesk.DefaultBaseURL)
	if c.BaseURL, err = sevdesk.ValidateBaseURL(c.BaseURL); err != nil {
		return nil, err
	}
	if c.Timeout, err = time.ParseDuration(envOr("SEVDESK_TIMEOUT", "30s")); err != nil || c.Timeout <= 0 {
		return nil, fmt.Errorf("invalid SEVDESK_TIMEOUT %q", os.Getenv("SEVDESK_TIMEOUT"))
	}
	if c.RateLimit, err = strconv.ParseFloat(envOr("SEVDESK_RATE_LIMIT", "5"), 64); err != nil || c.RateLimit < 0 || c.RateLimit > 100 {
		return nil, fmt.Errorf("invalid SEVDESK_RATE_LIMIT %q (requests per second, 0-100, 0 disables)", os.Getenv("SEVDESK_RATE_LIMIT"))
	}
	if c.DefaultLimit, err = strconv.Atoi(envOr("DEFAULT_LIMIT", "50")); err != nil || c.DefaultLimit < 1 || c.DefaultLimit > 100 {
		return nil, fmt.Errorf("invalid DEFAULT_LIMIT %q (1-100)", os.Getenv("DEFAULT_LIMIT"))
	}
	c.MaxUploadBytes, err = strconv.ParseInt(envOr("SEVDESK_MAX_UPLOAD_BYTES", strconv.Itoa(10<<20)), 10, 64)
	if err != nil || c.MaxUploadBytes < 1 || c.MaxUploadBytes > 32<<20 {
		return nil, fmt.Errorf("invalid SEVDESK_MAX_UPLOAD_BYTES %q (1 to %d)", os.Getenv("SEVDESK_MAX_UPLOAD_BYTES"), 32<<20)
	}
	c.MaxDownloadBytes, err = strconv.ParseInt(envOr("SEVDESK_MAX_DOWNLOAD_BYTES", strconv.Itoa(8<<20)), 10, 64)
	if err != nil || c.MaxDownloadBytes < 1 || c.MaxDownloadBytes > 32<<20 {
		return nil, fmt.Errorf("invalid SEVDESK_MAX_DOWNLOAD_BYTES %q (1 to %d)", os.Getenv("SEVDESK_MAX_DOWNLOAD_BYTES"), 32<<20)
	}
	if c.UploadDir = os.Getenv("SEVDESK_UPLOAD_DIR"); c.UploadDir != "" {
		st, err := os.Stat(c.UploadDir)
		if err != nil || !st.IsDir() {
			return nil, fmt.Errorf("SEVDESK_UPLOAD_DIR %q is not an existing directory", c.UploadDir)
		}
	}

	if c.APIToken, err = secretFromEnv("SEVDESK_API_TOKEN"); err != nil {
		return nil, err
	}
	tok, err := secretFromEnv("MCP_AUTH_TOKEN")
	if err != nil {
		return nil, err
	}
	c.AuthTokens = splitList(tok)

	return c, c.Validate()
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	switch c.Transport {
	case "stdio":
	case "http", "streamable-http", "sse":
		c.Transport = "http"
		if !strings.HasPrefix(c.HTTPPath, "/") {
			c.HTTPPath = "/" + c.HTTPPath
		}
		if err := validateHTTPPath(c.HTTPPath, c.HTTPEnableSSE); err != nil {
			return err
		}
		if len(c.AuthTokens) == 0 && !c.AllowNoAuth {
			return errors.New("http transport requires MCP_AUTH_TOKEN (or set MCP_ALLOW_NO_AUTH=true to explicitly run without authentication)")
		}
		for _, t := range c.AuthTokens {
			if len(t) < 16 {
				return errors.New("MCP_AUTH_TOKEN values must be at least 16 characters long")
			}
		}
		if len(c.AuthTokens) == 0 && !c.ReadOnly {
			return errors.New("write tools over http need MCP_AUTH_TOKEN: MCP_ALLOW_NO_AUTH=true is only allowed in read-only mode (SEVDESK_READ_ONLY=true)")
		}
	default:
		return fmt.Errorf("unknown transport %q (use stdio or http)", c.Transport)
	}

	if c.AllowClientToken {
		if c.Transport != "http" {
			return errors.New("SEVDESK_ALLOW_CLIENT_TOKEN=true only works with the http transport (clients send the token in the " + ClientTokenHeader + " header)")
		}
		if len(c.AuthTokens) == 0 {
			return errors.New("SEVDESK_ALLOW_CLIENT_TOKEN=true requires MCP_AUTH_TOKEN: client sevDesk tokens are only accepted from authenticated MCP clients")
		}
	}
	if c.APIToken == "" && !c.AllowClientToken {
		return errors.New("SEVDESK_API_TOKEN (or SEVDESK_API_TOKEN_FILE) is required; alternatively set SEVDESK_ALLOW_CLIENT_TOKEN=true with the http transport so clients bring their own token")
	}
	if c.APIToken != "" {
		if err := sevdesk.ValidateToken(c.APIToken); err != nil {
			return fmt.Errorf("SEVDESK_API_TOKEN: %w", err)
		}
	}
	return nil
}
