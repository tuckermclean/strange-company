package ui

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// uiActor is what §21's audit records for something done through this UI.
//
// Not a person's name: authentication happens at the ingress and this process
// never learns who was on the other end. Recording "human" alone would be a
// half-truth, and inventing a name would be a whole one -- so the audit says
// exactly what is known, which is that a person used the console.
const uiActor = "human:console"

// approve records a human approval of the card's specification (§10.2).
func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	id, ok := h.mutating(w, r)
	if !ok {
		return
	}
	if err := h.store.ApproveSpec(r.Context(), id, uiActor); err != nil {
		h.redirectWithError(w, r, id, err.Error())
		return
	}
	h.backToCard(w, r, id)
}

// block stops a card without a redeploy (see docs/reference/stopping-a-card.md).
func (h *Handler) block(w http.ResponseWriter, r *http.Request) {
	id, ok := h.mutating(w, r)
	if !ok {
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "stopped from the console"
	}
	h.move(w, r, id, card.Blocked, reason)
}

// sendBack returns a card to the queue. Leaving NeedsHuman also clears the
// consecutive infrastructure-failure count, so a card escalated for a cause
// that has since been fixed actually gets retried.
func (h *Handler) sendBack(w http.ResponseWriter, r *http.Request) {
	id, ok := h.mutating(w, r)
	if !ok {
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "sent back from the console"
	}
	h.move(w, r, id, card.Ready, reason)
}

// accept is §19's final merge authority: the human says the finished work is
// good and the card reaches Done.
//
// §18 reserves this for a person -- automated review can send a card to Review
// but never past it -- so the console has to offer it. Until it did, a card
// parked in Review could only be rejected or stopped, and the one act the
// state machine keeps for a human was the one act the UI could not perform.
func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := h.mutating(w, r)
	if !ok {
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		reason = "accepted from the console"
	}
	h.move(w, r, id, card.Done, reason)
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request, id uuid.UUID, to card.State, reason string) {
	// Validated by the state machine, never by the button that called it. A
	// UI that decided for itself what moves are legal would be a second
	// implementation of §4.3's rules, free to drift from the first.
	if err := h.store.Transition(r.Context(), id, to, card.ActorHuman, uiActor, reason); err != nil {
		h.redirectWithError(w, r, id, err.Error())
		return
	}
	h.backToCard(w, r, id)
}

// mutating parses the card id and refuses cross-site form posts.
//
// Authentication lives at the ingress, which means a browser that has been
// through it carries whatever the ingress issued on every request -- including
// requests a different site caused. An Origin check is the whole defence
// needed here and costs nothing: this UI has no cross-origin callers.
func (h *Handler) mutating(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return uuid.Nil, false
		}
	}

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not a card id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

// backToCard sends the browser back where it was, so an action leaves the
// person looking at its consequence rather than at a JSON blob.
func (h *Handler) backToCard(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	http.Redirect(w, r, "/ui/cards/"+id.String(), http.StatusSeeOther)
}

// redirectWithError shows the state machine's refusal on the card itself.
//
// An illegal move is not a server fault and must not read like one: §4.3
// rejecting a transition is the system working, and the person needs to see
// why on the page they were already on.
func (h *Handler) redirectWithError(w http.ResponseWriter, r *http.Request, id uuid.UUID, msg string) {
	http.Redirect(w, r, "/ui/cards/"+id.String()+"?error="+url.QueryEscape(msg), http.StatusSeeOther)
}
