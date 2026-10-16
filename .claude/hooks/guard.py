"""Phase-1 role guard for Strange Company coding agents (CD-44; plan 01 §6.1).

Invoked as a PreToolUse hook from the role subagents in .claude/agents/:

    python3 -I "$CLAUDE_PROJECT_DIR/.claude/hooks/guard.py" <tester|implementer|reviewer>

and, for every session in this repository, from .claude/settings.json with the
role `session`: agents never merge, approve or change repository settings on
GitHub (CD-46). Only the operator merges.

It reads the hook JSON on stdin and exits 2 (with the reason on stderr) to block
a call. It is a guardrail for cooperative agents, not enforcement: the
enforcement is interim CI (`tdd-discipline`, plan 52 §8.3) and human review.
Python by the same exception as tools/stage0 (ADR-0021); it ships in no image.
"""
from __future__ import annotations

import fnmatch
import json
import os
import re
import subprocess
import sys

# plan 52 §8.3 `policy/ci.yaml` spec.tddDiscipline.testPaths
TEST_PATHS = ["**/*_test.go", "**/testdata/**", "test/**", "charts/*/tests/**",
              "src/internal/contracts/testdata/**"]
# Never edited by a role agent: the orchestrator or a human owns these.
ROLE_FORBIDDEN = ["SPEC.md", "LICENSE", "PLAN.md", "STATUS.md", "plans/**", "adr/**",
                  "procedures/**", ".claude/**", "tools/stage0/**", ".github/**",
                  "**/*.sops.yaml", "secrets/**"]
STUB_MARK = "SC-STUB("
HISTORY_REWRITE = re.compile(r"\bgit\s+(push|rebase|filter-branch|filter-repo)\b|"
                             r"\bgit\s+reset\s+--hard\b|\bgit\s+commit\b[^|;&]*--amend\b|"
                             r"--no-verify\b")
# CD-46: the agent bot opens PRs; only the operator approves and merges.
GH_FORBIDDEN = re.compile(r"\bgh\s+(pr\s+(merge|review)|repo\s+(edit|delete|rename|archive)|"
                          r"release\s+(create|delete|edit)|secret|variable|ruleset)\b|"
                          r"\bgh\s+api\b[^|;&]*(/merge\b|/reviews\b|/protection\b|/rulesets\b|"
                          r"mergePullRequest|addPullRequestReview|enablePullRequestAutoMerge)")
# CD-47: the only commit status a session may post is `sc-review` (plan 52 §8.2a).
STATUS_POST = re.compile(r"/statuses/")
# Shell writes into ROLE_FORBIDDEN paths (operator decision after the F01 S3 review: an implementer wrote
# .github/** through the shell). Heuristic: redirect targets and the operands of file-writing commands.
REDIRECT = re.compile(r"(?<![<>&\d])\d*>>?\|?\s*([^\s;&|<>()]+)")
WRITE_CMDS = re.compile(r"^(?:sudo\s+)?(?:env\s+(?:\S+=\S*\s+)*)?(cp|mv|tee|rm|rmdir|touch|mkdir|ln|install|"
                        r"truncate|chmod|chown|dd|rsync|unzip|tar|patch)\b")
SED_INPLACE = re.compile(r"^(?:sudo\s+)?sed\b.*\s-[a-zA-Z]*i")
GIT_WRITES = re.compile(r"^git\s+(?:-C\s+\S+\s+)?(checkout|restore|apply|am|cherry-pick|revert|mv|rm|"
                        r"update-index|stash)\b")
PY_WRITE = re.compile(r"""open\(\s*['"]([^'"]+)['"]\s*,\s*['"][wax]|Path\(\s*['"]([^'"]+)['"]\s*\)\s*\."""
                      r"""(write_text|write_bytes|unlink|rename|replace|touch|mkdir)""")


def match(path: str, patterns: list[str]) -> bool:
    for p in patterns:
        if fnmatch.fnmatchcase(path, p):
            return True
        if p.startswith("**/") and fnmatch.fnmatchcase(path, p[3:]):
            return True
    return False


def deny(reason: str) -> None:
    sys.stderr.write(f"sc-guard: {reason}\n")
    sys.exit(2)


def rel(root: str, path: str) -> str:
    return os.path.relpath(os.path.abspath(os.path.join(root, path)), root).replace(os.sep, "/")


def is_stub_addition(path: str, tool: str, ti: dict, root: str) -> bool:
    """A tester may add declared stubs (plan 52 §8.3a) to Go sources: additions only, marked."""
    if not (path.startswith("src/") and path.endswith(".go")):
        return False
    if tool == "Edit":
        old, new = ti.get("old_string", ""), ti.get("new_string", "")
        return old in new and STUB_MARK in new.replace(old, "", 1)
    if tool == "Write":
        return not os.path.exists(os.path.join(root, path)) and STUB_MARK in ti.get("content", "")
    return False


def staged(root: str) -> list[tuple[int, int, str]]:
    out = subprocess.run(["git", "-C", root, "diff", "--cached", "--numstat"],
                         capture_output=True, text=True, check=False).stdout
    rows = []
    for line in out.splitlines():
        a, d, p = line.split("\t", 2)
        rows.append((int(a) if a != "-" else 0, int(d) if d != "-" else 0, p))
    return rows


def forbidden_shell_write(cmd: str, root: str) -> str | None:
    """Return a ROLE_FORBIDDEN path the command appears to write, or None."""
    def bad(token: str) -> bool:
        token = token.strip("'\"")
        if not token or token.startswith("-") or token.startswith("/dev/"):
            return False
        path = rel(root, token)
        return not path.startswith("../") and match(path, ROLE_FORBIDDEN)

    for target in REDIRECT.findall(cmd):
        if bad(target):
            return target
    for m in PY_WRITE.finditer(cmd):
        target = m.group(1) or m.group(2)
        if bad(target):
            return target
    for simple in re.split(r"&&|\|\||[;|\n]", cmd):
        simple = simple.strip().lstrip("(").strip()
        if WRITE_CMDS.match(simple) or SED_INPLACE.match(simple) or GIT_WRITES.match(simple):
            for token in simple.split()[1:]:
                if bad(token):
                    return token
    return None


def check_commit(role: str, root: str) -> None:
    rows = staged(root)
    for _added, deleted, path in rows:
        if match(path, ROLE_FORBIDDEN):
            deny(f"{role} may not commit {path} (orchestrator/human-owned path)")
    if role == "tester":
        for _added, deleted, path in rows:
            if match(path, TEST_PATHS):
                continue
            diff = subprocess.run(["git", "-C", root, "diff", "--cached", "--", path],
                                  capture_output=True, text=True, check=False).stdout
            if path.startswith("src/") and path.endswith(".go") and deleted == 0 and STUB_MARK in diff:
                continue
            deny(f"RED commit may change only test paths or add declared stubs; {path} is neither "
                 "(plan 52 §8.3 T3, §8.3a)")
    if role == "implementer":
        for _added, deleted, path in rows:
            if match(path, TEST_PATHS) and deleted > 0:
                deny(f"implementer may not modify existing test file {path} after RED "
                     "(SPEC §53; plan 52 §8.3 T7/T8). Report a test/spec conflict instead.")


def main() -> None:
    role = sys.argv[1] if len(sys.argv) > 1 else ""
    if role not in ("tester", "implementer", "reviewer", "session"):
        deny(f"unknown role {role!r}")
    ev = json.load(sys.stdin)
    root = os.environ.get("CLAUDE_PROJECT_DIR") or ev.get("cwd") or os.getcwd()
    tool, ti = ev.get("tool_name", ""), ev.get("tool_input") or {}

    if role == "session":
        if tool == "Bash" and GH_FORBIDDEN.search(ti.get("command", "")):
            deny("agents never merge, approve or change repository settings on GitHub; "
                 "the operator does (CD-46, plan 52 §8.2)")
        cmd = ti.get("command", "") if tool == "Bash" else ""
        if STATUS_POST.search(cmd):
            contexts = re.findall(r"context=['\"]?([^\s'\"]*)", cmd)
            if not contexts or any(c != "sc-review" for c in contexts):
                deny("the only commit status a session may post is context=sc-review (CD-47, plan 52 §8.2a)")
        return

    if tool in ("Edit", "Write", "NotebookEdit"):
        if role == "reviewer":
            deny("the reviewer does not modify files")
        path = rel(root, ti.get("file_path") or ti.get("notebook_path") or "")
        if path.startswith("../"):
            deny(f"{path} is outside the repository")
        if match(path, ROLE_FORBIDDEN):
            deny(f"{role} may not edit {path} (orchestrator/human-owned path)")
        if role == "tester" and not match(path, TEST_PATHS) and not is_stub_addition(path, tool, ti, root):
            deny(f"tester may edit only test paths {TEST_PATHS} or add `// SC-STUB(<node> <AC>)` "
                 f"declared stubs under src/ (plan 52 §8.3a); {path} is neither")
        if role == "implementer" and match(path, TEST_PATHS) and os.path.exists(os.path.join(root, path)):
            deny(f"implementer may not edit existing test file {path}; tests are fixed at RED (SPEC §53). "
                 "If the test is wrong, stop and report TEST_SPEC_CONFLICT.")
        return

    if tool == "Bash":
        cmd = ti.get("command", "")
        if re.search(r"(^|[\s;&|(])gh\s", cmd):
            deny("role agents do not use gh; the orchestrator opens PRs")
        if HISTORY_REWRITE.search(cmd):
            deny("role agents do not push or rewrite history; the orchestrator pushes (RED evidence must stay addressable)")
        if role == "reviewer" and re.search(r"\bgit\s+(commit|add|checkout\s+-b|switch\s+-c|stash)\b", cmd):
            deny("the reviewer does not change the repository")
        if role in ("tester", "implementer"):
            hit = forbidden_shell_write(cmd, root)
            if hit:
                deny(f"{role} may not write {hit} through the shell (orchestrator/human-owned path)")
        if re.search(r"\bgit\s+commit\b", cmd):
            # The guard inspects the index before the command runs, so staging must already be done.
            if re.search(r"\bgit\s+commit\b[^|;&]*(\s-[a-zA-Z]*a[a-zA-Z]*\b|--all\b)", cmd) or re.search(r"\bgit\s+(add|rm|mv)\b", cmd):
                deny("stage with `git add` in a separate command, then run `git commit` without -a")
            check_commit(role, root)


if __name__ == "__main__":
    main()
