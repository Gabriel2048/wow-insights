// Package warcraftlogs is a client for the Warcraft Logs v2 GraphQL API.
//
// It authenticates with the OAuth 2.0 client credentials flow, which grants
// access to the public API at /api/v2/client. Private reports would require
// the authorization code flow against /api/v2/user instead.
package warcraftlogs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTokenURL = "https://www.warcraftlogs.com/oauth/token"
	defaultAPIURL   = "https://www.warcraftlogs.com/api/v2/client"

	// Tokens are renewed this long before they actually expire, so a request
	// never leaves with a token that dies in flight.
	expiryLeeway = time.Minute
)

// ErrNoCredentials is returned when the client was built without a client ID
// and secret.
var ErrNoCredentials = errors.New("warcraftlogs: missing client ID or secret")

// Client is a Warcraft Logs API client. It is safe for concurrent use; the
// access token is cached and renewed on demand.
type Client struct {
	id     string
	secret string
	http   *http.Client

	// The two endpoints are separate because the real service puts them on
	// unrelated paths: the OAuth endpoint is not under /api. A test server has
	// to answer both, so WithBaseURL takes both.
	tokenURL string
	apiURL   string

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// Option configures a Client. Go has neither constructor overloads nor optional
// parameters, so this is how New takes anything beyond the credentials.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client used for both the token request and
// the API request. A nil client is ignored rather than installed, so a caller
// cannot accidentally strip the default timeout.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithBaseURL points the client at different token and API endpoints. Both are
// required: they are unrelated paths on the real service, so a test server has
// to serve both.
func WithBaseURL(tokenURL, apiURL string) Option {
	return func(c *Client) {
		c.tokenURL = tokenURL
		c.apiURL = apiURL
	}
}

// New returns a Client authenticating with the given OAuth credentials.
func New(id, secret string, opts ...Option) *Client {
	c := &Client{
		id:       id,
		secret:   secret,
		http:     &http.Client{Timeout: 30 * time.Second},
		tokenURL: defaultTokenURL,
		apiURL:   defaultAPIURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// accessToken returns a cached token, fetching a new one if none is held or the
// current one is close to expiring.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.id == "" || c.secret == "" {
		return "", ErrNoCredentials
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiry.Add(-expiryLeeway)) {
		return c.token, nil
	}

	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.id, c.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("warcraftlogs: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("warcraftlogs: token request returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("warcraftlogs: decode token: %w", err)
	}
	if token.AccessToken == "" {
		return "", errors.New("warcraftlogs: token response contained no access token")
	}

	c.token = token.AccessToken
	c.expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return c.token, nil
}

// graphQLError is a single error entry returned by the API.
type graphQLError struct {
	Message string `json:"message"`
}

// Query runs a GraphQL query and unmarshals the "data" object into out.
func (c *Client) Query(ctx context.Context, query string, variables map[string]any, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("warcraftlogs: query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("warcraftlogs: query returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("warcraftlogs: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			messages[i] = e.Message
		}
		return fmt.Errorf("warcraftlogs: %s", strings.Join(messages, "; "))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

// rateLimitResponse is the envelope the rate limit query returns.
type rateLimitResponse struct {
	RateLimitData RateLimit `json:"rateLimitData"`
}

// RateLimit describes the API points budget for the current hour.
type RateLimit struct {
	LimitPerHour        int     `json:"limitPerHour"`
	PointsSpentThisHour float64 `json:"pointsSpentThisHour"`
	PointsResetIn       int     `json:"pointsResetIn"`
}

// RateLimit fetches the current points budget. It is the cheapest query the API
// offers, which makes it a good check that credentials work.
func (c *Client) RateLimit(ctx context.Context) (RateLimit, error) {
	var data rateLimitResponse
	const query = `query { rateLimitData { limitPerHour pointsSpentThisHour pointsResetIn } }`
	err := c.Query(ctx, query, nil, &data)
	return data.RateLimitData, err
}
