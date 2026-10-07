package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBaseURL is the TypeSafe API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the model a Client requests unless configured otherwise.
	DefaultModel = "jev-latest"
	// DefaultTimeout bounds each HTTP attempt of the default HTTP client.
	DefaultTimeout = 10 * time.Second

	// EnvAPIKey names the variable that supplies the API key.
	EnvAPIKey = "TYPESAFE_API_KEY" //nolint:gosec // A variable name, not a credential.
	// EnvBaseURL names the variable that supplies the base URL.
	EnvBaseURL = "TYPESAFE_BASE_URL"
	// EnvDefaultModel names the variable that supplies the default model.
	EnvDefaultModel = "TYPESAFE_DEFAULT_MODEL"
)

const (
	systemOnePath    = "/v1/systemone"
	modelsPath       = "/v1/models"
	requestIDHeader  = "X-Typesafe-Request-Id"
	retryCountHeader = "X-Typesafe-Retry-Count"
	jsonContentType  = "application/json"
	userAgent        = "go-systemone"
	maxResponseBytes = 32 << 20
)

// protectedHeaders are set by the client and never taken from WithHeader.
var protectedHeaders = []string{"Authorization", "Accept", "Content-Type", "User-Agent", retryCountHeader}

// Client calls a System One server. It is safe for concurrent use.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	retry      RetryPolicy
	header     http.Header
}

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the bearer token. With no key the request carries no
// Authorization header.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
}

// WithBaseURL sets the server root, such as "http://localhost:11434".
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = baseURL }
}

// WithModel sets the model requested when a Request names none.
func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

// WithHTTPClient sets the HTTP client. Its timeout and redirect policy apply
// unchanged.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.httpClient = httpClient }
}

// WithHeader adds a header to every request. Authorization, Accept,
// Content-Type, User-Agent, and the retry count are reserved.
func WithHeader(name, value string) Option {
	return func(c *Client) { c.header.Add(name, value) }
}

// WithRetryPolicy replaces DefaultRetryPolicy.
func WithRetryPolicy(policy RetryPolicy) Option {
	return func(c *Client) { c.retry = policy }
}

// New returns a Client. Options take precedence over EnvAPIKey, EnvBaseURL,
// and EnvDefaultModel; blank variables are ignored.
//
// The default HTTP client bounds each attempt by DefaultTimeout and does not
// follow redirects.
func New(opts ...Option) (*Client, error) {
	c := &Client{
		apiKey:  env(EnvAPIKey, ""),
		baseURL: env(EnvBaseURL, DefaultBaseURL),
		model:   env(EnvDefaultModel, DefaultModel),
		retry:   DefaultRetryPolicy(),
		header:  make(http.Header),
	}

	for _, opt := range opts {
		opt(c)
	}

	if err := c.retry.validate(); err != nil {
		return nil, fmt.Errorf("systemone: retry policy: %w", err)
	}

	if c.model == "" {
		return nil, errors.New("systemone: model is empty")
	}

	base, err := url.Parse(c.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("systemone: base URL must be an absolute http or https URL without credentials, query, or fragment")
	}

	c.baseURL = strings.TrimRight(c.baseURL, "/")

	for _, name := range protectedHeaders {
		c.header.Del(name)
	}

	if !validHeaderValue(c.apiKey) {
		return nil, errors.New("systemone: API key contains a control character")
	}

	for name, values := range c.header {
		if !validHeaderName(name) {
			return nil, fmt.Errorf("systemone: header name %q is not a valid token", name)
		}

		for _, value := range values {
			if !validHeaderValue(value) {
				return nil, fmt.Errorf("systemone: header %s contains a control character", name)
			}
		}
	}

	if c.httpClient == nil {
		c.httpClient = &http.Client{
			Timeout: DefaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	return c, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}

	return fallback
}

// validHeaderName reports whether name is an RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}

	for _, r := range name {
		alphanumeric := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if r >= utf8.RuneSelf || !alphanumeric && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}

	return true
}

// validHeaderValue reports whether value holds no control character other than
// horizontal tab, which net/http would refuse on every attempt.
func validHeaderValue(value string) bool {
	for i := range len(value) {
		if b := value[i]; (b < ' ' && b != '\t') || b == 0x7f {
			return false
		}
	}

	return true
}

// Request is one System One call.
type Request struct {
	// State is the content to judge: a string, a JSON object, or an array.
	State any
	// Questions maps each question name to its question. Answers use the same
	// names.
	Questions map[string]Question
	// Model overrides the client's model when not empty.
	Model string
}

// SystemOne answers the request's questions about its state.
func (c *Client) SystemOne(ctx context.Context, req Request) (*Response, error) {
	if err := validateQuestions(req.Questions); err != nil {
		return nil, err
	}

	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, fmt.Errorf("%w: state: %w", ErrInvalidRequest, err)
	}

	if len(state) == 0 || (state[0] != '"' && state[0] != '{' && state[0] != '[') {
		return nil, fmt.Errorf("%w: state must be a string, a JSON object, or an array", ErrInvalidRequest)
	}

	model := req.Model
	if model == "" {
		model = c.model
	}

	body, err := json.Marshal(struct {
		State     json.RawMessage     `json:"state"`
		Model     string              `json:"model"`
		Questions map[string]Question `json:"questions"`
	}{state, model, req.Questions})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	result, err := c.send(ctx, http.MethodPost, systemOnePath, body)
	if err != nil {
		return nil, err
	}

	return decodeResponse(result.body, result.requestID)
}

// ListModels lists the models the server offers.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	result, err := c.send(ctx, http.MethodGet, modelsPath, nil)
	if err != nil {
		return nil, err
	}

	return decodeModels(result.body)
}

type result struct {
	body      []byte
	requestID string
}

func (c *Client) send(ctx context.Context, method, path string, body []byte) (result, error) {
	start := time.Now()

	for attempt := 0; ; attempt++ {
		res, err := c.attempt(ctx, method, path, body, attempt)
		if err == nil {
			return res, nil
		}

		if attempt >= c.retry.MaxRetries || !retryable(ctx, err) {
			return result{}, err
		}

		delay := c.retry.delay(attempt+1, err)
		if c.retry.Budget > 0 && delay >= c.retry.Budget-time.Since(start) {
			return result{}, err
		}

		if err := sleep(ctx, delay); err != nil {
			return result{}, err
		}
	}
}

func (c *Client) attempt(ctx context.Context, method, path string, body []byte, attempt int) (result, error) {
	endpoint := method + " " + c.baseURL + path

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return result{}, fmt.Errorf("systemone: %s: %w", endpoint, err)
	}

	req.Header = c.header.Clone()
	req.Header.Set("Accept", jsonContentType)
	req.Header.Set("User-Agent", userAgent)

	if body != nil {
		req.Header.Set("Content-Type", jsonContentType)
	}

	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	if attempt > 0 {
		req.Header.Set(retryCountHeader, strconv.Itoa(attempt))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result{}, fmt.Errorf("systemone: %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return result{}, fmt.Errorf("systemone: %s: read response: %w", endpoint, err)
	}

	requestID := resp.Header.Get(requestIDHeader)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		apiErr := &APIError{
			StatusCode: resp.StatusCode,
			Message:    errorMessage(data),
			Body:       data,
			RequestID:  requestID,
			Endpoint:   endpoint,
		}
		apiErr.RetryAfter, apiErr.retryAfterSet = parseRetryAfter(resp.Header, time.Now())

		return result{}, apiErr
	}

	return result{body: data, requestID: requestID}, nil
}
