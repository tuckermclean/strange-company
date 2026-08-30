package ui

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// artifactPage serves one artifact's contents.
//
// This is the click. Everything else on a card summarises; this is the thing
// itself -- the implementation plan the model wrote, the diff it produced, the
// reviewer's verdict, and the harness transcript, which is the raw discourse
// §21 keeps out of the summary.
//
// Rendered rather than downloaded, because the point is reading it. A page that
// hands you a file to open in another application has answered a different
// question than "what did it do".
func (h *Handler) artifactPage(w http.ResponseWriter, r *http.Request) {
	cardID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not a card id", http.StatusBadRequest)
		return
	}
	artifactID, err := uuid.Parse(r.PathValue("artifact"))
	if err != nil {
		http.Error(w, "not an artifact id", http.StatusBadRequest)
		return
	}

	artifacts, err := h.store.ListArtifacts(r.Context(), cardID)
	if err != nil {
		h.fail(w, "could not read the card's artifacts", err)
		return
	}

	var found *store.Artifact
	for _, a := range artifacts {
		if a.ID == artifactID {
			found = a
			break
		}
	}
	if found == nil {
		http.Error(w, "no such artifact on this card", http.StatusNotFound)
		return
	}

	// Raw, on request. Useful for piping a transcript somewhere else, and for
	// anyone who would rather read it in their own tools than in a browser.
	if r.URL.Query().Get("raw") != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(found.Content))
		return
	}

	h.render(w, "artifact.html", artifactPageView{
		CardID:    cardID.String(),
		ID:        artifactID.String(),
		Type:      found.Type,
		Actor:     found.Actor,
		Model:     found.Model,
		SizeBytes: found.SizeBytes,
		Truncated: found.Truncated,
		SHA256:    found.SHA256,
		// A model's own account of its work, shown where a reader has asked
		// for it and told what they are getting. §21 governs the card's
		// summary; it was never a rule that the evidence be unreadable.
		Unverified: found.Type == store.ArtifactRunLog,
		Content:    found.Content,
	})
}

type artifactPageView struct {
	CardID     string
	ID         string
	Type       string
	Actor      string
	Model      string
	SizeBytes  int64
	Truncated  bool
	SHA256     string
	Unverified bool
	Content    string
}

// evidenceView is a worker's account of one step (§12.2: evidence, not
// monologue).
type evidenceView struct {
	Summary string
	Actor   string
	Detail  string
}

func evidenceFrom(in []store.CardEvidence) []evidenceView {
	out := make([]evidenceView, 0, len(in))
	for _, e := range in {
		v := evidenceView{Summary: e.Summary, Actor: e.ActorID}
		if len(e.Detail) > 0 {
			var parts []string
			for k, val := range e.Detail {
				parts = append(parts, k+"="+strings.TrimSpace(strings.Trim(strings.TrimSpace(sprint(val)), `"`)))
			}
			v.Detail = strings.Join(parts, "  ")
		}
		out = append(out, v)
	}
	return out
}
