package specsession

import (
	"context"

	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

// completerAdapter bridges modelclient's concrete types to the structurally
// identical interface ambiguity declares. The duplication is deliberate --
// ambiguity compiles and tests without modelclient -- so the seam has to be
// crossed somewhere, and here is better than in main.
type completerAdapter struct{ c *modelclient.Client }

func (a completerAdapter) Complete(ctx context.Context, req ambiguity.CompleteRequest) (*ambiguity.Completion, error) {
	msgs := make([]modelclient.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, modelclient.Message{Role: m.Role, Content: m.Content})
	}

	out, err := a.c.Complete(ctx, modelclient.CompleteRequest{
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		JSONObject:  req.JSONObject,
	})
	if err != nil {
		return nil, err
	}

	return &ambiguity.Completion{
		Text:  out.Text,
		Model: out.Model,
		Usage: ambiguity.Usage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			TotalTokens:      out.Usage.TotalTokens,
		},
		Raw: out.Raw,
	}, nil
}

// documentScreener adapts ambiguity.Screener, which screens a parsed
// *spec.Document, to the Screener interface this package's reconciler uses,
// which is handed the stored text.
type documentScreener struct{ s *ambiguity.Screener }

// Screen parses the stored specification and screens it.
//
// Parse problems are not returned: Parse always yields a usable document, and
// an incomplete specification is exactly what screening is for. The
// deterministic gate, not this path, is what refuses to promote one.
func (d documentScreener) Screen(ctx context.Context, content string) (*ambiguity.Report, error) {
	doc, _ := spec.Parse("", []byte(content))
	return d.s.Screen(ctx, doc)
}

// NewModelScreener builds the Screener the reconciler needs from a
// modelclient talking to an OpenAI-compatible endpoint.
func NewModelScreener(c *modelclient.Client, model string) Screener {
	return documentScreener{s: ambiguity.NewScreener(completerAdapter{c: c}, model)}
}
