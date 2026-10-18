package toolbox

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type lockTool struct {
	Name   string            `yaml:"name"`
	Set    string            `yaml:"set"`
	Ver    string            `yaml:"version"`
	URL    string            `yaml:"url"`
	SHA256 map[string]string `yaml:"sha256"`
	Inst   struct {
		Extract string `yaml:"extract"`
		To      string `yaml:"to"`
	} `yaml:"install"`
	VersionCommand []string `yaml:"versionCommand"`
	VersionRegex   string   `yaml:"versionRegex"`
}

type toolLock struct {
	Spec struct {
		Tools []lockTool `yaml:"tools"`
	} `yaml:"spec"`
}

func readLock(path string) (*toolLock, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var l toolLock
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, b, nil
}

// archMap matches {arch:amd64=x86_64,arm64=aarch64}: a vendor's own name for each architecture.
var archMap = regexp.MustCompile(`\{arch:([^{}]*)\}`)

// expand substitutes {version}, {arch} and {arch:amd64=<name>,arm64=<name>} (as fetch-pinned did in url and
// install.extract). A map token without an entry for arch leaves the token in place, so the URL stays invalid
// instead of silently naming another platform's artifact.
func (t lockTool) expand(s, arch string) string {
	s = archMap.ReplaceAllStringFunc(s, func(tok string) string {
		for _, kv := range strings.Split(archMap.FindStringSubmatch(tok)[1], ",") {
			if k, v, ok := strings.Cut(kv, "="); ok && k == arch {
				return v
			}
		}
		return tok
	})
	return strings.NewReplacer("{version}", t.Ver, "{arch}", arch).Replace(s)
}

func (t lockTool) expandURL(arch string) string { return t.expand(t.URL, arch) }
