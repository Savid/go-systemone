package systemone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

const (
	typeNoul   = "noul"
	typeChoice = "choice"
	typeScore  = "score"
)

// Question is one typed question: a Noul, a Choice, or a Score.
//
// Instructions and descriptions are JSON content: a string, a JSON object, or
// an array. A nil value is omitted.
type Question interface {
	questionType() string
	validate() error
}

// Noul asks whether a statement is true. It is answered by a NoulAnswer.
type Noul struct {
	// Instructions is the statement or question to judge.
	Instructions any
	// Criteria optionally describes the yes and no outcomes.
	Criteria *NoulCriteria
}

// NoulCriteria describes the outcomes of a Noul. A nil field leaves that
// outcome undescribed.
type NoulCriteria struct {
	True  any `json:"true,omitempty"`
	False any `json:"false,omitempty"`
}

// Choice selects one label. It is answered by a ChoiceAnswer.
type Choice struct {
	// Instructions is the question to ask.
	Instructions any
	// Criteria lists the labels in the order they are sent.
	Criteria []Criterion
}

// Criterion is one Choice label. A nil Description sends the label undescribed.
type Criterion struct {
	Label       string
	Description any
}

// Score places the state on an ordered rubric. It is answered by a
// ScoreAnswer.
type Score struct {
	// Instructions is the question to ask.
	Instructions any
	// Criteria holds one description per level, starting at level zero.
	Criteria []any
}

func (Noul) questionType() string   { return typeNoul }
func (Choice) questionType() string { return typeChoice }
func (Score) questionType() string  { return typeScore }

func (Noul) validate() error { return nil }

// validate rejects a repeated label, which the wire's JSON object cannot hold.
func (q Choice) validate() error {
	seen := make(map[string]bool, len(q.Criteria))
	for i, option := range q.Criteria {
		if seen[option.Label] {
			return fmt.Errorf("%w: choice criterion %d repeats an earlier label", ErrInvalidRequest, i)
		}

		seen[option.Label] = true
	}

	return nil
}

func (q Score) validate() error {
	if len(q.Criteria) == 0 {
		return fmt.Errorf("%w: score has no criteria", ErrInvalidRequest)
	}

	return nil
}

// MarshalJSON encodes the question with its type tag.
func (q Noul) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string        `json:"type"`
		Instructions any           `json:"instructions,omitempty"`
		Criteria     *NoulCriteria `json:"criteria,omitempty"`
	}{typeNoul, q.Instructions, q.Criteria})
}

// MarshalJSON encodes the question with its type tag and its criteria as one
// JSON object whose keys keep the Criteria order.
func (q Choice) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string          `json:"type"`
		Instructions any             `json:"instructions,omitempty"`
		Criteria     orderedCriteria `json:"criteria"`
	}{typeChoice, q.Instructions, q.Criteria})
}

// MarshalJSON encodes the question with its type tag.
func (q Score) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions,omitempty"`
		Criteria     []any  `json:"criteria"`
	}{typeScore, q.Instructions, q.Criteria})
}

type orderedCriteria []Criterion

func (c orderedCriteria) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteByte('{')

	for i, option := range c {
		if i > 0 {
			buf.WriteByte(',')
		}

		label, err := json.Marshal(option.Label)
		if err != nil {
			return nil, err
		}

		description, err := json.Marshal(option.Description)
		if err != nil {
			return nil, fmt.Errorf("criterion %d: %w", i, err)
		}

		buf.Write(label)
		buf.WriteByte(':')
		buf.Write(description)
	}

	buf.WriteByte('}')

	return buf.Bytes(), nil
}

func validateQuestions(questions map[string]Question) error {
	if len(questions) == 0 {
		return fmt.Errorf("%w: at least one question is required", ErrInvalidRequest)
	}

	for name, question := range questions {
		if isNil(question) {
			return fmt.Errorf("%w: question %q is nil", ErrInvalidRequest, name)
		}

		if err := question.validate(); err != nil {
			return fmt.Errorf("question %q: %w", name, err)
		}
	}

	return nil
}

func isNil(question Question) bool {
	if question == nil {
		return true
	}

	value := reflect.ValueOf(question)

	return value.Kind() == reflect.Pointer && value.IsNil()
}
