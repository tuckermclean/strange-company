package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// listFiles returns the slash-separated relative paths of the regular files of the tree under root: the
// tracked and untracked-but-not-ignored files when root is a Git work tree, else every file except .git, out,
// .cache and .bootstrap.
func listFiles(root string) ([]string, error) {
	var out []string
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		cmd := exec.Command("git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
		var b, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &b, &e
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git ls-files: %v: %s", err, strings.TrimSpace(e.String()))
		}
		for _, p := range strings.Split(b.String(), "\x00") {
			if p == "" {
				continue
			}
			if st, err := os.Lstat(filepath.Join(root, p)); err == nil && st.Mode().IsRegular() {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	skip := map[string]bool{".git": true, "out": true, ".cache": true, ".bootstrap": true}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if rel != "." && skip[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func hasPrefixDir(p string, dirs ...string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// yamlDocs decodes every document of a YAML stream into generic values.
func yamlDocs(b []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var docs []any
	for {
		var v any
		err := dec.Decode(&v)
		if err != nil {
			if err.Error() == "EOF" {
				return docs, nil
			}
			return docs, err
		}
		if v != nil {
			docs = append(docs, v)
		}
	}
}

func readYAML(root, rel string) (any, error) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// at walks nested maps by key; it returns nil when a step is missing.
func at(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// str renders a scalar the way it was written (1.36 stays "1.36"); non-scalars give "".
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any, []any:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// assetPath finds a repository asset (schema, helper script). The tool's own checkout (found from the working
// directory upwards) wins over root, so fixture trees need not carry copies and cannot substitute code.
func assetPath(root, rel string) (string, error) {
	if d, err := os.Getwd(); err == nil {
		for {
			if _, err := os.Stat(filepath.Join(d, "SPEC.md")); err == nil {
				if p := filepath.Join(d, rel); fileExists(p) {
					return p, nil
				}
			}
			if p := filepath.Dir(d); p != d {
				d = p
				continue
			}
			break
		}
	}
	if p := filepath.Join(root, rel); fileExists(p) {
		return p, nil
	}
	return "", fmt.Errorf("%s not found in the repository", rel)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

type schemaViolation struct {
	File    string `json:"file"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// validateSchema runs test/contracts/validate_schema.py (Stage-0 venv of the ci image) over items
// ("<abs file>:<kind>") against contracts/<schemaFile>; defName is a $defs name, "-" or "@kind".
func validateSchema(root, schemaFile, defName string, items []string) ([]schemaViolation, error) {
	script, err := assetPath(root, "test/contracts/validate_schema.py")
	if err != nil {
		return nil, err
	}
	schema, err := assetPath(root, "contracts/"+schemaFile)
	if err != nil {
		return nil, err
	}
	python := os.Getenv("SC_STAGE0_PYTHON")
	if python == "" {
		python = "/opt/sc/stage0-venv/bin/python"
	}
	cmd := exec.Command(python, append([]string{"-I", script, schema, defName}, items...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("schema validation (%s): %v: %s", python, err, strings.TrimSpace(errb.String()))
	}
	var vs []schemaViolation
	if err := json.Unmarshal(out.Bytes(), &vs); err != nil {
		return nil, fmt.Errorf("schema validation output: %v", err)
	}
	return vs, nil
}
