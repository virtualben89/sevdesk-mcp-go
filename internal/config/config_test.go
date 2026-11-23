package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodTok = "0123456789abcdef0123456789abcdef"
const mcpTok = "mcp-test-token-0123456789"

var envKeys = []string{"MCP_TRANSPORT", "MCP_HTTP_ADDR", "MCP_HTTP_PATH", "MCP_HTTP_STATELESS", "MCP_HTTP_JSON_RESPONSE", "MCP_HTTP_ENABLE_SSE",
	"MCP_HTTP_SESSION_TIMEOUT", "MCP_ALLOW_NO_AUTH", "MCP_ALLOWED_HOSTS", "MCP_AUTH_TOKEN", "MCP_AUTH_TOKEN_FILE", "LOG_LEVEL", "LOG_FORMAT",
	"SEVDESK_API_TOKEN", "SEVDESK_API_TOKEN_FILE", "SEVDESK_BASE_URL", "SEVDESK_READ_ONLY", "SEVDESK_ALLOW_WRITE", "SEVDESK_ALLOW_BOOKING",
	"SEVDESK_DRY_RUN", "SEVDESK_ALLOW_CLIENT_TOKEN", "SEVDESK_TIMEOUT", "SEVDESK_RATE_LIMIT", "SEVDESK_UPLOAD_DIR", "SEVDESK_MAX_UPLOAD_BYTES",
	"SEVDESK_MAX_DOWNLOAD_BYTES", "DEFAULT_LIMIT"}

func load(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load(nil)
}

func base() map[string]string { return map[string]string{"SEVDESK_API_TOKEN": goodTok} }

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestDefaultsAreReadOnly(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if !c.ReadOnly || c.AllowWrite || c.AllowBooking || c.DryRun || c.AllowClientToken {
		t.Errorf("unsafe defaults: %+v", c)
	}
	if c.Transport != "stdio" || c.BaseURL != "https://my.sevdesk.de/api/v1" {
		t.Errorf("%+v", c)
	}
}

func TestTierMatrix(t *testing.T) {
	cases := []struct {
		name        string
		env         []string
		wantErr     string
		write, book bool
	}{
		{"default", nil, "", false, false},
		{"write on", []string{"SEVDESK_READ_ONLY", "false"}, "", true, false},
		{"write explicit", []string{"SEVDESK_READ_ONLY", "false", "SEVDESK_ALLOW_WRITE", "true"}, "", true, false},
		{"read-only false but write false", []string{"SEVDESK_READ_ONLY", "false", "SEVDESK_ALLOW_WRITE", "false"}, "", false, false},
		{"booking", []string{"SEVDESK_READ_ONLY", "false", "SEVDESK_ALLOW_BOOKING", "true"}, "", true, true},
		{"allow_write conflicts with default read-only", []string{"SEVDESK_ALLOW_WRITE", "true"}, "conflicts with SEVDESK_READ_ONLY", false, false},
		{"allow_booking conflicts with default read-only", []string{"SEVDESK_ALLOW_BOOKING", "true"}, "conflicts with SEVDESK_READ_ONLY", false, false},
		{"booking without write", []string{"SEVDESK_READ_ONLY", "false", "SEVDESK_ALLOW_WRITE", "false", "SEVDESK_ALLOW_BOOKING", "true"}, "requires SEVDESK_ALLOW_WRITE", false, false},
		{"unparseable read-only fails closed", []string{"SEVDESK_READ_ONLY", "off"}, "invalid boolean", false, false},
		{"unparseable booking fails closed", []string{"SEVDESK_READ_ONLY", "false", "SEVDESK_ALLOW_BOOKING", "yes please"}, "invalid boolean", false, false},
		{"unparseable dry run", []string{"SEVDESK_DRY_RUN", "maybe"}, "invalid boolean", false, false},
	}
	for _, c := range cases {
		cfg, err := load(t, with(base(), c.env...))
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if cfg.AllowWrite != c.write || cfg.AllowBooking != c.book || cfg.ReadOnly == c.write {
			t.Errorf("%s: write=%v booking=%v readonly=%v", c.name, cfg.AllowWrite, cfg.AllowBooking, cfg.ReadOnly)
		}
	}
}

func TestHTTPAuthRules(t *testing.T) {
	h := with(base(), "MCP_TRANSPORT", "http")
	if _, err := load(t, h); err == nil || !strings.Contains(err.Error(), "MCP_AUTH_TOKEN") {
		t.Errorf("http without token: %v", err)
	}
	if _, err := load(t, with(h, "MCP_AUTH_TOKEN", "short")); err == nil || !strings.Contains(err.Error(), "16 characters") {
		t.Errorf("short token: %v", err)
	}
	c, err := load(t, with(h, "MCP_AUTH_TOKEN", mcpTok+", other-token-0123456789"))
	if err != nil || len(c.AuthTokens) != 2 {
		t.Errorf("rotation list: %v %+v", err, c)
	}
	f := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(f, []byte(mcpTok+"\n"), 0o600)
	if c, err := load(t, with(h, "MCP_AUTH_TOKEN_FILE", f)); err != nil || c.AuthTokens[0] != mcpTok {
		t.Errorf("_FILE: %v", err)
	}
	if _, err := load(t, with(h, "MCP_ALLOW_NO_AUTH", "true")); err != nil {
		t.Errorf("no-auth read-only: %v", err)
	}
	if _, err := load(t, with(h, "MCP_ALLOW_NO_AUTH", "true", "SEVDESK_READ_ONLY", "false")); err == nil || !strings.Contains(err.Error(), "write tools over http") {
		t.Errorf("no-auth with writes must be refused: %v", err)
	}
	if _, err := load(t, with(h, "MCP_AUTH_TOKEN", mcpTok, "SEVDESK_READ_ONLY", "false")); err != nil {
		t.Errorf("authenticated write: %v", err)
	}
	if _, err := load(t, with(h, "MCP_AUTH_TOKEN", mcpTok, "MCP_HTTP_PATH", "/sse")); err == nil {
		t.Error("path collision accepted")
	}
}

func TestSevdeskTokenRules(t *testing.T) {
	if _, err := load(t, nil); err == nil || !strings.Contains(err.Error(), "SEVDESK_API_TOKEN") {
		t.Errorf("missing token: %v", err)
	}
	for _, bad := range []string{"abc", goodTok + "0", "Bearer " + goodTok, strings.Repeat("z", 32)} {
		if _, err := load(t, map[string]string{"SEVDESK_API_TOKEN": bad}); err == nil {
			t.Errorf("token %q accepted", bad)
		} else if strings.Contains(err.Error(), bad) {
			t.Errorf("error leaks token: %v", err)
		}
	}
	f := filepath.Join(t.TempDir(), "sd")
	_ = os.WriteFile(f, []byte(goodTok+"\n"), 0o600)
	if c, err := load(t, map[string]string{"SEVDESK_API_TOKEN_FILE": f}); err != nil || c.APIToken != goodTok {
		t.Errorf("_FILE: %v", err)
	}
	// client tokens: need http + bearer; server token then optional
	ct := map[string]string{"SEVDESK_ALLOW_CLIENT_TOKEN": "true"}
	if _, err := load(t, ct); err == nil || !strings.Contains(err.Error(), "http transport") {
		t.Errorf("client token on stdio: %v", err)
	}
	if _, err := load(t, with(ct, "MCP_TRANSPORT", "http", "MCP_ALLOW_NO_AUTH", "true")); err == nil || !strings.Contains(err.Error(), "requires MCP_AUTH_TOKEN") {
		t.Errorf("client token without bearer: %v", err)
	}
	c, err := load(t, with(ct, "MCP_TRANSPORT", "http", "MCP_AUTH_TOKEN", mcpTok))
	if err != nil || c.APIToken != "" || !c.AllowClientToken {
		t.Errorf("multi-tenant mode: %v %+v", err, c)
	}
	if _, err := load(t, with(ct, "MCP_TRANSPORT", "http", "MCP_AUTH_TOKEN", mcpTok, "SEVDESK_ALLOW_CLIENT_TOKEN", "enabled")); err == nil {
		t.Error("unparseable client token flag accepted")
	}
}

func TestBaseURLAndLimits(t *testing.T) {
	if _, err := load(t, with(base(), "SEVDESK_BASE_URL", "http://example.com/api/v1")); err == nil {
		t.Error("http base URL accepted")
	}
	if c, err := load(t, with(base(), "SEVDESK_BASE_URL", "http://127.0.0.1:9000/api/v1")); err != nil || c.BaseURL != "http://127.0.0.1:9000/api/v1" {
		t.Errorf("loopback: %v", err)
	}
	for k, v := range map[string]string{"SEVDESK_RATE_LIMIT": "-1", "DEFAULT_LIMIT": "0", "SEVDESK_MAX_UPLOAD_BYTES": "0", "SEVDESK_MAX_DOWNLOAD_BYTES": "999999999", "SEVDESK_TIMEOUT": "soon", "SEVDESK_UPLOAD_DIR": "/does/not/exist"} {
		if _, err := load(t, with(base(), k, v)); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
	d := t.TempDir()
	if c, err := load(t, with(base(), "SEVDESK_UPLOAD_DIR", d)); err != nil || c.UploadDir != d {
		t.Errorf("upload dir: %v", err)
	}
}
