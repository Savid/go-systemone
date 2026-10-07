package systemone_test

import (
	"context"
	"fmt"
	"log"

	systemone "github.com/savid/go-systemone"
)

func Example() {
	client, err := systemone.New() // reads TYPESAFE_API_KEY
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.SystemOne(context.Background(), systemone.Request{
		State: "I was charged twice. Please help.",
		Questions: map[string]systemone.Question{
			"billing": systemone.Noul{Instructions: "Is this about billing?"},
			"tone": systemone.Choice{
				Instructions: "What is the tone?",
				Criteria:     []systemone.Criterion{{Label: "calm"}, {Label: "angry"}},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	billing, _ := resp.Noul("billing")
	tone, _ := resp.Choice("tone")
	fmt.Printf("billing %.2f, tone %s (confidence %.2f)\n", billing.Noul, tone.Choice, tone.Confidence)
}
