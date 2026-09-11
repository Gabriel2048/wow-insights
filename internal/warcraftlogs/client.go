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
		return "", fmt.Errorf("%w: token request: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: reading token response: %w", ErrUpstream, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		// The OAuth endpoint answers a wrong client id or secret with one of
		// these; it is a deployment fault, not an outage.
		return "", fmt.Errorf("%w: token endpoint returned HTTP %d", ErrBadCredentials, resp.StatusCode)
	default:
		return "", &APIError{Status: resp.StatusCode, Body: keepBody(body), RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}

	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("%w: decode token: %w", ErrUpstream, err)
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("%w: token response carried no access token", ErrUpstream)
	}

	c.token = token.AccessToken
	c.expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return c.token, nil
}

// graphQLError is a single error entry returned by the API. Path names the
// field the error sits on, so a caller can say which part of a document was
// lost.
type graphQLError struct {
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (e graphQLError) path() string {
	parts := make([]string, 0, len(e.Path))
	for _, p := range e.Path {
		parts = append(parts, fmt.Sprint(p))
	}
	return strings.Join(parts, ".")
}

// Query runs a GraphQL query and unmarshals the "data" object into out.
//
// GraphQL permits a 200 carrying both data and errors: one failing field in a
// document of eleven leaves the other ten intact. So the data is decoded into
// out before the errors are reported, and the error returned is an *APIError
// naming the fields that failed — a caller that can use a partial document
// checks for it with errors.As and carries on with what it has.
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
		return fmt.Errorf("%w: query: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%w: reading response: %w", ErrUpstream, err)
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Body: keepBody(body), RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%w: decode response: %w", ErrUpstream, err)
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("%w: decode data: %w", ErrUpstream, err)
		}
	}
	if len(envelope.Errors) > 0 {
		apiErr := &APIError{Status: http.StatusOK}
		for _, e := range envelope.Errors {
			apiErr.Messages = append(apiErr.Messages, e.Message)
			apiErr.Paths = append(apiErr.Paths, e.path())
		}
		return apiErr
	}
	return nil
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
