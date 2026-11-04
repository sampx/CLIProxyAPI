package responses

import (
	"strings"
	"testing"
)

func TestMissingReasoningIsNeverInvented(t *testing.T) {
	for _, body := range []string{
		`{"reasoning":{"effort":"high"},"input":[{"type":"reasoning","summary":[]},{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
		`{"reasoning":{"effort":"high"},"input":[{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
	} {
		out, err := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("small", []byte(body), false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "[reasoning unavailable]") {
			t.Fatalf("invented reasoning: %s", out)
		}
	}
}
