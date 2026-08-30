// Package policy loads the control plane's provider, credential and
// model-routing configuration and answers the one question the rest of the
// system is allowed to ask a file instead of a model: "for this phase, on
// this attempt, which provider, which model, and which credentials?"
//
// Nothing in this package — or in any caller of it — may name a specific
// vendor. A provider is a harness (the binary that gets invoked), a
// credential set (environment variables sourced from Kubernetes Secrets the
// operator already created), and an optional base URL, all supplied by
// YAML. A model alias points at a provider plus a model string. A phase is
// an ordered ladder of aliases, each with its own attempt budget. Adding
// OpenAI, DeepSeek, Ollama, or anything else is a YAML edit; it must never
// require a Go change, and there must be no "if provider == ..." anywhere in
// this package.
//
// This matters because two providers can share a harness and differ only in
// which credential they carry: the same claude-code harness reads
// ANTHROPIC_API_KEY for one provider and CLAUDE_CODE_OAUTH_TOKEN for
// another (Claude subscription OAuth is scoped to the Claude Code binary, so
// a third-party caller like Hermes cannot legitimately reuse it). The
// credential is therefore not derivable from the harness — it has to be its
// own first-class piece of configuration, which is why Provider, not
// Harness, is the unit of configuration.
//
// See docs/specs/strange-company-control-plane-v1.md sections 2.3 (model
// aliases are policy, not application logic), 2.5 (policy lives in
// version-controlled files; models may read it, never rewrite it), 12 (the
// implementation escalation ladder) and 29 (least-privilege credentials: a
// run receives only the environment variables its declared provider needs,
// nothing else).
package policy

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Harness identifies which coding-agent (or inference) binary a provider
// invokes: "claude-code" | "codex" | "hermes". This package never switches
// on its value — it is opaque data that flows through to whatever actually
// launches the run. A fourth harness is a YAML edit, not a Go change.
type Harness string

// CredentialRef names where one environment variable's value comes from: a
// Kubernetes Secret the operator already created, and a key within it. The
// control plane never invents a Secret; it only reads the one named here.
type CredentialRef struct {
	Secret string `yaml:"secret"`
	Key    string `yaml:"key"`
}

// Provider is the unit of vendor configuration: a harness to invoke, the
// credentials that specific provider needs, and an optional base URL for a
// self-hosted or OpenAI-compatible endpoint.
//
// Env maps an environment-variable name (e.g. "ANTHROPIC_API_KEY") to the
// Secret/key it is read from — this is the whole of a run's credential
// footprint (spec 29): a resolution carries exactly this map and nothing
// from any other provider. PlainEnv carries non-secret settings that don't
// belong in a Kubernetes Secret. A provider needing no credentials at all —
// a local Ollama instance reachable only by BaseURL — simply has an empty or
// absent Env.
type Provider struct {
	Harness  Harness                  `yaml:"harness"`
	BaseURL  string                   `yaml:"baseUrl,omitempty"`
	Env      map[string]CredentialRef `yaml:"env"`      // env var -> where it comes from
	PlainEnv map[string]string        `yaml:"plainEnv"` // non-secret settings
}

// Alias points at a provider plus a model string. Phases reference aliases,
// never providers directly, so the same provider can back several
// differently named tiers without any tier needing to know the provider's
// name.
type Alias struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`

	// Pricing is what this model costs, when the harness will not say.
	//
	// opencode reports cost 0 for any provider models.dev has no pricing
	// for -- which is every custom OpenAI-compatible provider, including
	// the DeepSeek one this runs on -- and the gateway reports no cost at
	// all. So §22's ledger stays at zero however well the tokens are
	// counted, and max_cost_usd is enforced against a number that cannot
	// move.
	//
	// Configured rather than compiled in: the operator knows what they are
	// actually paying, a table in Go would go stale silently, and an
	// install with a negotiated rate would be wrong for everyone.
	Pricing *Pricing `yaml:"pricing,omitempty"`
}

// Pricing is a model's rate card, in US dollars per million tokens.
//
// Per million rather than per token because that is the unit every provider
// publishes, and a price written as 0.00000028 in YAML is a typo waiting to
// happen.
type Pricing struct {
	InputPerMTok  float64 `yaml:"inputPerMTok"`
	OutputPerMTok float64 `yaml:"outputPerMTok"`

	// CachedInputPerMTok is what a cache READ costs. Providers discount it
	// heavily, and a run that reads 9728 cached tokens against 552 fresh
	// ones -- a real ratio from this system -- is billed almost entirely
	// wrong if cache reads are charged at the full input rate.
	CachedInputPerMTok float64 `yaml:"cachedInputPerMTok"`

	// CacheWritePerMTok is what creating a cache entry costs. Some
	// providers charge a premium for it, some charge nothing; zero means
	// "not charged separately", which is the common case.
	CacheWritePerMTok float64 `yaml:"cacheWritePerMTok"`
}

// CostUSD prices one run's usage.
//
// Reasoning tokens are deliberately not charged here: providers bill them as
// output tokens and report them inside the output count, so adding them again
// would double-charge the thinking.
func (p *Pricing) CostUSD(input, output, cachedInput, cacheWrite int) float64 {
	if p == nil {
		return 0
	}
	const perMillion = 1_000_000.0
	return float64(input)*p.InputPerMTok/perMillion +
		float64(output)*p.OutputPerMTok/perMillion +
		float64(cachedInput)*p.CachedInputPerMTok/perMillion +
		float64(cacheWrite)*p.CacheWritePerMTok/perMillion
}

// Rung is one step of a phase's escalation ladder: an alias to use, and how
// many attempts to spend at this tier before moving to the next one.
type Rung struct {
	Alias       string `yaml:"model"`
	MaxAttempts int    `yaml:"max_attempts"`
}

// Policy is the fully loaded configuration: every known provider, every
// known alias, and every phase's ladder of rungs.
type Policy struct {
	Providers map[string]Provider `yaml:"providers"`
	Aliases   map[string]Alias    `yaml:"aliases"`
	Phases    map[string][]Rung   `yaml:"phases"`

	// actions is the allowlist stamped onto new cards. Unexported and read
	// through DefaultPermittedActions so it cannot be swapped after load --
	// spec §2.5: models may read policy, never silently rewrite it.
	actions *PermittedActions
}

// Resolution is the answer to "for this phase, on this attempt, which
// provider/model/credentials?" Its Env and PlainEnv carry only what its own
// Provider declares (spec 29) — never the union of every provider defined
// anywhere in policy.
type Resolution struct {
	Phase, Alias, ProviderName, Model string
	Harness                           Harness
	Pricing                           *Pricing
	BaseURL                           string
	Env                               map[string]CredentialRef
	PlainEnv                          map[string]string
	Attempt                           int
}

// Sentinel errors. Callers should match with errors.Is, not string
// comparison — every returned error also names the offending phase, alias or
// provider so an operator can fix the YAML without reading Go.
var (
	// ErrLadderExhausted is returned by Resolve when an attempt number falls
	// past the end of a phase's ladder. Spec 12.3: this is what moves a card
	// to NeedsHuman instead of escalating forever.
	ErrLadderExhausted = errors.New("policy: ladder exhausted")

	// ErrUnknownPhase is returned by Resolve when no phase of that name is
	// defined in models.yaml.
	ErrUnknownPhase = errors.New("policy: unknown phase")

	// ErrUnknownAlias is returned when a phase rung names an alias that is
	// not defined in models.yaml.
	ErrUnknownAlias = errors.New("policy: unknown alias")

	// ErrUnknownProvider is returned when an alias names a provider that is
	// not defined in providers.yaml.
	ErrUnknownProvider = errors.New("policy: unknown provider")
)

// Load parses providers.yaml and models.yaml (already read into memory) into
// a validated Policy. It never touches the network and never prompts —
// policy is a file, not a conversation (spec 2.5).
func Load(providersYAML, modelsYAML []byte) (*Policy, error) {
	var providersDoc Policy
	if err := yaml.Unmarshal(providersYAML, &providersDoc); err != nil {
		return nil, fmt.Errorf("policy: parsing providers.yaml: %w", err)
	}

	var modelsDoc Policy
	if err := yaml.Unmarshal(modelsYAML, &modelsDoc); err != nil {
		return nil, fmt.Errorf("policy: parsing models.yaml: %w", err)
	}

	p := &Policy{
		Providers: providersDoc.Providers,
		Aliases:   modelsDoc.Aliases,
		Phases:    modelsDoc.Phases,
	}
	if p.Providers == nil {
		p.Providers = map[string]Provider{}
	}
	if p.Aliases == nil {
		p.Aliases = map[string]Alias{}
	}
	if p.Phases == nil {
		p.Phases = map[string][]Rung{}
	}

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return p, nil
}

// LoadDir reads providers.yaml and models.yaml from dir — conventionally
// /policy, the read-only ConfigMap mount described in spec 2.5 — and loads
// them with Load.
func LoadDir(dir string) (*Policy, error) {
	providersYAML, err := os.ReadFile(filepath.Join(dir, "providers.yaml"))
	if err != nil {
		return nil, fmt.Errorf("policy: reading providers.yaml: %w", err)
	}
	modelsYAML, err := os.ReadFile(filepath.Join(dir, "models.yaml"))
	if err != nil {
		return nil, fmt.Errorf("policy: reading models.yaml: %w", err)
	}
	return Load(providersYAML, modelsYAML)
}

// Resolve answers "for phase, on this 1-based attempt, which provider, model
// and credentials?" It walks the phase's ladder of rungs, accumulating each
// rung's MaxAttempts, until it finds the rung that covers attempt: three
// attempts at MaxAttempts 3 exhaust rung zero, the fourth through sixth
// attempt exhaust rung one, and so on. A single-rung phase with
// MaxAttempts 1 (e.g. planning) is simply a ladder of length one — attempt 1
// resolves, attempt 2 is exhausted.
//
// When attempt is past the end of the ladder, Resolve returns
// ErrLadderExhausted — spec 12.3: no attempt escalates forever automatically.
//
// The shape of the ladder lives entirely in YAML; this walk is arithmetic.
func (p *Policy) Resolve(phase string, attempt int) (*Resolution, error) {
	if attempt < 1 {
		return nil, fmt.Errorf("policy: attempt must be >= 1, got %d", attempt)
	}

	rungs, ok := p.Phases[phase]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPhase, phase)
	}

	cumulative := 0
	for i, rung := range rungs {
		cumulative += rung.MaxAttempts
		if attempt > cumulative {
			continue
		}

		alias, ok := p.Aliases[rung.Alias]
		if !ok {
			return nil, fmt.Errorf("%w: phase %q rung %d names alias %q", ErrUnknownAlias, phase, i, rung.Alias)
		}
		provider, ok := p.Providers[alias.Provider]
		if !ok {
			return nil, fmt.Errorf("%w: alias %q names provider %q", ErrUnknownProvider, rung.Alias, alias.Provider)
		}

		return &Resolution{
			Phase:        phase,
			Alias:        rung.Alias,
			ProviderName: alias.Provider,
			Model:        alias.Model,
			Harness:      provider.Harness,
			BaseURL:      provider.BaseURL,
			Env:          copyEnv(provider.Env),
			PlainEnv:     copyPlainEnv(provider.PlainEnv),
			Pricing:      alias.Pricing,
			Attempt:      attempt,
		}, nil
	}

	return nil, fmt.Errorf("%w: phase %q attempt %d is past the end of its %d-attempt ladder", ErrLadderExhausted, phase, attempt, cumulative)
}

// copyEnv returns a defensive shallow copy so a caller mutating a Resolution
// can never mutate the Policy's own provider definitions.
func copyEnv(in map[string]CredentialRef) map[string]CredentialRef {
	out := make(map[string]CredentialRef, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// copyPlainEnv is copyEnv's counterpart for the non-secret settings map.
func copyPlainEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Validate checks internal consistency of the loaded policy:
//
//   - every alias names a provider that exists;
//   - every phase rung names an alias that exists;
//   - every rung asks for at least one attempt;
//   - every provider names a non-empty harness;
//   - every credential entry names both a Secret and a key within it.
//
// Every problem found names the offending key, so an operator can fix the
// YAML without reading this package's source. Validate collects every
// problem it finds rather than stopping at the first, matching this
// codebase's convention (see internal/config.Load) of letting an operator
// fix a broken deployment in one pass instead of one restart per mistake.
// AttemptsFor returns how many attempts a phase's whole ladder allows.
//
// It is the denominator in §33's "Haiku attempt 2/3". A bare attempt number
// tells a reader nothing; the total tells them how much rope is left, which is
// the question actually asked of a card that is taking a while.
//
// Zero for a phase with no ladder, so a caller can omit the denominator rather
// than print a misleading one.
func (p *Policy) AttemptsFor(phase string) int {
	total := 0
	for _, rung := range p.Phases[phase] {
		total += rung.MaxAttempts
	}
	return total
}

func (p *Policy) Validate() error {
	var problems []error

	for name, provider := range p.Providers {
		if strings.TrimSpace(string(provider.Harness)) == "" {
			problems = append(problems, fmt.Errorf("provider %q: harness must not be empty", name))
		}
		for envVar, ref := range provider.Env {
			if strings.TrimSpace(ref.Secret) == "" {
				problems = append(problems, fmt.Errorf("provider %q: env %q: secret must not be empty", name, envVar))
			}
			if strings.TrimSpace(ref.Key) == "" {
				problems = append(problems, fmt.Errorf("provider %q: env %q: key must not be empty", name, envVar))
			}
		}
	}

	for name, alias := range p.Aliases {
		if _, ok := p.Providers[alias.Provider]; !ok {
			problems = append(problems, fmt.Errorf("%w: alias %q names provider %q", ErrUnknownProvider, name, alias.Provider))
		}
	}

	for phaseName, rungs := range p.Phases {
		for i, rung := range rungs {
			if _, ok := p.Aliases[rung.Alias]; !ok {
				problems = append(problems, fmt.Errorf("%w: phase %q rung %d names alias %q", ErrUnknownAlias, phaseName, i, rung.Alias))
			}
			if rung.MaxAttempts < 1 {
				problems = append(problems, fmt.Errorf("phase %q rung %d (alias %q): max_attempts must be >= 1, got %d", phaseName, i, rung.Alias, rung.MaxAttempts))
			}
		}
	}

	return errors.Join(problems...)
}


//go:embed defaults/providers.yaml defaults/models.yaml defaults/actions.yaml
var defaultsFS embed.FS

// Defaults returns the policy compiled into the binary.
//
// Shipping a working default matters: the control plane must start and be
// inspectable before an operator has written any policy of their own. An
// operator overrides it by mounting their own files and pointing POLICY_DIR at
// them -- which is why the mount is read-only (spec 2.5: models may read
// policy, never silently rewrite it).
func Defaults() (*Policy, error) {
	providers, err := defaultsFS.ReadFile("defaults/providers.yaml")
	if err != nil {
		return nil, fmt.Errorf("policy: read embedded providers: %w", err)
	}
	models, err := defaultsFS.ReadFile("defaults/models.yaml")
	if err != nil {
		return nil, fmt.Errorf("policy: read embedded models: %w", err)
	}
	actions, err := defaultsFS.ReadFile("defaults/actions.yaml")
	if err != nil {
		return nil, fmt.Errorf("policy: read embedded actions: %w", err)
	}

	p, err := Load(providers, models)
	if err != nil {
		return nil, err
	}
	if err := p.loadActions(actions); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadOrDefaults loads policy from dir when dir is non-empty and readable, and
// otherwise falls back to the embedded defaults. The returned bool reports
// whether operator-supplied policy was used, so startup can say which is in
// force rather than leaving it ambiguous.
func LoadOrDefaults(dir string) (*Policy, bool, error) {
	if dir == "" {
		p, err := Defaults()
		return p, false, err
	}
	p, err := LoadDir(dir)
	if err != nil {
		return nil, false, err
	}

	// actions.yaml is optional in an operator directory. Someone who only
	// wanted to change models must not have to restate the allowlist, and
	// silently leaving them without one would make every card unpromotable.
	raw, err := os.ReadFile(filepath.Join(dir, "actions.yaml"))
	switch {
	case err == nil:
		if err := p.loadActions(raw); err != nil {
			return nil, false, err
		}
	case errors.Is(err, os.ErrNotExist):
		defaults, dErr := defaultsFS.ReadFile("defaults/actions.yaml")
		if dErr != nil {
			return nil, false, fmt.Errorf("policy: read embedded actions: %w", dErr)
		}
		if err := p.loadActions(defaults); err != nil {
			return nil, false, err
		}
	default:
		return nil, false, fmt.Errorf("policy: read %s: %w", filepath.Join(dir, "actions.yaml"), err)
	}

	return p, true, nil
}

// PermittedActions is a card's allowlist (spec §5's permitted_actions block).
//
// It is an ALLOWLIST. §24's forbidden set is forbidden by absence from these
// lists, not by a denylist beside them -- a denylist alongside an allowlist
// invites the reading that anything not denied is allowed.
type PermittedActions struct {
	Files struct {
		Include []string `yaml:"include" json:"include"`
		Exclude []string `yaml:"exclude" json:"exclude"`
	} `yaml:"files" json:"files"`
	Commands  []string `yaml:"commands" json:"commands"`
	Endpoints []string `yaml:"endpoints" json:"endpoints"`
	Network   []string `yaml:"network" json:"network"`
}

// DefaultPermittedActions is the allowlist stamped onto a card at creation.
//
// Per-card rather than global on purpose: §10's gate asks whether a policy
// exists FOR THIS CARD, and a global default consulted at gate time would make
// that check unfailable -- turning a real rule into a rubber stamp. A card that
// reaches the gate without one still fails, which is the check working.
func (p *Policy) DefaultPermittedActions() *PermittedActions {
	return p.actions
}

func (p *Policy) loadActions(raw []byte) error {
	var a PermittedActions
	if err := yaml.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("policy: parsing actions.yaml: %w", err)
	}
	if len(a.Files.Include) == 0 {
		return errors.New("policy: actions.yaml permits no files, so no card could do any work")
	}
	if len(a.Commands) == 0 {
		return errors.New("policy: actions.yaml permits no commands, so no card could run its tests")
	}
	p.actions = &a
	return nil
}
