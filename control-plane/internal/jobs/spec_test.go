package jobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

// validSpec returns a Spec that satisfies every one of Build's rejection
// rules, for tests to mutate a single field away from.
func validSpec() Spec {
	return Spec{
		CardID:  "card-123",
		RunID:   "run-abc",
		Namespace: "agent-runs",
		Image:   "registry.example.com/strange-company/runner:latest",
		Harness: "claude-code",
		Model:   "claude-sonnet-5",
		RepoURL: "https://github.com/example/repo.git",
		Branch:  "agent/card-123-do-the-thing",
		Command: []string{"claude", "-p", "implement the widget"},
		Env: map[string]policy.CredentialRef{
			"ANTHROPIC_API_KEY": {Secret: "anthropic-credentials", Key: "api-key"},
		},
		PlainEnv: map[string]string{
			"FOO": "bar",
		},
		CPULimit:    "2",
		MemoryLimit: "4Gi",
		Timeout:     30 * time.Minute,
		ServiceAcct: "",
	}
}

func mustBuild(t *testing.T, s Spec) *Job {
	t.Helper()
	job, err := Build(s)
	if err != nil {
		t.Fatalf("Build() returned unexpected error: %v", err)
	}
	return job
}

// --- Build: rejection of malformed input ------------------------------

func TestBuild_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *Spec)
	}{
		{"empty namespace", func(s *Spec) { s.Namespace = "" }},
		{"empty image", func(s *Spec) { s.Image = "" }},
		{"nil command", func(s *Spec) { s.Command = nil }},
		{"empty command", func(s *Spec) { s.Command = []string{} }},
		{"zero timeout", func(s *Spec) { s.Timeout = 0 }},
		{"negative timeout", func(s *Spec) { s.Timeout = -1 * time.Second }},
		{"empty card id", func(s *Spec) { s.CardID = "" }},
		{"empty run id", func(s *Spec) { s.RunID = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			tc.mutate(&s)
			if _, err := Build(s); err == nil {
				t.Fatalf("spec 16.1: Build() with %s = nil error, want an error — a malformed Job that runs is far worse than one that never launches", tc.name)
			}
		})
	}
}

// --- Build: wall-clock timeout -----------------------------------------

func TestBuild_SetsActiveDeadlineSecondsFromTimeout(t *testing.T) {
	s := validSpec()
	s.Timeout = 45 * time.Minute
	job := mustBuild(t, s)

	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatalf("spec 16.1: activeDeadlineSeconds is nil, want it set from Timeout")
	}
	want := int64(45 * 60)
	if got := *job.Spec.ActiveDeadlineSeconds; got != want {
		t.Fatalf("spec 16.1: activeDeadlineSeconds = %d, want %d (Timeout=%s)", got, want, s.Timeout)
	}
}

// --- Build: backoffLimit 0, restartPolicy Never -------------------------

func TestBuild_SetsBackoffLimitZero(t *testing.T) {
	job := mustBuild(t, validSpec())
	if job.Spec.BackoffLimit == nil {
		t.Fatalf("spec 16.1/12: backoffLimit is nil, want 0 — a coding attempt must not be silently retried by Kubernetes")
	}
	if got := *job.Spec.BackoffLimit; got != 0 {
		t.Fatalf("spec 16.1/12: backoffLimit = %d, want 0 — spec 12 owns retries, a k8s retry would corrupt the attempt count", got)
	}
}

func TestBuild_SetsRestartPolicyNever(t *testing.T) {
	job := mustBuild(t, validSpec())
	if got := job.Spec.Template.Spec.RestartPolicy; got != "Never" {
		t.Fatalf("spec 16.1: restartPolicy = %q, want %q", got, "Never")
	}
}

// --- Build: non-root, seccomp -------------------------------------------

func TestBuild_RunsNonRoot(t *testing.T) {
	job := mustBuild(t, validSpec())

	podSC := job.Spec.Template.Spec.SecurityContext
	if podSC == nil {
		t.Fatalf("spec 16.1: pod securityContext is nil, want runAsNonRoot true")
	}
	if podSC.RunAsNonRoot == nil || !*podSC.RunAsNonRoot {
		t.Fatalf("spec 16.1: pod securityContext.runAsNonRoot = %v, want true", podSC.RunAsNonRoot)
	}
	if podSC.RunAsUser == nil || *podSC.RunAsUser == 0 {
		t.Fatalf("spec 16.1: pod securityContext.runAsUser = %v, want a non-zero uid", podSC.RunAsUser)
	}
	if podSC.RunAsGroup == nil || *podSC.RunAsGroup == 0 {
		t.Fatalf("spec 16.1: pod securityContext.runAsGroup = %v, want a non-zero gid", podSC.RunAsGroup)
	}
	if podSC.SeccompProfile == nil || podSC.SeccompProfile.Type != "RuntimeDefault" {
		t.Fatalf("spec 16.1: pod securityContext.seccompProfile = %+v, want type RuntimeDefault", podSC.SeccompProfile)
	}

	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("want exactly one container, got %d", len(job.Spec.Template.Spec.Containers))
	}
	containerSC := job.Spec.Template.Spec.Containers[0].SecurityContext
	if containerSC == nil {
		t.Fatalf("spec 16.1: container securityContext is nil")
	}
	if containerSC.RunAsNonRoot == nil || !*containerSC.RunAsNonRoot {
		t.Fatalf("spec 16.1: container securityContext.runAsNonRoot = %v, want true", containerSC.RunAsNonRoot)
	}
	if containerSC.RunAsUser == nil || *containerSC.RunAsUser == 0 {
		t.Fatalf("spec 16.1: container securityContext.runAsUser = %v, want a non-zero uid", containerSC.RunAsUser)
	}
	if containerSC.RunAsGroup == nil || *containerSC.RunAsGroup == 0 {
		t.Fatalf("spec 16.1: container securityContext.runAsGroup = %v, want a non-zero gid", containerSC.RunAsGroup)
	}
}

// --- Build: no privilege escalation, capabilities dropped ---------------

func TestBuild_AllowPrivilegeEscalationFalse(t *testing.T) {
	job := mustBuild(t, validSpec())
	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatalf("spec 16.1: allowPrivilegeEscalation = %+v, want false", sc)
	}
}

func TestBuild_CapabilitiesDropAll(t *testing.T) {
	job := mustBuild(t, validSpec())
	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.Capabilities == nil {
		t.Fatalf("spec 16.1: capabilities is nil, want drop: [ALL]")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("spec 16.1: capabilities.drop = %v, want [ALL]", sc.Capabilities.Drop)
	}
}

func TestBuild_PrivilegedNotTrue(t *testing.T) {
	job := mustBuild(t, validSpec())
	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	if sc != nil && sc.Privileged != nil && *sc.Privileged {
		t.Fatalf("spec 16.1: privileged = true, want absent or false")
	}
}

// --- Build: no Kubernetes API access -------------------------------------

func TestBuild_AutomountServiceAccountTokenFalse(t *testing.T) {
	job := mustBuild(t, validSpec())
	got := job.Spec.Template.Spec.AutomountServiceAccountToken
	if got == nil || *got {
		t.Fatalf("spec 16.1: automountServiceAccountToken = %v, want false — a coding Job never gets Kubernetes API access", got)
	}
}

// --- Build: no host escape surface ---------------------------------------

func TestBuild_NoHostPathVolume(t *testing.T) {
	job := mustBuild(t, validSpec())
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.EmptyDir == nil {
			t.Fatalf("spec 16.1: volume %q is not an emptyDir volume, want no hostPath (or any non-emptyDir) volume", v.Name)
		}
	}

	data, err := job.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}
	if strings.Contains(string(data), "hostPath") {
		t.Fatalf("spec 16.1: manifest JSON contains \"hostPath\", want no hostPath volume at all:\n%s", data)
	}
}

func TestBuild_NoHostNetworkPIDIPC(t *testing.T) {
	job := mustBuild(t, validSpec())
	ps := job.Spec.Template.Spec
	if ps.HostNetwork {
		t.Fatalf("spec 16.1: hostNetwork = true, want false")
	}
	if ps.HostPID {
		t.Fatalf("spec 16.1: hostPID = true, want false")
	}
	if ps.HostIPC {
		t.Fatalf("spec 16.1: hostIPC = true, want false")
	}
}

// --- Build: emptyDir workspace, no PVC -----------------------------------

func TestBuild_MountsEmptyDirWorkspace(t *testing.T) {
	job := mustBuild(t, validSpec())
	ps := job.Spec.Template.Spec

	var found *Volume
	for i := range ps.Volumes {
		if ps.Volumes[i].Name == workspaceVolumeName {
			found = &ps.Volumes[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("spec 16.2: no %q volume found, want an emptyDir workspace (git is the persistent store, no PVC per card)", workspaceVolumeName)
	}
	if found.EmptyDir == nil {
		t.Fatalf("spec 16.2: volume %q is not an emptyDir", workspaceVolumeName)
	}

	mounted := false
	for _, vm := range ps.Containers[0].VolumeMounts {
		if vm.Name == workspaceVolumeName && vm.MountPath != "" {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("spec 16.2: container does not mount the %q volume", workspaceVolumeName)
	}
}

func TestBuild_ReadOnlyRootFilesystemWithWritableWorkspaceMount(t *testing.T) {
	job := mustBuild(t, validSpec())
	c := job.Spec.Template.Spec.Containers[0]

	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatalf("spec 16.1: readOnlyRootFilesystem = %+v, want true", c.SecurityContext)
	}

	found := false
	for _, vm := range c.VolumeMounts {
		if vm.Name == workspaceVolumeName && !vm.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Fatalf("spec 16.1/16.2: no writable workspace mount found alongside readOnlyRootFilesystem")
	}
}

// --- Build: CPU/memory limits and requests -------------------------------

func TestBuild_SetsCPUAndMemoryLimitsAndRequests(t *testing.T) {
	s := validSpec()
	s.CPULimit = "2"
	s.MemoryLimit = "4Gi"
	job := mustBuild(t, s)

	res := job.Spec.Template.Spec.Containers[0].Resources
	if res.Limits["cpu"] != "2" {
		t.Fatalf("spec 16.1: resources.limits.cpu = %q, want %q", res.Limits["cpu"], "2")
	}
	if res.Limits["memory"] != "4Gi" {
		t.Fatalf("spec 16.1: resources.limits.memory = %q, want %q", res.Limits["memory"], "4Gi")
	}
	if res.Requests["cpu"] != "2" {
		t.Fatalf("spec 16.1: resources.requests.cpu = %q, want it set", res.Requests["cpu"])
	}
	if res.Requests["memory"] != "4Gi" {
		t.Fatalf("spec 16.1: resources.requests.memory = %q, want it set", res.Requests["memory"])
	}
}

// --- Build: least-privilege credentials (spec 29) ------------------------

func TestBuild_EnvCarriesOnlyResolutionCredentials(t *testing.T) {
	s := validSpec()
	s.Env = map[string]policy.CredentialRef{
		"ANTHROPIC_API_KEY": {Secret: "anthropic-credentials", Key: "api-key"},
	}
	job := mustBuild(t, s)

	env := job.Spec.Template.Spec.Containers[0].Env

	var got *EnvVar
	for i := range env {
		if env[i].Name == "ANTHROPIC_API_KEY" {
			got = &env[i]
		}
		// An unrelated provider's variable must never appear: a Haiku
		// Claude Code Job should not receive an OpenAI key, and vice
		// versa (spec 29).
		if env[i].Name == "OPENAI_API_KEY" || env[i].Name == "CLAUDE_CODE_OAUTH_TOKEN" {
			t.Fatalf("spec 29: env contains %q, which this run's Resolution never declared", env[i].Name)
		}
	}
	if got == nil {
		t.Fatalf("spec 29: env does not contain declared credential %q", "ANTHROPIC_API_KEY")
	}
	if got.Value != "" {
		t.Fatalf("spec 29: credential %q has an inline value %q, want it sourced only from a secretKeyRef", "ANTHROPIC_API_KEY", got.Value)
	}
	if got.ValueFrom == nil || got.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("spec 29: credential %q is not a secretKeyRef", "ANTHROPIC_API_KEY")
	}
	if got.ValueFrom.SecretKeyRef.Name != "anthropic-credentials" || got.ValueFrom.SecretKeyRef.Key != "api-key" {
		t.Fatalf("spec 29: credential %q secretKeyRef = %+v, want secret %q key %q",
			"ANTHROPIC_API_KEY", got.ValueFrom.SecretKeyRef, "anthropic-credentials", "api-key")
	}
}

func TestBuild_EnvOmitsCredentialsWhenResolutionDeclaresNone(t *testing.T) {
	s := validSpec()
	s.Env = nil
	job := mustBuild(t, s)

	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			t.Fatalf("spec 29: env contains secretKeyRef %q even though the Resolution declared no credentials", e.Name)
		}
	}
}

// --- Build: findability ----------------------------------------------------

func TestBuild_LabelsCardAndRunID(t *testing.T) {
	s := validSpec()
	job := mustBuild(t, s)

	if job.Metadata.Labels["strangecompany.dev/card-id"] != s.CardID {
		t.Fatalf("spec 16.1: Job label card-id = %q, want %q — a human must be able to find this Job by card",
			job.Metadata.Labels["strangecompany.dev/card-id"], s.CardID)
	}
	if job.Metadata.Labels["strangecompany.dev/run-id"] != s.RunID {
		t.Fatalf("spec 16.1: Job label run-id = %q, want %q",
			job.Metadata.Labels["strangecompany.dev/run-id"], s.RunID)
	}
}

func TestBuild_NamespaceSetFromSpec(t *testing.T) {
	s := validSpec()
	s.Namespace = "agent-runs"
	job := mustBuild(t, s)
	if job.Metadata.Namespace != "agent-runs" {
		t.Fatalf("spec 16: metadata.namespace = %q, want %q", job.Metadata.Namespace, "agent-runs")
	}
}

// --- JSON -------------------------------------------------------------

func TestJSON_ProducesValidJSONWithExpectedKindAndAPIVersion(t *testing.T) {
	job := mustBuild(t, validSpec())
	data, err := job.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var round map[string]any
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v\n%s", err, data)
	}
	if round["kind"] != "Job" {
		t.Fatalf("kind = %v, want %q", round["kind"], "Job")
	}
	if round["apiVersion"] != "batch/v1" {
		t.Fatalf("apiVersion = %v, want %q", round["apiVersion"], "batch/v1")
	}
}

// --- AgentBranch (spec 16.2) ----------------------------------------------

func TestAgentBranch_Basic(t *testing.T) {
	got := AgentBranch("card-123", "Add dark mode toggle")
	want := "agent/card-123-add-dark-mode-toggle"
	if got != want {
		t.Fatalf("spec 16.2: AgentBranch() = %q, want %q", got, want)
	}
}

func TestAgentBranch_CollapsesPunctuationAndLowercases(t *testing.T) {
	got := AgentBranch("card-9", "Fix!!  the ***Bug*** (again)")
	want := "agent/card-9-fix-the-bug-again"
	if got != want {
		t.Fatalf("spec 16.2: AgentBranch() = %q, want %q", got, want)
	}
}

func TestAgentBranch_TruncatesLongTitle(t *testing.T) {
	longTitle := strings.Repeat("a", 200)
	got := AgentBranch("card-1", longTitle)

	want := "agent/card-1-" + strings.Repeat("a", maxSlugLen)
	if got != want {
		t.Fatalf("spec 16.2: AgentBranch() with a %d-character title = %q, want the slug truncated to %d characters: %q",
			len(longTitle), got, maxSlugLen, want)
	}
}

func TestAgentBranch_TruncationDoesNotLeaveTrailingHyphen(t *testing.T) {
	// Constructed so the untruncated slug is exactly one hyphen past
	// maxSlugLen: 49 a's, a hyphen, then more content. A naive
	// slug[:maxSlugLen] would end exactly on that hyphen.
	title := strings.Repeat("a", 49) + " " + strings.Repeat("b", 10)
	got := AgentBranch("card-1", title)

	if strings.HasSuffix(got, "-") {
		t.Fatalf("spec 16.2: AgentBranch() = %q, truncation must not leave a dangling trailing hyphen", got)
	}
	want := "agent/card-1-" + strings.Repeat("a", 49)
	if got != want {
		t.Fatalf("spec 16.2: AgentBranch() = %q, want %q", got, want)
	}
}

func TestAgentBranch_PunctuationOnlyTitleOmitsSlug(t *testing.T) {
	got := AgentBranch("card-42", "!!! --- ??? ...")
	want := "agent/card-42"
	if got != want {
		t.Fatalf("spec 16.2: AgentBranch() with an all-punctuation title = %q, want %q (no dangling hyphen for an empty slug)", got, want)
	}
}
