package toolbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envCmd implements `sc-bootstrap env <scope>`. Only the s3 scope exists in this build; kube and unlock need
// the black-start kubeconfig and the recovery bundle (plan 50 section 10.2).
func (c *cmdCtx) envCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(c.err, "usage: sc-bootstrap [--env-file <file>] env s3")
		return 2
	}
	if args[0] != "s3" {
		fmt.Fprintf(c.err, "sc-bootstrap env: scope %q is not available in this build\n", args[0])
		return 2
	}
	vars, err := c.loadEnvFile()
	if err != nil {
		return c.fail(err)
	}
	var missing []string
	for _, k := range []string{"S3_ENDPOINT", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"} {
		if vars[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(c.err, "sc-bootstrap env s3: missing %s\n", strings.Join(missing, ", "))
		return 2
	}
	dir := filepath.Join(c.getenv("SC_RUN_DIR", "/run/sc"), "aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return c.fail(err)
	}
	var cfg strings.Builder
	cfg.WriteString("[profile sc-admin]\n")
	if r := vars["S3_REGION"]; r != "" {
		fmt.Fprintf(&cfg, "region = %s\n", r)
	}
	fmt.Fprintf(&cfg, "endpoint_url = %s\n", vars["S3_ENDPOINT"])
	if strings.EqualFold(vars["S3_FORCE_PATH_STYLE"], "true") {
		cfg.WriteString("s3 =\n    addressing_style = path\n")
	}
	cred := fmt.Sprintf("[sc-admin]\naws_access_key_id = %s\naws_secret_access_key = %s\n", vars["S3_ACCESS_KEY_ID"], vars["S3_SECRET_ACCESS_KEY"])
	cfgPath, credPath := filepath.Join(dir, "config"), filepath.Join(dir, "credentials")
	for p, body := range map[string]string{cfgPath: cfg.String(), credPath: cred} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return c.fail(err)
		}
		if err := os.Chmod(p, 0o600); err != nil {
			return c.fail(err)
		}
	}
	fmt.Fprintf(c.out, "export AWS_CONFIG_FILE=%s\nexport AWS_SHARED_CREDENTIALS_FILE=%s\n", shQuote(cfgPath), shQuote(credPath))
	return 0
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
