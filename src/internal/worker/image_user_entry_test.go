package worker_test

import (
	"regexp"
	"strings"
	"testing"
)

// S6e (28-AC-3, plan 28 section 13.1, 27-AC-5): the image runs as uid/gid 65532 with home /home/worker. Docker, containerd
// and the kubelet synthesize no passwd/group entry for --user (rootless podman does), and OpenSSH's ssh exits "No user exists
// for uid 65532" without one, so the final stage must define both entries itself, before `USER 65532:65532`.

// checkUserEntry returns the problems with the passwd/group entry of uid/gid 65532 in the final stage.
func checkUserEntry(text string) []string {
	stages := parseContainerfile(text)
	if len(stages) == 0 {
		return []string{"the Containerfile has no FROM"}
	}
	var before []string
	userSeen := false
	for _, l := range stages[len(stages)-1].lines {
		if regexp.MustCompile(`(?i)^USER\s+"?65532(:65532)?"?\s*$`).MatchString(strings.TrimSpace(l)) {
			userSeen = true
			break
		}
		before = append(before, l)
	}
	if !userSeen {
		return []string{"the final stage has no `USER 65532:65532`"}
	}
	// one string per RUN, shell fragments split at ; && || | and newlines
	var cmds []string
	for _, l := range before {
		if f := strings.Fields(l); len(f) > 1 && strings.EqualFold(f[0], "RUN") {
			cmds = append(cmds, regexp.MustCompile(`\s*(;|&&|\|\||\n)\s*`).Split(strings.Join(f[1:], " "), -1)...)
		}
	}
	all := strings.Join(cmds, "\n")
	gid := `(-g|--gid)[ =]'?"?65532\b`
	group := regexp.MustCompile(`(?m)\b(groupadd|addgroup)\b.*`+gid).MatchString(all) ||
		regexp.MustCompile(`(?m)\b[a-z_][a-z0-9_-]*:[^:\s'"]*:65532:[^\n]*>>?\s*/etc/group|/etc/group.*\b[a-z_][a-z0-9_-]*:[^:\s'"]*:65532:`).MatchString(all)
	passwdUseradd := false
	for _, c := range cmds {
		if regexp.MustCompile(`\buseradd\b`).MatchString(c) &&
			regexp.MustCompile(`(-u|--uid)[ =]'?"?65532\b`).MatchString(c) &&
			regexp.MustCompile(`(-d|--home-dir|--home)[ =]'?"?/home/worker\b`).MatchString(c) &&
			(regexp.MustCompile(gid).MatchString(c) || regexp.MustCompile(`(-g|--gid)[ =]\w`).MatchString(c) || group) {
			passwdUseradd = true
		}
	}
	passwdExplicit := regexp.MustCompile(`\b[a-z_][a-z0-9_-]*:[^:\s'"]*:65532:65532:[^:]*:/home/worker:[^\s'"]*`).MatchString(all) &&
		regexp.MustCompile(`/etc/passwd`).MatchString(all)
	var bad []string
	if !passwdUseradd && !passwdExplicit {
		bad = append(bad, "the final stage gives uid 65532 no passwd entry with home /home/worker before USER (useradd -u 65532 -g 65532 -d /home/worker, or a name:x:65532:65532::/home/worker:shell line added to /etc/passwd)")
	}
	if !group {
		bad = append(bad, "the final stage gives gid 65532 no group entry before USER (groupadd -g 65532, or a name:x:65532: line added to /etc/group)")
	}
	return bad
}

func TestHarnessFakeImage_UserHasPasswdAndGroupEntry_28AC3(t *testing.T) {
	text, ok := readRepoFile(t, imageDir+"/Containerfile")
	if !ok {
		return
	}
	for _, msg := range checkUserEntry(text) {
		t.Error(msg)
	}
}

// Model and mutation guard for checkUserEntry (passes now).
func TestHarnessImageRules_UserEntryCheckerCatchesViolations_28AC3(t *testing.T) {
	debian := lockDigest(t, "debian")
	mk := func(run string) string {
		return "FROM docker.io/library/debian@" + debian + "\n" + run + "\nUSER 65532:65532\nWORKDIR /workspace\n"
	}
	good := map[string]string{
		"useradd":              mk("RUN set -eu; \\\n    groupadd -g 65532 worker; \\\n    useradd -u 65532 -g 65532 -d /home/worker -M -s /usr/sbin/nologin worker"),
		"useradd long options": mk("RUN groupadd --gid 65532 worker && useradd --uid 65532 --gid worker --home-dir /home/worker --no-create-home worker"),
		"explicit files":       mk("RUN set -eu; \\\n    echo 'worker:x:65532:' >> /etc/group; \\\n    echo 'worker:x:65532:65532::/home/worker:/usr/sbin/nologin' >> /etc/passwd"),
		"printf":               mk("RUN printf 'worker:x:65532:\\n' >>/etc/group && printf 'worker:x:65532:65532::/home/worker:/usr/sbin/nologin\\n' >>/etc/passwd"),
	}
	for name, m := range good {
		if bad := checkUserEntry(m); len(bad) != 0 {
			t.Errorf("good form %q is rejected: %v", name, bad)
		}
	}
	base := good["useradd"]
	for name, m := range map[string]string{
		"no entry":          mk("RUN mkdir -p /home/worker"),
		"no group":          strings.Replace(base, "groupadd -g 65532 worker; \\\n    ", "", 1),
		"no passwd":         strings.Replace(base, "useradd -u 65532 -g 65532 -d /home/worker -M -s /usr/sbin/nologin worker", "true", 1),
		"wrong uid":         strings.Replace(base, "useradd -u 65532", "useradd -u 1000", 1),
		"wrong home":        strings.Replace(base, "-d /home/worker", "-d /home/other", 1),
		"after USER":        "FROM docker.io/library/debian@" + debian + "\nUSER 65532:65532\n" + strings.Replace(base, "FROM docker.io/library/debian@"+debian+"\n", "", 1),
		"only group file":   mk("RUN echo 'worker:x:65532:' >> /etc/group"),
		"only passwd file":  mk("RUN echo 'worker:x:65532:65532::/home/worker:/bin/false' >> /etc/passwd"),
		"passwd wrong home": mk("RUN echo 'worker:x:65532:' >> /etc/group; echo 'worker:x:65532:65532::/nonexistent:/bin/false' >> /etc/passwd"),
	} {
		if m == base || len(checkUserEntry(m)) == 0 {
			t.Errorf("mutation %q is not caught", name)
		}
	}
}
