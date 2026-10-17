package toolbox

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseEnvFile reads the plan 50 section 9.1 grammar: KEY=VALUE lines, # comments, optional single or double
// quotes, no expansion of any kind. Empty values count as unset.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if v != "" {
			out[k] = v
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
