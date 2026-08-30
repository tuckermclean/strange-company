package vikunja

import (
	"fmt"
	"path"
	"strings"

	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// extensions gives each artifact type a filename a browser and an editor will
// both open sensibly. Anything unlisted falls back to .txt, which is honest.
var extensions = map[string]string{
	"text/x-diff":      ".patch",
	"text/markdown":    ".md",
	"application/json": ".json",
	"text/plain":       ".txt",
}

// attachmentName is an artifact's filename on the Vikunja task.
//
// Deterministic, and carrying enough identity to be unique per artifact: the
// whole idempotence story is "a name already attached is already done".
// Artifacts are immutable, so this is sound -- but only while the name is
// stable across reconcile passes. A name containing anything that varies (a
// timestamp, a UUID minted at render time) would re-upload the same file every
// tick and grow the operator's storage without bound.
func attachmentName(a *store.Artifact) string {
	base := a.Type
	if base == "" {
		base = "artifact"
	}

	// The artifact id is the only thing guaranteed unique when a card has
	// several of one type -- three implementation run logs, say. Short form
	// keeps the name readable; collisions across 8 hex characters within one
	// card are not a real risk.
	short := strings.SplitN(a.ID.String(), "-", 2)[0]

	ext, ok := extensions[a.ContentType]
	if !ok {
		ext = ".txt"
	}

	return fmt.Sprintf("%s-%s%s", path.Base(base), short, ext)
}
