package systemone

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type capture struct {
	method string
	path   string
	header http.Header
	body   string
}

type recorder struct {
	mu    sync.Mutex
	calls []capture
}

func (r *recorder) add(c capture) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, c)

	return len(r.calls)
}

func (r *recorder) all() []capture {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}

func newServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, attempt int)) (*httptest.Server, *recorder) {
	t.Helper()

	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		attempt := rec.add(capture{r.Method, r.URL.Path, r.Header.Clone(), string(body)})
		handler(w, r, attempt)
	}))
	t.Cleanup(server.Close)

	return server, rec
}

func newClient(t *testing.T, baseURL string, opts ...Option) *Client {
	t.Helper()

	noRetry := RetryPolicy{}
	client, err := New(append([]Option{WithBaseURL(baseURL), WithAPIKey("key"), WithRetryPolicy(noRetry)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return client
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

const okBody = `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`

func TestSystemOneRequestWire(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, okBody)
	})
	client := newClient(t, server.URL+"/", WithModel("jev-preview"), WithHeader("X-Extra", "1"), WithHeader("Authorization", "spoofed"))

	_, err := client.SystemOne(context.Background(), Request{
		State: map[string]any{"message": "charged twice"},
		Questions: map[string]Question{
			"billing": Noul{Instructions: "Is this about billing?", Criteria: &NoulCriteria{True: "mentions a charge"}},
			"tone": Choice{Criteria: []Criterion{
				{Label: "zeta", Description: "listed first"},
				{Label: "alpha"},
			}},
			"urgency": &Score{Instructions: "How urgent?", Criteria: []any{"low", map[string]any{"level": "high"}}},
		},
	})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if len(calls.all()) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls.all()))
	}

	call := calls.all()[0]
	if call.method != http.MethodPost || call.path != "/v1/systemone" {
		t.Errorf("request = %s %s, want POST /v1/systemone", call.method, call.path)
	}

	want := `{"state":{"message":"charged twice"},"model":"jev-preview","questions":{` +
		`"billing":{"type":"noul","instructions":"Is this about billing?","criteria":{"true":"mentions a charge"}},` +
		`"tone":{"type":"choice","criteria":{"zeta":"listed first","alpha":null}},` +
		`"urgency":{"type":"score","instructions":"How urgent?","criteria":["low",{"level":"high"}]}}}`
	if call.body != want {
		t.Errorf("body =\n%s\nwant\n%s", call.body, want)
	}

	for name, value := range map[string]string{
		"Authorization": "Bearer key",
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"User-Agent":    "go-systemone",
		"X-Extra":       "1",
	} {
		if got := call.header.Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}

	if got := call.header.Get(retryCountHeader); got != "" {
		t.Errorf("first attempt carries retry count %q", got)
	}
}

func TestSystemOneRequestOverridesModel(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, okBody)
	})
	client := newClient(t, server.URL)

	_, err := client.SystemOne(context.Background(), Request{State: "text", Model: "nimble", Questions: map[string]Question{"q": Noul{}}})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	want := `{"state":"text","model":"nimble","questions":{"q":{"type":"noul"}}}`
	if got := calls.all()[0].body; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestSystemOneSendsEmptyChoiceCriteria(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, okBody)
	})

	_, err := newClient(t, server.URL).SystemOne(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Choice{}}})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	want := `{"state":"x","model":"jev-latest","questions":{"q":{"type":"choice","criteria":{}}}}`
	if got := calls.all()[0].body; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestSystemOneOmitsAuthorizationWithoutKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "")

	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, okBody)
	})

	client, err := New(WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.SystemOne(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul{}}}); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if got := calls.all()[0].header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestSystemOneDecodesAnswers(t *testing.T) {
	server, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("X-Typesafe-Request-Id", "req-1")
		writeJSON(w, http.StatusOK, `{
			"model": "jev-1.13.0",
			"answers": {
				"bait": {"type": "noul", "noul": 0.97},
				"intent": {"type": "choice", "choice": "bug", "probabilities": {"bug": 0.9, "praise": 0.1}, "confidence": 0.8},
				"hook": {"type": "score", "score": 1.87, "legend": {"0": "none", "1": {"text": "ok"}, "2": "strong"},
					"probabilities": {"0": 0.01, "1": 0.11, "2": 0.88}, "confidence": 0.86},
				"future": {"type": "ranking", "order": ["a"]}
			},
			"usage": {"input_tokens": 84}
		}`)
	})
	client := newClient(t, server.URL)

	resp, err := client.SystemOne(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul{}}})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if resp.Model != "jev-1.13.0" || resp.RequestID != "req-1" {
		t.Errorf("model, request ID = %q, %q", resp.Model, resp.RequestID)
	}

	if resp.Usage.InputTokens == nil || *resp.Usage.InputTokens != 84 || resp.Usage.OutputTokens != nil {
		t.Errorf("usage = %+v, want input 84 and no output", resp.Usage)
	}

	if bait, ok := resp.Noul("bait"); !ok || bait.Noul != 0.97 {
		t.Errorf("bait = %+v, %v", bait, ok)
	}

	intent, ok := resp.Choice("intent")
	if !ok || intent.Choice != "bug" || intent.Confidence != 0.8 || intent.Probabilities["praise"] != 0.1 {
		t.Errorf("intent = %+v, %v", intent, ok)
	}

	hook, ok := resp.Score("hook")
	if !ok || hook.Score != 1.87 || hook.Probabilities[2] != 0.88 || hook.Legend[0] != "none" {
		t.Errorf("hook = %+v, %v", hook, ok)
	}

	if legend, isObject := hook.Legend[1].(map[string]any); !isObject || legend["text"] != "ok" {
		t.Errorf("hook legend 1 = %#v", hook.Legend[1])
	}

	future, ok := resp.Answers["future"].(UnknownAnswer)
	if !ok || future.Type != "ranking" || !json.Valid(future.Raw) {
		t.Errorf("future = %#v", resp.Answers["future"])
	}

	if _, ok := resp.Choice("bait"); ok {
		t.Error("Choice returned a noul answer")
	}
}

func TestSystemOneRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		path string
	}{
		{"not json", `nope`, ""},
		{"missing usage", `{"model":"m","answers":{}}`, "usage"},
		{"null answers", `{"model":"m","answers":null,"usage":{}}`, "answers"},
		{"missing type", `{"model":"m","answers":{"a":{"noul":0.5}},"usage":{}}`, "answers.a.type"},
		{"missing confidence", `{"model":"m","answers":{"a":{"type":"choice","choice":"x","probabilities":{}}},"usage":{}}`, "answers.a.confidence"},
		{"missing legend", `{"model":"m","answers":{"a":{"type":"score","score":1,"confidence":1,"probabilities":{}}},"usage":{}}`, "answers.a.legend"},
		{"wrong noul type", `{"model":"m","answers":{"a":{"type":"noul","noul":"yes"}},"usage":{}}`, "answers.a"},
		{"object type", `{"model":"m","answers":{"a":{"type":{"secret":"body"}}},"usage":{}}`, "answers.a.type"},
		{"null choice probability", `{"model":"m","answers":{"a":{"type":"choice","choice":"x","confidence":1,"probabilities":{"x":null}}},"usage":{}}`,
			"answers.a.probabilities"},
		{"null score probability", `{"model":"m","answers":{"a":{"type":"score","score":1,"confidence":1,"legend":{"0":"a"},"probabilities":{"0":null}}},"usage":{}}`,
			"answers.a.probabilities"},
		{"null legend entry", `{"model":"m","answers":{"a":{"type":"score","score":1,"confidence":1,"legend":{"0":null},"probabilities":{"0":1}}},"usage":{}}`,
			"answers.a.legend"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				writeJSON(w, http.StatusOK, tt.body)
			})

			_, err := newClient(t, server.URL).SystemOne(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul{}}})
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err = %v, want ErrInvalidResponse", err)
			}

			if tt.path != "" && !strings.Contains(err.Error(), "at "+tt.path+":") {
				t.Errorf("err = %v, want path %s", err, tt.path)
			}

			if strings.Contains(err.Error(), "secret") {
				t.Errorf("err = %v, echoes the response body", err)
			}
		})
	}
}

func TestSystemOneValidatesRequest(t *testing.T) {
	var nilScore *Score

	tests := []struct {
		name string
		req  Request
	}{
		{"no questions", Request{State: "x"}},
		{"nil question", Request{State: "x", Questions: map[string]Question{"q": nil}}},
		{"nil pointer question", Request{State: "x", Questions: map[string]Question{"q": nilScore}}},
		{"repeated label", Request{State: "x", Questions: map[string]Question{"q": Choice{Criteria: []Criterion{{Label: "a"}, {Label: "a"}}}}}},
		{"empty score", Request{State: "x", Questions: map[string]Question{"q": Score{}}}},
		{"nil state", Request{Questions: map[string]Question{"q": Noul{}}}},
		{"number state", Request{State: 3, Questions: map[string]Question{"q": Noul{}}}},
		{"unencodable state", Request{State: func() {}, Questions: map[string]Question{"q": Noul{}}}},
		{"unencodable instructions", Request{State: "x", Questions: map[string]Question{"q": Noul{Instructions: make(chan int)}}}},
	}

	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, okBody)
	})
	client := newClient(t, server.URL)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := client.SystemOne(context.Background(), tt.req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}

	if len(calls.all()) != 0 {
		t.Errorf("invalid requests reached the server %d times", len(calls.all()))
	}
}

func TestAPIError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"error string", 401, `{"error":"invalid key"}`, "invalid key"},
		{"error object", 403, `{"error":{"message":"no access"}}`, "no access"},
		{"message", 404, `{"message":"not found"}`, "not found"},
		{"detail string", 400, `{"detail":"bad"}`, "bad"},
		{"detail object", 400, `{"detail":{"message":"worse"}}`, "worse"},
		{"validation list", 422, `{"detail":[{"loc":["body","questions","tone","criteria"],"msg":"Field required"},{"msg":"second"},3]}`,
			"questions.tone.criteria: Field required; second"},
		{"json string", 500, `"boom"`, "boom"},
		{"plain text", 502, "bad gateway\n", "bad gateway"},
		{"unknown object", 500, `{"code":7}`, `{"code":7}`},
		{"long text", 500, strings.Repeat("é", 250), strings.Repeat("é", 200) + "…"},
		{"empty", 503, ``, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("X-Typesafe-Request-Id", "req-9")
				writeJSON(w, tt.status, tt.body)
			})

			_, err := newClient(t, server.URL).ListModels(context.Background())

			apiErr, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("err = %v, want *APIError", err)
			}

			if apiErr.StatusCode != tt.status || apiErr.Message != tt.message || apiErr.RequestID != "req-9" {
				t.Errorf("APIError = %d %q %q, want %d %q req-9", apiErr.StatusCode, apiErr.Message, apiErr.RequestID, tt.status, tt.message)
			}

			if !strings.HasPrefix(err.Error(), "systemone: GET "+server.URL+"/v1/models: ") || !strings.HasSuffix(err.Error(), "(request_id=req-9)") {
				t.Errorf("Error() = %q", err.Error())
			}
		})
	}
}

func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})

	_, err := newClient(t, server.URL).ListModels(context.Background())
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v, want APIError 307", err)
	}

	if len(calls.all()) != 1 {
		t.Errorf("calls = %d, want 1", len(calls.all()))
	}
}

func TestListModels(t *testing.T) {
	server, calls := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, `{"models":[{"name":"jev-1.13.0","description":"Jev","release_date":"2026-09-15"}]}`)
	})

	models, err := newClient(t, server.URL).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	want := Model{Name: "jev-1.13.0", Description: "Jev", ReleaseDate: "2026-09-15"}
	if len(models) != 1 || models[0] != want {
		t.Errorf("models = %+v, want [%+v]", models, want)
	}

	call := calls.all()[0]
	if call.method != http.MethodGet || call.path != "/v1/models" || call.body != "" || call.header.Get("Content-Type") != "" {
		t.Errorf("request = %s %s body %q content type %q", call.method, call.path, call.body, call.header.Get("Content-Type"))
	}
}

func TestListModelsRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		path string
	}{
		{"missing models", `{}`, "models"},
		{"empty model", `{"models":[{}]}`, "models.0.name"},
		{"null model", `{"models":[{"name":"a","description":"b","release_date":"c"},null]}`, "models.1.name"},
		{"missing release date", `{"models":[{"name":"a","description":"b"}]}`, "models.0.release_date"},
		{"wrong name type", `{"models":[{"name":1,"description":"b","release_date":"c"}]}`, "models.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				writeJSON(w, http.StatusOK, tt.body)
			})

			_, err := newClient(t, server.URL).ListModels(context.Background())
			if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "at "+tt.path+":") {
				t.Fatalf("err = %v, want ErrInvalidResponse at %s", err, tt.path)
			}
		})
	}
}

func TestNewConfiguration(t *testing.T) {
	t.Setenv(EnvAPIKey, " env-key ")
	t.Setenv(EnvBaseURL, "http://env.example")
	t.Setenv(EnvDefaultModel, "  ")

	client, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if client.apiKey != "env-key" || client.baseURL != "http://env.example" || client.model != DefaultModel {
		t.Errorf("from env: key %q base %q model %q", client.apiKey, client.baseURL, client.model)
	}

	client, err = New(WithAPIKey("opt-key"), WithBaseURL("https://opt.example/"), WithModel("opt-model"), WithHeader("X-Tab", "a\tb"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if client.apiKey != "opt-key" || client.baseURL != "https://opt.example" || client.model != "opt-model" {
		t.Errorf("from options: key %q base %q model %q", client.apiKey, client.baseURL, client.model)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{"relative base URL", []Option{WithBaseURL("api.typesafe.ai")}},
		{"ftp base URL", []Option{WithBaseURL("ftp://api.typesafe.ai")}},
		{"credentials in base URL", []Option{WithBaseURL("https://user:pass@api.typesafe.ai")}},
		{"query in base URL", []Option{WithBaseURL("https://api.typesafe.ai?x=1")}},
		{"empty model", []Option{WithModel("")}},
		{"negative retries", []Option{WithRetryPolicy(RetryPolicy{MaxRetries: -1})}},
		{"negative backoff", []Option{WithRetryPolicy(RetryPolicy{BackoffInitial: -time.Second})}},
		{"jitter above one", []Option{WithRetryPolicy(RetryPolicy{BackoffJitter: 1.5})}},
		{"negative budget", []Option{WithRetryPolicy(RetryPolicy{Budget: -time.Second})}},
		{"API key with newline", []Option{WithAPIKey("key\n")}},
		{"header name with space", []Option{WithHeader("X Bad", "1")}},
		{"empty header name", []Option{WithHeader("", "1")}},
		{"header value with newline", []Option{WithHeader("X-Ok", "a\r\nX-Injected: 1")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts...); err == nil {
				t.Error("New succeeded, want error")
			}
		})
	}
}
