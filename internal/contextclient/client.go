// Package contextclient keeps Context impersonation credentials and tokens in the wing.
package contextclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/protectedfile"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const maxResponseBytes = 2 << 20

type cachedToken struct {
	value   string
	expires time.Time
}

type Client struct {
	cfg       config.ContextConfig
	secret    []byte
	http      *http.Client
	tokenLock chan struct{}
	tokens    map[string]cachedToken
	now       func() time.Time
}

// New is called only in the wing/roost, never in an egg process.
func New(cfg *config.ContextConfig) (*Client, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secret, err := protectedfile.ReadResolved(cfg.SecretFile)
	if err != nil {
		var violation *protectedfile.Error
		if errors.As(err, &violation) {
			return nil, fmt.Errorf("context: cannot read secret_file: %s", violation.Reason)
		}
		return nil, fmt.Errorf("context: cannot read secret_file")
	}
	secret = bytes.TrimSpace(secret)
	if len(secret) == 0 {
		return nil, fmt.Errorf("context: secret_file is empty")
	}
	copyCfg := *cfg
	copyCfg.Scopes = append([]string(nil), cfg.Scopes...)
	return &Client{cfg: copyCfg, secret: secret, http: &http.Client{
		// Never forward credentials or assertions to redirects, including on the same host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, tokens: make(map[string]cachedToken), tokenLock: make(chan struct{}, 1), now: time.Now}, nil
}

func (c *Client) token(ctx context.Context, owner, rejected string) (string, error) {
	select {
	case c.tokenLock <- struct{}{}:
		defer func() { <-c.tokenLock }()
	case <-ctx.Done():
		return "", fmt.Errorf("context: request timed out or cancelled")
	}
	now := c.now()
	// Invalidate only the rejected token, preserving any concurrent refresh.
	if rejected != "" && c.tokens[owner].value == rejected {
		delete(c.tokens, owner)
	}
	if token := c.tokens[owner]; now.Before(token.expires) {
		return token.value, nil
	}
	endpoint := strings.TrimRight(c.cfg.URL, "/") + "/oauth/token"
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer: c.cfg.ClientID, Subject: c.cfg.ClientID, Audience: jwt.ClaimStrings{endpoint},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(60 * time.Second)), ID: uuid.NewString(),
	}).SignedString(c.secret)
	if err != nil {
		return "", fmt.Errorf("context: cannot sign client assertion")
	}
	form := url.Values{
		"client_id":             {c.cfg.ClientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
		"grant_type":            {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token_type":    {"https://context.pants.taxi/oauth/token-type/user"},
		"subject_token":         {owner},
	}
	if len(c.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(c.cfg.Scopes, " "))
	}
	body, err := c.post(ctx, endpoint, "application/x-www-form-urlencoded", "", []byte(form.Encode()), "", form.Encode(), assertion, rejected)
	if err != nil {
		return "", err
	}
	var response struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.Unmarshal(body, &response) != nil || response.AccessToken == "" || response.ExpiresIn <= 0 || response.ExpiresIn > 3600 {
		return "", fmt.Errorf("context: invalid token response")
	}
	// Refresh shortly before expiry; short-lived tokens retain a useful cache window.
	ttl := time.Duration(response.ExpiresIn) * time.Second
	margin := min(30*time.Second, ttl/10)
	c.tokens[owner] = cachedToken{response.AccessToken, now.Add(ttl - margin)}
	return response.AccessToken, nil
}

// Call's owner must come from the authenticated session, independently of tool arguments and env.
func (c *Client) Call(ctx context.Context, owner, name string, arguments map[string]any) (string, error) {
	if c == nil {
		return "", fmt.Errorf("context: wing context block is required")
	}
	owner = strings.ToLower(strings.TrimSpace(owner))
	address, err := mail.ParseAddress(owner)
	if err != nil || address.Address != owner {
		return "", fmt.Errorf("context: verified owner email is required")
	}
	token, err := c.token(ctx, owner, "")
	if err != nil {
		return "", err
	}
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments, "_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "wingthing", "version": "1"},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("context: invalid tool arguments")
	}
	credentials := []string{token}
	body, err := c.post(ctx, strings.TrimRight(c.cfg.URL, "/")+"/mcp", "application/json", token, request, name)
	if status, ok := err.(*httpError); ok && status.code == http.StatusUnauthorized {
		rejected := token
		token, err = c.token(ctx, owner, rejected)
		credentials = append(credentials, token)
		if err == nil {
			body, err = c.post(ctx, strings.TrimRight(c.cfg.URL, "/")+"/mcp", "application/json", token, request, name, rejected)
		}
		if err != nil {
			return "", fmt.Errorf("%s", c.redact(err.Error(), credentials...))
		}
	}
	if err != nil {
		return "", err
	}
	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &response) != nil || response.JSONRPC != "2.0" || response.ID != 1 {
		return "", fmt.Errorf("context: invalid MCP response")
	}
	if response.Error != nil {
		return "", fmt.Errorf("context: MCP error %d: %s", response.Error.Code, c.redact(response.Error.Message, credentials...))
	}
	if response.Result == nil {
		return "", fmt.Errorf("context: missing MCP result")
	}
	var texts []string
	for _, content := range response.Result.Content {
		if content.Type == "text" {
			texts = append(texts, content.Text)
		}
	}
	output := c.redact(strings.Join(texts, "\n"), credentials...)
	if response.Result.IsError {
		return "", fmt.Errorf("context: %s", output)
	}
	return output, nil
}

func (c *Client) redact(text string, credentials ...string) string {
	for _, credential := range append(credentials, string(c.secret)) {
		if credential != "" {
			text = strings.ReplaceAll(text, credential, "[redacted]")
		}
	}
	return text
}

type httpError struct {
	code    int
	message string
}

func (e *httpError) Error() string { return e.message }

func (c *Client) post(ctx context.Context, endpoint, contentType, token string, body []byte, name string, sensitive ...string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("context: cannot create request")
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", name)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context: request timed out or cancelled")
		}
		return nil, fmt.Errorf("context: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("context: HTTP %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		var detail struct {
			Message          string `json:"message"`
			ErrorDescription string `json:"error_description"`
			// MCP rejects unknown or unpermitted tools with a JSON-RPC error body.
			RPCError *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err == nil && len(data) <= maxResponseBytes && json.Unmarshal(data, &detail) == nil {
			if detail.Message == "" {
				detail.Message = detail.ErrorDescription
			}
			if detail.Message == "" && detail.RPCError != nil {
				detail.Message = detail.RPCError.Message
			}
			// Redact before truncating so a credential at the boundary cannot leak.
			text := []rune(c.redact(detail.Message, append(sensitive, token)...))
			if len(text) > 500 {
				text = text[:500]
			}
			if len(text) > 0 {
				message += ": " + string(text)
			}
		}
		return nil, &httpError{code: resp.StatusCode, message: message}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, fmt.Errorf("context: unreadable or oversized response")
	}
	return data, nil
}
