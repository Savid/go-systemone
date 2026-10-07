package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Response holds the answers to one System One request.
type Response struct {
	// Model is the model that answered.
	Model string
	// Answers maps each question name to its answer.
	Answers map[string]Answer
	// Usage reports the request's token counts.
	Usage Usage
	// RequestID is the x-typesafe-request-id header, empty when absent.
	RequestID string
}

// Usage reports token counts. A nil field was not reported.
type Usage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
}

// Answer is the answer to one question: a NoulAnswer, a ChoiceAnswer, a
// ScoreAnswer, or an UnknownAnswer.
type Answer interface {
	answerType() string
}

// NoulAnswer answers a Noul.
type NoulAnswer struct {
	// Noul is the probability of yes, from zero to one.
	Noul float64 `json:"noul"`
}

// ChoiceAnswer answers a Choice.
type ChoiceAnswer struct {
	// Choice is the selected label.
	Choice string `json:"choice"`
	// Confidence is the reported confidence in the selected label.
	Confidence float64 `json:"confidence"`
	// Probabilities maps each label to its probability.
	Probabilities map[string]float64 `json:"probabilities"`
}

// ScoreAnswer answers a Score.
type ScoreAnswer struct {
	// Score is the probability-weighted level, which may fall between levels.
	Score float64 `json:"score"`
	// Confidence is the reported confidence in the score.
	Confidence float64 `json:"confidence"`
	// Legend maps each level to its description.
	Legend map[int]any `json:"legend"`
	// Probabilities maps each level to its probability.
	Probabilities map[int]float64 `json:"probabilities"`
}

// UnknownAnswer carries an answer whose type this package does not model.
type UnknownAnswer struct {
	// Type is the answer's type tag.
	Type string
	// Raw is the answer's JSON object.
	Raw json.RawMessage
}

func (NoulAnswer) answerType() string      { return typeNoul }
func (ChoiceAnswer) answerType() string    { return typeChoice }
func (ScoreAnswer) answerType() string     { return typeScore }
func (a UnknownAnswer) answerType() string { return a.Type }

// Noul returns the named answer when it is a NoulAnswer.
func (r *Response) Noul(name string) (NoulAnswer, bool) {
	answer, ok := r.Answers[name].(NoulAnswer)

	return answer, ok
}

// Choice returns the named answer when it is a ChoiceAnswer.
func (r *Response) Choice(name string) (ChoiceAnswer, bool) {
	answer, ok := r.Answers[name].(ChoiceAnswer)

	return answer, ok
}

// Score returns the named answer when it is a ScoreAnswer.
func (r *Response) Score(name string) (ScoreAnswer, bool) {
	answer, ok := r.Answers[name].(ScoreAnswer)

	return answer, ok
}

// Model describes one model a server offers.
type Model struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

var null = []byte("null")

func decodeResponse(body []byte, requestID string) (*Response, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, invalidResponse("", err)
	}

	if err := requireFields(fields, "", "model", "answers", "usage"); err != nil {
		return nil, err
	}

	response := &Response{RequestID: requestID}
	if err := json.Unmarshal(fields["model"], &response.Model); err != nil {
		return nil, invalidResponse("model", err)
	}

	if err := json.Unmarshal(fields["usage"], &response.Usage); err != nil {
		return nil, invalidResponse("usage", err)
	}

	var answers map[string]json.RawMessage
	if err := json.Unmarshal(fields["answers"], &answers); err != nil {
		return nil, invalidResponse("answers", err)
	}

	response.Answers = make(map[string]Answer, len(answers))
	for name, raw := range answers {
		answer, err := decodeAnswer("answers."+name, raw)
		if err != nil {
			return nil, err
		}

		response.Answers[name] = answer
	}

	return response, nil
}

func decodeAnswer(path string, raw json.RawMessage) (Answer, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, invalidResponse(path, err)
	}

	var tag string
	if err := json.Unmarshal(fields["type"], &tag); err != nil || tag == "" {
		return nil, invalidResponse(path+".type", errors.New("missing or not a nonempty string"))
	}

	switch tag {
	case typeNoul:
		return decodeTyped[NoulAnswer](path, raw, fields, "noul")
	case typeChoice:
		if err := rejectNullEntries(fields, path, "probabilities"); err != nil {
			return nil, err
		}

		return decodeTyped[ChoiceAnswer](path, raw, fields, "choice", "confidence", "probabilities")
	case typeScore:
		if err := rejectNullEntries(fields, path, "legend", "probabilities"); err != nil {
			return nil, err
		}

		return decodeTyped[ScoreAnswer](path, raw, fields, "score", "confidence", "legend", "probabilities")
	default:
		return UnknownAnswer{Type: tag, Raw: bytes.Clone(raw)}, nil
	}
}

func decodeTyped[T Answer](path string, raw json.RawMessage, fields map[string]json.RawMessage, required ...string) (Answer, error) {
	if err := requireFields(fields, path, required...); err != nil {
		return nil, err
	}

	var answer T
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, invalidResponse(path, err)
	}

	return answer, nil
}

func requireFields(fields map[string]json.RawMessage, path string, names ...string) error {
	for _, name := range names {
		value, ok := fields[name]
		if !ok || bytes.Equal(value, null) {
			return invalidResponse(joinPath(path, name), fmt.Errorf("missing required field %q", name))
		}
	}

	return nil
}

// rejectNullEntries fails when a map field holds a null value, which would
// otherwise decode as zero or nil. A field that is absent or not an object is
// left to the typed decode.
func rejectNullEntries(fields map[string]json.RawMessage, path string, names ...string) error {
	for _, name := range names {
		var entries map[string]json.RawMessage
		if json.Unmarshal(fields[name], &entries) != nil {
			continue
		}

		for _, value := range entries {
			if bytes.Equal(value, null) {
				return invalidResponse(joinPath(path, name), errors.New("entry is null"))
			}
		}
	}

	return nil
}

// decodeModels decodes a /v1/models body, requiring every model field.
func decodeModels(body []byte) ([]Model, error) {
	var list struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, invalidResponse("", err)
	}

	if list.Models == nil {
		return nil, invalidResponse("models", errors.New(`missing required field "models"`))
	}

	models := make([]Model, len(list.Models))
	for i, raw := range list.Models {
		path := "models." + strconv.Itoa(i)

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, invalidResponse(path, err)
		}

		if err := requireFields(fields, path, "name", "description", "release_date"); err != nil {
			return nil, err
		}

		if err := json.Unmarshal(raw, &models[i]); err != nil {
			return nil, invalidResponse(path, err)
		}
	}

	return models, nil
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}

func invalidResponse(path string, err error) error {
	if path == "" {
		return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}

	return fmt.Errorf("%w at %s: %w", ErrInvalidResponse, path, err)
}
