# go-systemone

A Go client for the System One API: send a piece of state and typed
questions, get back typed answers with calibrated probabilities. The types
mirror the wire format, so any server that implements `/v1/systemone` is
reachable by setting its base URL. Standard library only.

```sh
go get github.com/savid/go-systemone
```

## Usage

```go
client, err := systemone.New() // TYPESAFE_API_KEY, TYPESAFE_BASE_URL, TYPESAFE_DEFAULT_MODEL
if err != nil {
    return err
}

resp, err := client.SystemOne(ctx, systemone.Request{
    State: "I was charged twice. Please help.",
    Questions: map[string]systemone.Question{
        "billing": systemone.Noul{Instructions: "Is this about billing?"},
        "tone": systemone.Choice{
            Instructions: "What is the tone?",
            Criteria:     []systemone.Criterion{{Label: "calm"}, {Label: "angry"}},
        },
        "urgency": systemone.Score{
            Instructions: "How urgent is this?",
            Criteria:     []any{"can wait", "today", "right now"},
        },
    },
})
if err != nil {
    return err
}

billing, _ := resp.Noul("billing")    // billing.Noul: probability of yes
tone, _ := resp.Choice("tone")        // tone.Choice, tone.Probabilities, tone.Confidence
urgency, _ := resp.Score("urgency")   // urgency.Score: weighted level from 0
```

`State`, instructions, and descriptions accept a string, a JSON object, or an
array. Choice criteria keep their order on the wire.

## Questions and Answers

| Question | Answer fields |
|---|---|
| `Noul` — is the statement true? Optional `Criteria` describes yes and no | `Noul` |
| `Choice` — which label? | `Choice`, `Probabilities` by label, `Confidence` |
| `Score` — which level of an ordered rubric? | `Score`, `Legend` and `Probabilities` by level, `Confidence` |

An answer type the package does not model arrives as `UnknownAnswer` with its
raw JSON. A successful response missing a required field fails with
`ErrInvalidResponse` naming the field.

## Configuration

| Option | Default |
|---|---|
| `WithAPIKey` | `TYPESAFE_API_KEY`; no `Authorization` header when empty |
| `WithBaseURL` | `TYPESAFE_BASE_URL`, else `https://api.typesafe.ai` |
| `WithModel` | `TYPESAFE_DEFAULT_MODEL`, else `jev-latest` |
| `WithHTTPClient` | 10 s per attempt, redirects not followed |
| `WithRetryPolicy` | `DefaultRetryPolicy()`: 2 retries, 0.5–5 s backoff, 30 s budget |
| `WithHeader` | none; authentication, content negotiation, and `User-Agent` are reserved |

Options take precedence over the environment; blank variables are ignored.
Retries cover connection failures, timeouts, and HTTP 408, 429, and 5xx, and
honor `retry-after-ms` and `Retry-After`.

## Errors

- `ErrInvalidRequest`: the request was rejected before sending.
- `*APIError`: the server answered with a non-2xx status; carries the status,
  the server's message, the raw body, the request ID, and any `Retry-After`.
- `ErrInvalidResponse`: a 2xx body does not match the wire format.

## Development

```sh
make test    # race, shuffled
make lint    # pinned golangci-lint
make audit   # fmt-check lint build coverage-check tidy vuln modernize-check
```

## License

MIT
