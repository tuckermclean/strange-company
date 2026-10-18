package toolbox

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var envLine = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)

// parseEnvFile reads the plan 50 section 9.1 grammar: KEY=VALUE lines matching ^[A-Z][A-Z0-9_]*=, optional single
// or double quotes, no expansion of any kind. Other lines assign nothing. Empty values count as unset.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !envLine.MatchString(line) {
			continue // outside ^[A-Z][A-Z0-9_]*= : comments, blanks and malformed lines assign nothing
		}
		k, v, _ := strings.Cut(line, "=")
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if v != "" {
			out[k] = v
		} else {
			delete(out, k) // last assignment wins; an empty value unsets an earlier one
		}
	}
	return out, sc.Err()
}

func (c *cmdCtx) loadEnvFile() (map[string]string, error) {
	p := c.envFile
	if p == "" {
		p = c.getenv("SC_INPUT_DIR", "/run/sc-input") + "/bootstrap.env"
	}
	return parseEnvFile(p)
}
