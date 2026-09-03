package ui

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// cardView is one card's whole story.
//
// Ordered as the work happened rather than as the database is shaped: what it
// is, what it promised, what each run cost and produced, and what it left
// behind. The runs are the centrepiece, because a run that failed and a run
// that could not start are the two things that tell a reader this system knows
// what it does not know.
type cardView struct {
	ID        string
	Title     string
	State     string
	Phase     string
	SourceURL string
	RepoURL   string
	Branch    string
	Worker    string

	CostUSD  float64
	Unpriced bool
	MaxCost  string

	Attempts  int
	InfraFail int

	Criteria  []criterionView
	Runs      []runView
	Artifacts []artifactGroup
	Workers   []workerStint
	Evidence  []evidenceView

	// SpecSession is the §10.2 conversation, when one is open.
	SpecSession    string
	SpecSessionURL string
	History   []historyView

	// CanApprove and friends decide which buttons render. A button that
	// cannot work is worse than a missing one: it invites a click that
	// produces an error the person cannot act on.
	CanApprove  bool
	CanAccept   bool
	CanBlock    bool
	CanSendBack bool

	// Parent is the work this card was split out of; Children are the
	// pieces it was split into. One of the two is empty for most cards and
	// both are for a card that was never decomposed.
	Parent   *lineageView
	Children []lineageView

	// Prerequisites are the cards this one waits for, in order, each
	// carrying whether it is finished.
	Prerequisites []prerequisiteView

	// WaitingFor is the first unfinished one, when there is one.
	WaitingFor string

	// Waiting says, in a sentence, what this card wants from the person
	// reading it. A board that shows a stopped card and not why it stopped
	// makes the reader reconstruct the state machine from the buttons.
	Waiting string

	// Error carries the state machine's own words when a move was refused.
	Error string
}

type lineageView struct {
	ID, Title, State string
}

type prerequisiteView struct {
	ID, Title, State string
	Met              bool
}

type criterionView struct {
	ID, Text, Verification string
}

type runView struct {
	Phase    string
	Model    string
	Harness  string
	Status   string
	Counted  bool
	Summary  string
	CostUSD  string
	Tokens   string
	Duration string
	When     string
	Failed   bool
	Infra    bool
}

// artifactGroup collapses repeats of one type.
//
// A retry loop can leave hundreds of artifacts of the same kind on one card --
// 262 on a real one -- and rendering a row each buries everything else on the
// page. The newest is the one worth reading; the rest are counted.
type artifactGroup struct {
	Type    string
	Count   int
	Newest  artifactView
	Others  []artifactView
	Repeats bool
}

type artifactView struct {
	ID        string
	Type      string
	Size      int64
	Truncated bool
	When      string
}

// workerStint is one Meeseeks' whole life: which phase it took, and how long it
// existed.
//
// §7.1 gives each phase its own worker -- claim, one step, release, exit -- and
// the card page rendered every one of them as "agent", which flattens the most
// distinctive thing this architecture does into a word. Derived from history
// rather than stored: the actor id is already on every transition.
//
// The phase is deliberately not carried here. It is not reliably derivable
// from a transition row -- "phase advanced to X" names where the card is
// GOING, not what this worker just did -- and a field that is sometimes wrong
// is worse than one that does not exist. The history below says what each one
// did, in its own words.
type workerStint struct {
	Worker string
	From   string
	To     string
}

type historyView struct {
	When    string
	From    string
	To      string
	Actor   string
	ActorID string
	Reason  string
}

func (h *Handler) cardPage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not a card id", http.StatusBadRequest)
		return
	}

	c, err := h.store.GetCard(r.Context(), id)
	if err != nil {
		http.Error(w, "no such card", http.StatusNotFound)
		return
	}

	v := cardView{
		ID: c.ID.String(), Title: c.Title,
		State: string(c.State), Phase: string(c.Phase),
		CostUSD: c.CostUSD, Unpriced: c.CostUSD <= 0,
		Attempts: c.ImplementationAttempt, InfraFail: c.InfrastructureFailures,
	}
	if c.SourceURL != nil {
		v.SourceURL = *c.SourceURL
	}
	if c.RepoURL != nil {
		v.RepoURL = *c.RepoURL
	}
	if c.Branch != nil {
		v.Branch = *c.Branch
	}
	if c.ClaimedBy != nil {
		v.Worker = shortWorker(*c.ClaimedBy)
	}
	if c.MaxCostUSD != nil {
		v.MaxCost = money(*c.MaxCostUSD)
	}

	// §19 and the state machine decide these, not the template. Asking
	// CanTransition is what keeps the buttons honest as the rules change.
	v.CanBlock = card.CanTransition(c.State, card.Blocked, card.ActorHuman) == nil
	v.CanSendBack = card.CanTransition(c.State, card.Ready, card.ActorHuman) == nil
	v.CanAccept = card.CanTransition(c.State, card.Done, card.ActorHuman) == nil

	if cardSpec, err := h.store.GetSpec(r.Context(), id); err == nil && cardSpec != nil {
		if strings.TrimSpace(cardSpec.Content) != "" {
			doc, _ := spec.Parse(id.String(), []byte(cardSpec.Content))
			if doc != nil {
				for _, cr := range doc.Criteria {
					v.Criteria = append(v.Criteria, criterionView{ID: cr.ID, Text: cr.Text, Verification: cr.Verification})
				}
			}
			v.CanApprove = !cardSpec.Approved && c.State == card.Backlog
		}
	}

	// Lineage, from the edges decomposition already writes. Never fatal:
	// a card that cannot show its family is still a card.
	if parentOf, err := h.store.Parentage(r.Context()); err == nil {
		if p, ok := parentOf[id]; ok {
			if parent, err := h.store.GetCard(r.Context(), p); err == nil && parent != nil {
				v.Parent = &lineageView{ID: p.String(), Title: parent.Title, State: string(parent.State)}
			}
		}
		var kids []*card.Card
		for child, parent := range parentOf {
			if parent != id {
				continue
			}
			if c, err := h.store.GetCard(r.Context(), child); err == nil && c != nil {
				kids = append(kids, c)
			}
		}
		// Build order, which is creation order: decomposition chains each
		// piece to the one before it as it writes them. Sorting by title
		// would put them in an order nothing builds them in, under a
		// heading that says otherwise.
		sort.Slice(kids, func(i, j int) bool { return kids[i].CreatedAt.Before(kids[j].CreatedAt) })
		for _, c := range kids {
			v.Children = append(v.Children, lineageView{
				ID: c.ID.String(), Title: c.Title, State: string(c.State),
			})
		}
	}

	if prereqs, err := h.store.Prerequisites(r.Context()); err == nil {
		for _, p := range prereqs[id] {
			v.Prerequisites = append(v.Prerequisites, prerequisiteView{
				ID: p.ID.String(), Title: p.Title, State: p.State, Met: p.Met(),
			})
		}
		v.WaitingFor = blockedBy(prereqs[id])
	}

	// After the spec block: Backlog only wants a person when there is in fact
	// a specification sitting unapproved.
	v.Waiting = waitingOn(c.State)
	if c.State == card.Backlog && !v.CanApprove {
		v.Waiting = ""
	}
	if v.WaitingFor != "" {
		// §10's gate will refuse to promote this card whatever a person
		// does here, so saying so beats a button that appears to do
		// nothing.
		v.Waiting = "This card cannot start until \"" + v.WaitingFor + "\" is done. " +
			"Approving or sending it back now will not move it: the gate holds it until then."
		v.CanApprove = false
		v.CanSendBack = false
	}

	if attempts, err := h.store.ListAttempts(r.Context(), id); err == nil {
		for _, a := range attempts {
			v.Runs = append(v.Runs, runFrom(a))
		}
	}
	if artifacts, err := h.store.ListArtifacts(r.Context(), id); err == nil {
		v.Artifacts = groupArtifacts(artifacts)
	}
	if session, err := h.store.GetSpecSession(r.Context(), id); err == nil && session != "" {
		v.SpecSession = session
		if h.dashboard != "" {
			v.SpecSessionURL = h.dashboard + "/sessions/" + session
		}
	}

	// §12.2's evidence: what each worker said it did, which is the narrative
	// the history's one-line reasons compress.
	if evidence, err := h.store.ListEvidence(r.Context(), id); err == nil {
		v.Evidence = evidenceFrom(evidence)
	}

	if history, err := h.store.ListHistory(r.Context(), id, 200); err == nil {
		for _, e := range history {
			v.History = append(v.History, historyView{
				When: e.At.UTC().Format("15:04:05Z"), From: e.From, To: e.To,
				Actor: e.ActorType, ActorID: shortWorker(e.ActorID), Reason: e.Reason,
			})
		}
		v.Workers = relay(history)
	}

	v.Error = r.URL.Query().Get("error")
	h.render(w, "card.html", v)
}

func runFrom(a store.StoredAttempt) runView {
	r := runView{
		Phase: a.Phase, Model: a.Model, Harness: a.Harness,
		Status: string(a.Status), Counted: a.CountedAsAttempt,
		Summary:  a.Summary,
		Duration: (time.Duration(a.DurationMS) * time.Millisecond).Truncate(time.Second).String(),
		When:     a.CreatedAt.UTC().Format("15:04:05Z"),
	}

	// The two states worth showing loudly. A run that failed on the merits
	// spent a rung of the ladder; one that could not start spent nothing and
	// says something is wrong outside the model. Collapsing them into "error"
	// is what makes an autonomous system look unreliable when it is being
	// careful.
	switch a.Status {
	case "failed":
		r.Failed = true
	case "infra_error", "timeout":
		r.Infra = true
	}

	if a.CostUSD != nil {
		r.CostUSD = money(*a.CostUSD)
	} else {
		r.CostUSD = "unpriced"
	}
	if a.InputTokens > 0 || a.OutputTokens > 0 {
		r.Tokens = formatTokens(a.InputTokens, a.OutputTokens)
	}
	return r
}

// groupArtifacts collapses repeats of one type, newest first within each group.
//
// ListArtifacts returns oldest-first, so the last of a run is the newest.
func groupArtifacts(in []*store.Artifact) []artifactGroup {
	order := []string{}
	byType := map[string][]artifactView{}

	for _, a := range in {
		if _, seen := byType[a.Type]; !seen {
			order = append(order, a.Type)
		}
		byType[a.Type] = append(byType[a.Type], artifactView{
			ID: a.ID.String(), Type: a.Type, Size: a.SizeBytes, Truncated: a.Truncated,
		})
	}

	out := make([]artifactGroup, 0, len(order))
	for _, t := range order {
		views := byType[t]
		g := artifactGroup{Type: t, Count: len(views), Newest: views[len(views)-1]}
		if len(views) > 1 {
			g.Repeats = true
			// Newest first, and the newest itself is already shown above.
			for i := len(views) - 2; i >= 0; i-- {
				g.Others = append(g.Others, views[i])
			}
		}
		out = append(out, g)
	}
	return out
}

// relay reconstructs the chain of Meeseeks that carried this card.
//
// Each worker claims, does one step and exits, so its life is bounded by the
// first and last history rows naming it. Consecutive rows from one actor
// collapse into a single stint; a worker that appears again later gets another.
func relay(history []store.HistoryEntry) []workerStint {
	var out []workerStint
	for _, e := range history {
		if !strings.HasPrefix(e.ActorID, "meeseeks-") {
			continue
		}
		short := shortWorker(e.ActorID)
		when := e.At.UTC().Format("15:04:05Z")

		if n := len(out); n > 0 && out[n-1].Worker == short {
			out[n-1].To = when
			continue
		}
		out = append(out, workerStint{Worker: short, From: when, To: when})
	}
	return out
}

// waitingOn names what the card wants from a person, or nothing when it is
// the engine's turn.
//
// §18 and §19 put a human at exactly two points -- approving a specification
// and accepting finished work -- and a console that shows a card parked at one
// of them without saying so is asking the reader to infer the state machine
// from which buttons happen to be lit.
func waitingOn(s card.State) string {
	switch s {
	case card.Review:
		return "This card is waiting on you. Automated review cannot mark work done (§18) " +
			"and the final call on merging is yours (§19): accept it, or send it back with " +
			"what needs changing."
	case card.NeedsHuman:
		return "The engine escalated this card to you and will not pick it up again on its own. " +
			"Read the latest result below, fix whatever caused it, then send it back to the queue."
	case card.Blocked:
		return "This card is stopped and no worker will claim it. Send it back to the queue to resume."
	case card.Backlog:
		return "This card needs an approved specification before any worker can claim it."
	case card.Done:
		return ""
	default:
		return ""
	}
}

// nextAction is the reader's move, in the imperative and short enough for a
// board row.
//
// The state name answers "what is this card"; it does not answer "what am I
// for". A row reading "Review" tells a person the machine's vocabulary and
// leaves them to infer that Review means someone must decide -- which is a
// thing you learn by reading the state machine, not by looking at a board.
func nextAction(s card.State) string {
	switch s {
	case card.Review:
		return "Accept it, or send it back"
	case card.NeedsHuman:
		return "Read the result, then send it back"
	case card.Blocked:
		return "Send it back to resume"
	default:
		return ""
	}
}
