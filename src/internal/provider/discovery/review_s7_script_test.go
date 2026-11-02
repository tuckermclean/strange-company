package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// S7 review findings on the discovery script (05-AC-2 for the document it prints, 05-AC-3 for the tool list). The tests
// run discovery.Script() with sh in a temporary empty HOME and working directory; the ci image that runs make test
// provides lsblk, findmnt, ip and the rest.

// runScript runs the script with sh and the given PATH and returns the decoded JSON document.
func runScript(t *testing.T, path string) map[string]any {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	script := requireScript(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-s")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + dir, "LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sh -s failed: %v\nstderr:\n%.1000s", err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the script's stdout is not one JSON object: %v\nstdout:\n%.2000s", err, stdout.String())
	}
	return doc
}

func missingTools(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	l, ok := doc["missing_tools"].([]any)
	if !ok {
		t.Fatalf("the document has no missing_tools list: %v", doc)
	}
	out := map[string]bool{}
	for _, v := range l {
		s, _ := v.(string)
		out[s] = true
	}
	return out
}

// 05-AC-2: the script asks findmnt for the columns the parser reads (-o TARGET,SOURCE,FSTYPE,SIZE,AVAIL), so its
// findmnt.filesystems[0] carries size and avail as JSON numbers (findmnt -b prints bytes as numbers).
func TestScriptOutput_FindmntCarriesNumericSizeAndAvail_05AC2(t *testing.T) {
	doc := runScript(t, os.Getenv("PATH"))
	fm, ok := doc["findmnt"].(map[string]any)
	if !ok {
		t.Fatalf("findmnt is %v, want an object (the ci image has findmnt)", doc["findmnt"])
	}
	fss, _ := fm["filesystems"].([]any)
	if len(fss) == 0 {
		t.Fatalf("findmnt.filesystems is empty: %v", fm)
	}
	fs, _ := fss[0].(map[string]any)
	for _, k := range []string{"size", "avail"} {
		v, present := fs[k]
		if !present {
			t.Errorf("findmnt.filesystems[0] has no %q (findmnt must run with -o TARGET,SOURCE,FSTYPE,SIZE,AVAIL): %v", k, fs)
			continue
		}
		if _, isNum := v.(float64); !isNum {
			t.Errorf("findmnt.filesystems[0].%s = %#v, want a JSON number", k, v)
		}
	}
}

// 05-AC-3 (and plan 05 section 14.2): a tool the script runs that the node lacks is listed in missing_tools instead of
// producing an empty or guessed fact. With the full PATH none of them is listed (ci image); with one of them removed
// from PATH it is.
func TestScriptOutput_MissingToolsCoverEveryExternalTool_05AC3(t *testing.T) {
	full := missingTools(t, runScript(t, os.Getenv("PATH")))
	tools := []string{"od", "awk", "sed", "tr", "head", "grep", "cat"}
	for _, tool := range tools {
		if full[tool] {
			t.Errorf("%s is listed in missing_tools although it is on PATH", tool)
		}
	}
	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			dir := t.TempDir()
			for _, src := range []string{"/usr/bin", "/bin"} {
				ents, err := os.ReadDir(src)
				if err != nil {
					continue
				}
				for _, e := range ents {
					if e.Name() == tool {
						continue
					}
					_ = os.Symlink(filepath.Join(src, e.Name()), filepath.Join(dir, e.Name())) // duplicates are fine
				}
			}
			got := missingTools(t, runScript(t, dir))
			if !got[tool] {
				t.Errorf("%s is not on PATH but missing_tools is %v: the script uses %s without checking for it", tool, got, tool)
			}
		})
	}
}
