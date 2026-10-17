package toolbox

import (
	"fmt"
	"os"
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

func (t lockTool) expandURL(arch string) string {
	return strings.NewReplacer("{version}", t.Ver, "{arch}", arch).Replace(t.URL)
}
