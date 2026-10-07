# AGENTS.md

## Purpose

This Go module is a client for the System One API (`POST /v1/systemone`,
`GET /v1/models`). It mirrors the wire format and nothing else: it does not
translate to other decision APIs, add a provider abstraction, or choose
models.

## Project Map

- `client.go`: `Client`, options, environment resolution, transport, and the
  two endpoints.
- `question.go`: `Noul`, `Choice`, `Score`, their wire encoding, and request
  validation.
- `answer.go`: `Response`, answer types, `Model`, and strict response
  decoding.
- `errors.go`: `APIError`, the sentinel errors, and server message extraction.
- `retry.go`: `RetryPolicy`, backoff, and `Retry-After` parsing.

## Commands

```sh
make build
make test
make lint
make audit
```

`make test` runs with race detection and shuffled order. `make audit` is the
full local gate.

## Wire Fidelity

- The wire truth is TypeSafe's published OpenAPI schema
  (`https://api.typesafe.ai/openapi.json`) and its official SDK. Verify a wire
  claim there before changing a type, and use web search for current
  behavior.
- Field names, tags, and paths match the wire exactly. A Go convenience never
  changes what is sent or accepted.
- Required response fields fail decoding with `ErrInvalidResponse` and their
  path; a missing number never reads as zero. Unknown answer types survive as
  `UnknownAnswer`.
- Validate on the client only what is certain to fail on the server or
  what the wire cannot express.

## Coding Rules

- Standard library only: `go.mod` has no requirements, and the `go`
  directive is the oldest release whose APIs the code uses. Tools run pinned
  through `go run` in the Makefile.
- Follow Go idioms: `ctx` first, `%w` for wrapped errors, sentinels for
  `errors.Is`, `*APIError` for `errors.As`.
- Never log or embed state, question content, or response bodies in error
  strings beyond `APIError.Message`. Question names and field paths are
  allowed.
- A comment states what the code does or why a constraint exists.
- Tests use `httptest` and never reach a real server.

## Verification

Run `make test` and `make lint` for Go changes and `make audit` once they
settle. Review the complete diff against the wire schema.
