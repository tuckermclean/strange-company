package modelclient

import (
	"fmt"
	"strings"
)

// Transcript renders one model call as a readable document: every message that
// was sent, then what came back.
//
// This is the question, which nothing stored. A stored verdict with no record
// of its input cannot be checked -- §18's claim is that the reviewer saw the
// diff, and the only way anyone can confirm that is to read what it was given.
//
// Deliberately produced here rather than in each step: the shape of a call is
// this package's business, and a step assembling its own transcript would drift
// from what was actually sent the first time the request gained a field.
func Transcript(req CompleteRequest, c *Completion, callErr error) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Request\n\nmax_tokens: %d\n", req.MaxTokens)
	if req.Timeout > 0 {
		fmt.Fprintf(&b, "timeout: %s\n", req.Timeout)
	}
	if req.IdleTimeout > 0 {
		fmt.Fprintf(&b, "idle timeout: %s\n", req.IdleTimeout)
	}

	for _, m := range req.Messages {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", m.Role, m.Content)
	}

	b.WriteString("\n# Response\n\n")
	switch {
	case callErr != nil:
		// The failure belongs in the transcript. A call that produced no
		// answer is exactly the one somebody will come looking for.
		fmt.Fprintf(&b, "The call did not produce an answer: %v\n", callErr)
	case c == nil:
		b.WriteString("No completion and no error, which should not happen.\n")
	default:
		if c.Model != "" {
			fmt.Fprintf(&b, "model: %s\n", c.Model)
		}
		fmt.Fprintf(&b, "tokens: %d in / %d out\n\n%s\n",
			c.Usage.PromptTokens, c.Usage.CompletionTokens, c.Text)
	}

	return b.String()
}
