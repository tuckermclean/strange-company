package policy

import (
	"regexp"
	"strings"
)

// Error codes of the HarnessRegistry faults that the schema detects (plan 28 section 9, 28-AC-1). They
// accompany the POLICY_SCHEMA_INVALID issue of the same location; the document is rejected either way.
const (
	CodeHarnessImageNotPinned  = "HARNESS_IMAGE_NOT_DIGEST_PINNED"
	CodeHarnessNameReserved    = "HARNESS_NAME_RESERVED"
	CodeHarnessCredentialField = "HARNESS_PROVIDER_CREDENTIAL_FIELD"
	CodeHarnessScenarioUnknown = "HARNESS_FAKE_SCENARIO_UNKNOWN"
	CodeHarnessTelemetry       = "HARNESS_TELEMETRY_CONTENT_CAPTURE"
)

var (
	harnessImagePtr    = regexp.MustCompile(`^/spec/harnesses/[^/]+/image$`)
	harnessEntryPtr    = regexp.MustCompile(`^/spec/harnesses/[^/]+$`)
	harnessScenarioPtr = regexp.MustCompile(`^/spec/harnesses/[^/]+/fake/scenario$`)
	harnessTelemetry   = regexp.MustCompile(`^/spec/harnesses/[^/]+/defaults/environment/(OTEL_LOG_USER_PROMPTS|OTEL_LOG_TOOL_DETAILS|OTEL_EXPORTER_OTLP_(TRACES_|LOGS_)?ENDPOINT|CLAUDE_CODE_ENABLE_TELEMETRY)$`)
	harnessAdditional  = regexp.MustCompile(`^additional properties (.+) not allowed$`)
	harnessCredential  = regexp.MustCompile(`(?i)(secret|credential|password|passwd|token|api[-_]?key|private[-_]?key|authorization|bearer)`)
)

var harnessKnownName = map[string]bool{"tokenDelivery": true, "baseUrlEnv": true}

// harnessSchemaCode classifies one schema error of a HarnessRegistry document by instance location. A schema
// error that matches none of the faults stays POLICY_SCHEMA_INVALID, so no fault is ever dropped.
func harnessSchemaCode(ptr, msg string) string {
	switch {
	case harnessImagePtr.MatchString(ptr):
		return CodeHarnessImageNotPinned
	case ptr == "/spec/harnesses/hermes" && strings.HasPrefix(msg, "invalid propertyName 'hermes'"):
		return CodeHarnessNameReserved
	case harnessScenarioPtr.MatchString(ptr):
		return CodeHarnessScenarioUnknown
	case harnessTelemetry.MatchString(ptr):
		return CodeHarnessTelemetry
	case harnessEntryPtr.MatchString(ptr):
		if m := harnessAdditional.FindStringSubmatch(msg); m != nil {
			for _, n := range strings.Split(m[1], ", ") {
				n = strings.Trim(n, "'")
				// Names the entry schema itself defines (tokenDelivery lives under gateway) are misplaced, not
				// provider credentials.
				if !harnessKnownName[n] && harnessCredential.MatchString(n) {
					return CodeHarnessCredentialField
				}
			}
			return CodeSchemaInvalid
		}
	}
	return CodeSchemaInvalid
}
