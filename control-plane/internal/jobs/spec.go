// Package jobs builds the Kubernetes Job manifest for one isolated coding
// run (spec §16, "Kubernetes Coding Jobs"). It is pure construction: given a
// card, a resolved policy.Resolution and a runner.Request, Build produces a
// *Job value that marshals to the manifest the control plane will later
// submit to the Kubernetes API. Nothing in this package talks to a cluster —
// creating and watching Jobs is a later milestone; getting the manifest
// itself right is what matters here, because every hardening requirement in
// spec §16.1 has to be encoded in this one place or it does not exist.
//
// This package deliberately does not import k8s.io/api or client-go. It
// defines the minimal subset of the batch/v1 Job shape it needs as plain Go
// structs with encoding/json tags that match the real Kubernetes API field
// names exactly, so the JSON this package produces is a valid Job manifest
// even though no Kubernetes types are linked in. Should this control plane
// later add a real client, these types can be swapped for k8s.io/api's
// without changing Build's contract.
package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

// ---------------------------------------------------------------------------
// Minimal batch/v1 Job shape.
//
// Every field below exists because Build sets it; this is not a general
// Kubernetes API binding. Field names and JSON tags mirror the real
// batch/v1.Job / corev1.PodSpec API exactly.
// ---------------------------------------------------------------------------

// ObjectMeta is the minimal subset of metav1.ObjectMeta this package needs.
type ObjectMeta struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Job is the minimal batch/v1 Job shape this package constructs.
type Job struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       JobSpec    `json:"spec"`
}

// JobSpec is the minimal batch/v1.JobSpec this package needs.
//
// ActiveDeadlineSeconds and BackoffLimit are pointers because their zero
// value (0) is meaningful in the Kubernetes API (an unset
// ActiveDeadlineSeconds means "no wall-clock deadline"; BackoffLimit 0 means
// "never retry", which is exactly what spec §16.1 requires) and must be
// distinguishable from "not set".
type JobSpec struct {
	ActiveDeadlineSeconds *int64          `json:"activeDeadlineSeconds,omitempty"`
	BackoffLimit          *int32          `json:"backoffLimit"`
	Template              PodTemplateSpec `json:"template"`
}

// PodTemplateSpec is the minimal corev1.PodTemplateSpec this package needs.
type PodTemplateSpec struct {
	Metadata ObjectMeta `json:"metadata,omitempty"`
	Spec     PodSpec    `json:"spec"`
}

// PodSpec is the minimal corev1.PodSpec this package needs. Notably absent:
// any hostPath-capable field, HostNetwork/HostPID/HostIPC are present only
// so Build can leave them at their zero value (false) rather than never
// mentioning them at all — spec §16.1 forbids all three.
type PodSpec struct {
	RestartPolicy                string              `json:"restartPolicy"`
	ServiceAccountName           string              `json:"serviceAccountName,omitempty"`
	AutomountServiceAccountToken *bool               `json:"automountServiceAccountToken"`
	SecurityContext              *PodSecurityContext `json:"securityContext,omitempty"`
	Containers                   []Container         `json:"containers"`
	Volumes                      []Volume            `json:"volumes,omitempty"`
	HostNetwork                  bool                `json:"hostNetwork,omitempty"`
	HostPID                      bool                `json:"hostPID,omitempty"`
	HostIPC                      bool                `json:"hostIPC,omitempty"`
}

// PodSecurityContext is the minimal corev1.PodSecurityContext this package
// needs.
type PodSecurityContext struct {
	RunAsNonRoot   *bool           `json:"runAsNonRoot,omitempty"`
	RunAsUser      *int64          `json:"runAsUser,omitempty"`
	RunAsGroup     *int64          `json:"runAsGroup,omitempty"`
	SeccompProfile *SeccompProfile `json:"seccompProfile,omitempty"`
}

// SeccompProfile is the minimal corev1.SeccompProfile this package needs.
type SeccompProfile struct {
	Type string `json:"type"`
}

// Container is the minimal corev1.Container this package needs.
type Container struct {
	Name            string                    `json:"name"`
	Image           string                    `json:"image"`
	Command         []string                  `json:"command,omitempty"`
	Args            []string                  `json:"args,omitempty"`
	WorkingDir      string                    `json:"workingDir,omitempty"`
	Env             []EnvVar                  `json:"env,omitempty"`
	Resources       ResourceRequirements      `json:"resources,omitempty"`
	VolumeMounts    []VolumeMount             `json:"volumeMounts,omitempty"`
	SecurityContext *ContainerSecurityContext `json:"securityContext,omitempty"`
}

// EnvVar is the minimal corev1.EnvVar this package needs. Exactly one of
// Value or ValueFrom is ever set by this package: Value for
// Spec.PlainEnv and the run-identifying variables Build adds, ValueFrom for
// every entry of Spec.Env (spec §29 — secret-backed credentials only, never
// inlined).
type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

// EnvVarSource is the minimal corev1.EnvVarSource this package needs.
type EnvVarSource struct {
	SecretKeyRef *SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// SecretKeySelector is the minimal corev1.SecretKeySelector this package
// needs.
type SecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// ResourceRequirements is the minimal corev1.ResourceRequirements this
// package needs. Kubernetes resource quantities are strings (e.g. "2",
// "4Gi"), so both maps are map[string]string rather than a typed quantity —
// this package never parses or arithmetically manipulates a quantity, only
// carries the operator-supplied string through.
type ResourceRequirements struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

// VolumeMount is the minimal corev1.VolumeMount this package needs.
type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

// Volume is the minimal corev1.Volume this package needs. Deliberately, the
// only source this package knows how to produce is EmptyDir — there is no
// HostPath field here at all, which is a stronger guarantee than "Build
// happens not to set one": spec §16.1 forbids hostPath volumes outright, and
// this type cannot represent one even by mistake.
type Volume struct {
	Name     string                `json:"name"`
	EmptyDir *EmptyDirVolumeSource `json:"emptyDir,omitempty"`
}

// EmptyDirVolumeSource is the minimal corev1.EmptyDirVolumeSource this
// package needs.
type EmptyDirVolumeSource struct {
	Medium string `json:"medium,omitempty"`
}

// ContainerSecurityContext is the minimal corev1.SecurityContext this
// package needs. There is no Privileged field set by Build — it is left at
// its nil/absent zero value throughout, satisfying spec §16.1's "privileged
// absent/false" by construction rather than by remembering to set false.
type ContainerSecurityContext struct {
	AllowPrivilegeEscalation *bool         `json:"allowPrivilegeEscalation,omitempty"`
	ReadOnlyRootFilesystem   *bool         `json:"readOnlyRootFilesystem,omitempty"`
	Privileged               *bool         `json:"privileged,omitempty"`
	Capabilities             *Capabilities `json:"capabilities,omitempty"`
	RunAsNonRoot             *bool         `json:"runAsNonRoot,omitempty"`
	RunAsUser                *int64        `json:"runAsUser,omitempty"`
	RunAsGroup               *int64        `json:"runAsGroup,omitempty"`
}

// Capabilities is the minimal corev1.Capabilities this package needs.
type Capabilities struct {
	Add  []string `json:"add,omitempty"`
	Drop []string `json:"drop,omitempty"`
}

// ---------------------------------------------------------------------------
// Fixed, non-configurable hardening values (spec §16.1).
//
// These are not exposed on Spec: they are not per-run decisions, they are
// the control plane's own security posture. A caller cannot construct a Spec
// that turns them off.
// ---------------------------------------------------------------------------

const (
	// nonRootUID/nonRootGID are the fixed non-root UID/GID every coding Job
	// runs as. Any non-zero value satisfies spec §16.1's "non-root user";
	// this one is arbitrary but fixed so Job identity in cluster audit logs
	// (e.g. Falco, admission webhooks keyed on uid) is stable across runs.
	nonRootUID = 65532
	nonRootGID = 65532

	// workspaceVolumeName/workspaceMountPath back the emptyDir workspace
	// (spec §16.2: git is the persistent store, no per-card PVC).
	workspaceVolumeName = "workspace"
	workspaceMountPath  = "/workspace"

	// homeVolumeName/homeMountPath back a writable HOME.
	//
	// §16.1 requires readOnlyRootFilesystem, which leaves a CLI harness
	// nowhere to put its own state: opencode failed the first real run with
	// EROFS creating /home/runner/.local. The workspace cannot double as
	// HOME -- it is the git checkout, and a harness dropping dotfiles there
	// would commit them.
	homeVolumeName = "home"
	homeMountPath  = "/home/agent"

	// containerName is the name of the sole container in the pod template.
	containerName = "coding-agent"

	// maxSlugLen bounds AgentBranch's slug component to a sane length —
	// long enough to be recognisable, short enough that
	// "agent/<card-id>-<slug>" never runs into a Git ref length limit.
	maxSlugLen = 50

	// maxK8sNameLen is the Kubernetes DNS-1123 subdomain length limit that
	// Job metadata.name (and most other object names) must respect.
	maxK8sNameLen = 253
)

// Spec is everything Build needs to construct the manifest for one isolated
// coding Job.
type Spec struct {
	CardID    string
	RunID     string
	Namespace string // conventionally "agent-runs" (spec §16); Build does not default this — see Build's doc comment
	Image     string // runner image
	Harness   string
	Model     string
	RepoURL   string
	Branch    string
	Command   []string // argv from the adapter's Command()

	// BaseRef is the ref the agent branch is cut from. Required: defaulting it
	// would mean silently branching from the wrong place on any repo whose
	// default branch is not what we guessed.
	BaseRef string

	// Phase and Attempt appear in the commit message the runner writes, so the
	// agent branch reads as a history of the work (spec §16.2).
	Phase   string
	Attempt int

	// CommitSummary optionally overrides the generated commit subject.
	CommitSummary string

	// GitToken is the credential the runner pushes the agent branch with. It is
	// a Secret reference like any other credential, and is deliberately separate
	// from Env: it is repository access, not model access, and the two should
	// never be conflated (spec §29).
	GitToken *policy.CredentialRef
	// GitUsername is the HTTPS username paired with GitToken. For GitHub tokens
	// this is a placeholder the server ignores.
	GitUsername string
	// GitAuthorName/Email identify the bot in commit metadata.
	GitAuthorName  string
	GitAuthorEmail string

	Env      map[string]policy.CredentialRef // secret-backed (spec §29)
	PlainEnv map[string]string

	CPULimit    string // e.g. "2"
	MemoryLimit string // e.g. "4Gi"

	Timeout     time.Duration // wall-clock timeout -> activeDeadlineSeconds
	ServiceAcct string        // usually empty; automountServiceAccountToken is false regardless (spec §16.1)
}

// Build constructs the Job manifest for one isolated coding run.
//
// Build validates before it builds: Namespace, Image, Command, CardID and
// RunID must be non-empty and Timeout must be positive, or Build returns an
// error instead of a manifest. Namespace is required rather than defaulted
// to "agent-runs" here on purpose — spec §16 names "agent-runs" as the
// convention, not a fallback this package should paper over a caller's
// mistake with; a malformed or under-specified Job that nonetheless runs is
// worse than one that never launches.
//
// Every hardening property spec §16.1 requires is set unconditionally by
// Build, not derived from anything on Spec: non-root user, dropped
// capabilities, no privilege escalation, no service-account token, no
// hostPath/hostNetwork/hostPID/hostIPC, backoffLimit 0, restartPolicy
// Never, and an emptyDir workspace in place of a per-card PVC (spec §16.2).
func Build(s Spec) (*Job, error) {
	if strings.TrimSpace(s.Namespace) == "" {
		return nil, fmt.Errorf("jobs: Namespace must not be empty")
	}
	if strings.TrimSpace(s.Image) == "" {
		return nil, fmt.Errorf("jobs: Image must not be empty")
	}
	if s.BaseRef == "" {
		// Defaulting this would silently branch from the wrong place on any repo
		// whose default branch is not the one we guessed.
		return nil, fmt.Errorf("jobs: BaseRef is required")
	}
	if s.Phase == "" {
		return nil, fmt.Errorf("jobs: Phase is required")
	}
	if s.Attempt < 1 {
		return nil, fmt.Errorf("jobs: Attempt must be at least 1")
	}
	if len(s.Command) == 0 {
		return nil, fmt.Errorf("jobs: Command must not be empty")
	}
	if s.Timeout <= 0 {
		return nil, fmt.Errorf("jobs: Timeout must be > 0, got %s", s.Timeout)
	}
	if strings.TrimSpace(s.CardID) == "" {
		return nil, fmt.Errorf("jobs: CardID must not be empty")
	}
	if strings.TrimSpace(s.RunID) == "" {
		return nil, fmt.Errorf("jobs: RunID must not be empty")
	}

	labels := map[string]string{
		"strangecompany.dev/card-id": s.CardID,
		"strangecompany.dev/run-id":  s.RunID,
	}
	if strings.TrimSpace(s.Harness) != "" {
		labels["strangecompany.dev/harness"] = s.Harness
	}

	env := buildEnv(s)
	resources := buildResources(s)

	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := true
	runAsNonRoot := true
	runAsUser := int64(nonRootUID)
	runAsGroup := int64(nonRootGID)
	automount := false
	backoffLimit := int32(0)
	activeDeadlineSeconds := int64(s.Timeout / time.Second)
	if activeDeadlineSeconds < 1 {
		// A sub-second timeout still must not silently mean "no deadline"
		// (activeDeadlineSeconds 0 is unset in the Kubernetes API).
		activeDeadlineSeconds = 1
	}

	job := &Job{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Metadata: ObjectMeta{
			Name:      jobName(s.RunID),
			Namespace: s.Namespace,
			Labels:    labels,
		},
		Spec: JobSpec{
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			BackoffLimit:          &backoffLimit,
			Template: PodTemplateSpec{
				Metadata: ObjectMeta{
					Labels: labels,
				},
				Spec: PodSpec{
					RestartPolicy:                "Never",
					ServiceAccountName:           s.ServiceAcct,
					AutomountServiceAccountToken: &automount,
					SecurityContext: &PodSecurityContext{
						RunAsNonRoot: &runAsNonRoot,
						RunAsUser:    &runAsUser,
						RunAsGroup:   &runAsGroup,
						SeccompProfile: &SeccompProfile{
							Type: "RuntimeDefault",
						},
					},
					Containers: []Container{
						{
							Name:       containerName,
							Image:      s.Image,
							// args, NOT command.
							//
							// Kubernetes maps `command` to the image's
							// ENTRYPOINT and `args` to its CMD. Putting the
							// harness argv in `command` REPLACED the runner
							// image's entrypoint, so entrypoint.sh never ran:
							// no clone, no agent branch, no harness config, no
							// commit and no push. The harness executed alone in
							// an empty workspace and everything the entrypoint
							// exists to do silently did not happen.
							Args:       s.Command,
							WorkingDir: workspaceMountPath,
							Env:        env,
							Resources:  resources,
							VolumeMounts: []VolumeMount{
								{
									Name:      homeVolumeName,
									MountPath: homeMountPath,
								},
								{
									Name:      workspaceVolumeName,
									MountPath: workspaceMountPath,
								},
							},
							SecurityContext: &ContainerSecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
								Capabilities: &Capabilities{
									Drop: []string{"ALL"},
								},
								RunAsNonRoot: &runAsNonRoot,
								RunAsUser:    &runAsUser,
								RunAsGroup:   &runAsGroup,
							},
						},
					},
					Volumes: []Volume{
						{
							Name:     homeVolumeName,
							EmptyDir: &EmptyDirVolumeSource{},
						},
						{
							Name:     workspaceVolumeName,
							EmptyDir: &EmptyDirVolumeSource{},
						},
					},
					// HostNetwork, HostPID and HostIPC are left at their
					// zero value (false) — spec §16.1.
				},
			},
		},
	}

	return job, nil
}

// buildEnv assembles the container's env list: every credential Spec.Env
// declares as a secretKeyRef (spec §29 — never inlined, never any other
// provider's variable), every Spec.PlainEnv entry as a literal value, and a
// small set of run-identifying variables (spec §16.1: "each Job receives
// card ID, run ID, model alias, harness selection, repository URL,
// branch"). Iteration is over sorted keys so the resulting manifest is
// deterministic across calls, which matters for diffing and for tests.
func buildEnv(s Spec) []EnvVar {
	// HOME and the XDG paths point at the writable emptyDir. Without them a
	// harness under readOnlyRootFilesystem has nowhere for its own state:
	// opencode failed the first real run with
	// EROFS: read-only file system, mkdir '/home/runner/.local'.
	//
	// Set before anything else so they cannot silently override a value the
	// caller meant, and pointed away from the workspace on purpose -- that
	// is the git checkout, and dotfiles written there would be committed.
	env := []EnvVar{
		{Name: "HOME", Value: homeMountPath},
		{Name: "XDG_CONFIG_HOME", Value: homeMountPath + "/.config"},
		{Name: "XDG_DATA_HOME", Value: homeMountPath + "/.local/share"},
		{Name: "XDG_STATE_HOME", Value: homeMountPath + "/.local/state"},
		{Name: "XDG_CACHE_HOME", Value: homeMountPath + "/.cache"},
	}

	secretKeys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		secretKeys = append(secretKeys, k)
	}
	sort.Strings(secretKeys)
	for _, k := range secretKeys {
		ref := s.Env[k]
		env = append(env, EnvVar{
			Name: k,
			ValueFrom: &EnvVarSource{
				SecretKeyRef: &SecretKeySelector{
					Name: ref.Secret,
					Key:  ref.Key,
				},
			},
		})
	}

	plainKeys := make([]string, 0, len(s.PlainEnv))
	for k := range s.PlainEnv {
		plainKeys = append(plainKeys, k)
	}
	sort.Strings(plainKeys)
	for _, k := range plainKeys {
		env = append(env, EnvVar{Name: k, Value: s.PlainEnv[k]})
	}

	if s.GitToken != nil {
		env = append(env, EnvVar{
			Name: "SC_GIT_TOKEN",
			ValueFrom: &EnvVarSource{
				SecretKeyRef: &SecretKeySelector{
					Name: s.GitToken.Secret,
					Key:  s.GitToken.Key,
				},
			},
		})
	}

	// Run-identifying variables. These are not credentials and carry no
	// secret material, so they are safe to inline as literal values.
	for _, kv := range []struct{ name, value string }{
		{"SC_CARD_ID", s.CardID},
		{"SC_RUN_ID", s.RunID},
		{"SC_HARNESS", s.Harness},
		{"SC_MODEL", s.Model},
		{"SC_REPO_URL", s.RepoURL},
		{"SC_BRANCH", s.Branch},
		{"SC_BASE_REF", s.BaseRef},
		{"SC_PHASE", s.Phase},
		{"SC_ATTEMPT", strconv.Itoa(s.Attempt)},
		{"SC_COMMIT_SUMMARY", s.CommitSummary},
		{"SC_GIT_USERNAME", s.GitUsername},
		{"SC_GIT_AUTHOR_NAME", s.GitAuthorName},
		{"SC_GIT_AUTHOR_EMAIL", s.GitAuthorEmail},
		{"SC_WORKSPACE_ROOT", workspaceMountPath},
	} {
		if kv.value == "" {
			continue
		}
		env = append(env, EnvVar{Name: kv.name, Value: kv.value})
	}

	return env
}

// buildResources sets CPU and memory limits and requests. Requests are set
// equal to limits: Spec carries only one CPU/memory figure per run, and
// scheduling this Job at anything less than its limit risks the same
// resource-starvation surprises spec §16.1's hard limits exist to prevent.
// An empty CPULimit or MemoryLimit is omitted rather than encoded as an
// empty-string quantity, which Kubernetes would reject.
func buildResources(s Spec) ResourceRequirements {
	limits := map[string]string{}
	if s.CPULimit != "" {
		limits["cpu"] = s.CPULimit
	}
	if s.MemoryLimit != "" {
		limits["memory"] = s.MemoryLimit
	}
	if len(limits) == 0 {
		return ResourceRequirements{}
	}
	requests := make(map[string]string, len(limits))
	for k, v := range limits {
		requests[k] = v
	}
	return ResourceRequirements{Limits: limits, Requests: requests}
}

// jobName derives a valid Kubernetes object name from a run ID.
func jobName(runID string) string {
	name := slugify("coding-" + runID)
	if name == "" {
		name = "coding-job"
	}
	if len(name) > maxK8sNameLen {
		name = strings.TrimRight(name[:maxK8sNameLen], "-")
	}
	return name
}

// JSON marshals the Job to indented JSON — the manifest this package
// exists to produce.
func (j *Job) JSON() ([]byte, error) {
	return json.MarshalIndent(j, "", "  ")
}

// ---------------------------------------------------------------------------
// Branch naming (spec §16.2).
// ---------------------------------------------------------------------------

// nonAlnum matches any run of characters that is not a lowercase ASCII
// letter or digit, for collapsing into a single hyphen by slugify.
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases s, collapses every run of non-alphanumeric characters
// into a single hyphen, trims leading/trailing hyphens, and truncates to
// maxSlugLen (re-trimming any hyphen the truncation exposes at the new
// end). A title that is entirely punctuation (or otherwise contains no
// alphanumeric character at all) slugifies to the empty string.
func slugify(s string) string {
	lower := strings.ToLower(s)
	collapsed := nonAlnum.ReplaceAllString(lower, "-")
	trimmed := strings.Trim(collapsed, "-")
	if len(trimmed) > maxSlugLen {
		trimmed = strings.TrimRight(trimmed[:maxSlugLen], "-")
	}
	return trimmed
}

// AgentBranch returns the agent branch name for a card, per spec §16.2:
// "agent/<card-id>-<slug>". When title slugifies to nothing (empty, or
// entirely punctuation/whitespace), the trailing "-<slug>" is omitted
// entirely rather than left as a dangling hyphen — "agent/<card-id>" is a
// valid, if less descriptive, branch name; "agent/<card-id>-" is not one
// anybody would want in git history.
func AgentBranch(cardID, title string) string {
	slug := slugify(title)
	if slug == "" {
		return fmt.Sprintf("agent/%s", cardID)
	}
	return fmt.Sprintf("agent/%s-%s", cardID, slug)
}
