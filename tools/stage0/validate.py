#!/usr/bin/env python3
"""Stage-0 validator for the Strange Company engineering definition (ADR-0021, CD-27).

Deterministically performs every structural check that STATUS.md claims. Exit 0
only if every check passes. No network access, no clock, no randomness.

Usage:
    python3 tools/stage0/validate.py [--repo PATH] [--json]

Checks (each prints PASS/FAIL with details):
  C01 contracts       every *.schema.yaml is a valid JSON Schema 2020-12 with $id;
                      every `examples` item validates; every `x-invalid-examples`
                      value fails; cross-file $ref resolve via $id.
  C02 openapi         every *.openapi.yaml is OpenAPI 3.1, its component schemas are
                      valid 2020-12 schemas and all local $refs resolve.
  C03 data-files      data files validate against their schemas (state machine,
                      secret register, alert catalogue, compatibility baseline).
  C04 state-machine   per mode: every state reachable from the initial state and
                      every non-terminal state can reach a terminal state.
  C05 plan-sections   every subsystem plan has the 25 SPEC §121 sections in order.
  C06 dag             plans/dag.yaml: unique ids, known deps, acyclic, milestones
                      reference nodes; every listed plan has a GATE-<node> section;
                      every GATE-<node> section's plan is listed on that node;
                      every node has >= 1 acceptance criterion.
  C07 ac-ids          acceptance-criterion IDs (<NN>-AC-<n>) are unique per plan
                      and use the plan's own number.
  C08 references      no dangling `procedures/*.md`, `contracts/*`, `adr/NNNN` or
                      `plans/NN-*` references anywhere in the definition.
  C09 alerts          every `SC*` alert name referenced resolves in the alert
                      catalogue (name or alias); every catalogue runbook exists.
  C10 secrets         every SC-* secret-class token resolves in the register (or an
                      alias recorded in it, or a documented non-secret pattern).
  C11 inventories     master plan §10 lists every contract file and §12 every
                      procedure, and nothing that does not exist.
  C12 plan-md         PLAN.md equals the output of tools/stage0/genplan.py.
  C13 blocking        no open question in any plan is marked blocking: yes.
  C14 spec-issues     every SPEC issue row has a non-empty resolution and a
                      blocking column that is not "Yes".
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import re
import subprocess
import sys
import tempfile

import yaml
from jsonschema import Draft202012Validator as V
from referencing import Registry, Resource

SECTIONS = [
    "Purpose", "Requirements derived from SPEC", "Inputs", "Outputs", "Dependencies",
    "Owned state", "External interfaces", "Internal interfaces", "Configuration",
    "Secrets", "Storage", "Resource requirements", "Security boundary", "Happy path",
    "Failure modes", "Retry behavior", "Idempotency behavior", "Bootstrap behavior",
    "Upgrade behavior", "Recovery behavior", "Observability", "Testing strategy",
    "Acceptance criteria", "Explicit non-goals", "Open questions",
]

DATA_FILES = {
    "state-machine.yaml": "state-machine.schema.yaml",
    "secret-classes.yaml": "secret-classes.schema.yaml",
    "alert-catalog.yaml": "alert-catalog.schema.yaml",
}
OPTIONAL_DATA_FILES = {
    "../plans/compatibility-baseline.yaml": "compatibility-baseline.schema.yaml",
}

# Tokens that look like secret classes but are documented non-secret identifiers
# (network-matrix row IDs, marker strings, family prefixes used in prose).
NON_SECRET_TOKEN_PATTERNS = [
    r"SC-\d+[A-Z]?",              # plan 36 allow-matrix row IDs (SC-1, SC-5a ...)
    r"SC-CANARY(-[A-Z0-9]+)*",    # test canary strings (plans 35, 40, 41, 53)
    r"SC-STUB(-[A-Z0-9]+)*",      # declared stub markers (ADR-0352)
    r"SC-PARTIAL-DIFF(-[A-Z0-9]+)*",
    r"SC-RESULT-(BEGIN|END)",     # worker result framing (plan 27)
    r"SC-CLASS",                  # placeholder in ADR-0101 / plan 50 text
    r"SC-BREAK-GLASS",            # family prefix for SC-BREAK-GLASS-* classes
    r"SC-AUTHENTIK",              # family name in credential-compromise.md
    r"SC-OBJECT-STORAGE-[A-Z]+",  # legacy per-bucket spelling, aliased in the register
]


class Result:
    def __init__(self) -> None:
        self.checks: dict[str, list[str]] = {}
        self.stats: dict[str, object] = {}

    def add(self, check: str, msg: str | None = None) -> None:
        self.checks.setdefault(check, [])
        if msg:
            self.checks[check].append(msg)

    @property
    def ok(self) -> bool:
        return all(not v for v in self.checks.values())


def load_yaml(path: str):
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


def read(path: str) -> str:
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def plan_files(repo: str) -> list[str]:
    return sorted(
        f for f in glob.glob(os.path.join(repo, "plans", "[0-9][0-9]-*.md"))
        if not f.endswith("00-master-plan.md")
    )


# ---------------------------------------------------------------- C01/C03
def check_contracts(repo: str, r: Result) -> dict:
    r.add("C01 contracts")
    r.add("C03 data-files")
    schemas: dict[str, dict] = {}
    for f in sorted(glob.glob(os.path.join(repo, "contracts", "*.schema.yaml"))):
        name = os.path.basename(f)
        try:
            s = load_yaml(f)
            V.check_schema(s)
        except Exception as e:  # noqa: BLE001
            r.add("C01 contracts", f"{name}: not a valid 2020-12 schema: {str(e)[:200]}")
            continue
        if "$id" not in s:
            r.add("C01 contracts", f"{name}: missing $id")
        schemas[name] = s
    registry = Registry().with_resources(
        [(s["$id"], Resource.from_contents(s)) for s in schemas.values() if "$id" in s]
    )
    n_ex = n_bad = 0
    for name, s in schemas.items():
        v = V(s, registry=registry)
        examples = s.get("examples") or []
        if not examples:
            r.add("C01 contracts", f"{name}: no examples")
        for i, ex in enumerate(examples):
            n_ex += 1
            try:
                errs = list(v.iter_errors(ex))
            except Exception as e:  # noqa: BLE001
                r.add("C01 contracts", f"{name}: example {i}: unresolvable reference: {str(e)[:160]}")
                continue
            if errs:
                r.add("C01 contracts", f"{name}: example {i} invalid: {errs[0].message[:200]} at {list(errs[0].absolute_path)}")
        for i, bad in enumerate(s.get("x-invalid-examples") or []):
            n_bad += 1
            value = bad.get("value") if isinstance(bad, dict) and "value" in bad else bad
            try:
                errs = list(v.iter_errors(value))
            except Exception as e:  # noqa: BLE001
                r.add("C01 contracts", f"{name}: x-invalid-example {i}: unresolvable reference: {str(e)[:160]}")
                continue
            if not errs:
                reason = bad.get("reason", "") if isinstance(bad, dict) else ""
                r.add("C01 contracts", f"{name}: x-invalid-example {i} unexpectedly valid ({reason[:100]})")
    r.stats.update(schemas=len(schemas), schema_examples=n_ex, schema_invalid_examples=n_bad)

    for data, schema in list(DATA_FILES.items()) + list(OPTIONAL_DATA_FILES.items()):
        dpath = os.path.normpath(os.path.join(repo, "contracts", data))
        optional = data in OPTIONAL_DATA_FILES
        if not os.path.exists(dpath):
            if not optional:
                r.add("C03 data-files", f"missing data file {data}")
            continue
        if schema not in schemas:
            r.add("C03 data-files", f"{data}: schema {schema} missing or invalid")
            continue
        errs = list(V(schemas[schema], registry=registry).iter_errors(load_yaml(dpath)))
        for e in errs[:5]:
            r.add("C03 data-files", f"{data}: {e.message[:200]} at {list(e.absolute_path)}")
    return schemas


# ---------------------------------------------------------------- C02
def check_openapi(repo: str, r: Result) -> None:
    r.add("C02 openapi")
    for f in sorted(glob.glob(os.path.join(repo, "contracts", "*.openapi.yaml"))):
        name = os.path.basename(f)
        try:
            doc = load_yaml(f)
        except Exception as e:  # noqa: BLE001
            r.add("C02 openapi", f"{name}: YAML error {e}")
            continue
        if not str(doc.get("openapi", "")).startswith("3.1"):
            r.add("C02 openapi", f"{name}: openapi version is not 3.1")
        comps = (doc.get("components") or {}).get("schemas") or {}
        for cname, sch in comps.items():
            try:
                V.check_schema(sch)
            except Exception as e:  # noqa: BLE001
                r.add("C02 openapi", f"{name}: components.schemas.{cname}: {str(e)[:160]}")

        def walk(node):
            if isinstance(node, dict):
                for k, val in node.items():
                    if k == "$ref" and isinstance(val, str) and val.startswith("#/"):
                        cur = doc
                        for part in val[2:].split("/"):
                            part = part.replace("~1", "/").replace("~0", "~")
                            if not isinstance(cur, dict) or part not in cur:
                                r.add("C02 openapi", f"{name}: unresolved local $ref {val}")
                                break
                            cur = cur[part]
                    else:
                        walk(val)
            elif isinstance(node, list):
                for x in node:
                    walk(x)

        walk(doc)
        r.stats.setdefault("openapi_paths", 0)
        r.stats["openapi_paths"] += len(doc.get("paths") or {})


# ---------------------------------------------------------------- C04
def check_state_machine(repo: str, r: Result) -> None:
    r.add("C04 state-machine")
    path = os.path.join(repo, "contracts", "state-machine.yaml")
    if not os.path.exists(path):
        r.add("C04 state-machine", "contracts/state-machine.yaml missing")
        return
    spec = load_yaml(path)["spec"]
    states = {s["name"]: s for s in spec["states"]}
    terminal = set(spec["terminalStates"])
    initial = spec["initialState"]
    trans = spec["transitions"]
    r.stats.update(states=len(states), transitions=len(trans))
    ids = [t["id"] for t in trans]
    if len(ids) != len(set(ids)):
        r.add("C04 state-machine", "duplicate transition ids")
    for t in trans:
        for f in t["from"]:
            if f != "$none" and f not in states:
                r.add("C04 state-machine", f"{t['id']}: unknown from-state {f}")
        if not str(t["to"]).startswith("$") and t["to"] not in states:
            r.add("C04 state-machine", f"{t['id']}: unknown to-state {t['to']}")
    for mode in spec["modes"]:
        mstates = {n for n, s in states.items() if mode in s.get("modes", spec["modes"])}
        mtrans = [t for t in trans if mode in t.get("modes", spec["modes"])]
        preds: dict[str, set[str]] = {s: set() for s in mstates}
        edges: dict[str, set[str]] = {s: set() for s in mstates}
        for t in mtrans:
            if str(t["to"]).startswith("$"):
                continue
            for f in t["from"]:
                if f in mstates and t["to"] in mstates:
                    edges[f].add(t["to"])
                    preds[t["to"]].add(f)
        # $return / $resume go back to a state that previously led into the source state.
        for t in mtrans:
            if t["to"] in ("$return", "$resume"):
                for f in t["from"]:
                    if f in mstates:
                        edges[f] |= {p for p in preds[f] if p in mstates and p != f}
        seen = {initial}
        stack = [initial]
        while stack:
            s = stack.pop()
            for n in edges.get(s, ()):
                if n not in seen:
                    seen.add(n)
                    stack.append(n)
        for s in sorted(mstates - seen):
            r.add("C04 state-machine", f"mode {mode}: state {s} unreachable from {initial}")
        for s in sorted(mstates - terminal):
            reach, stack = {s}, [s]
            while stack:
                x = stack.pop()
                for n in edges.get(x, ()):
                    if n not in reach:
                        reach.add(n)
                        stack.append(n)
            if not reach & terminal:
                r.add("C04 state-machine", f"mode {mode}: state {s} cannot reach a terminal state")


# ---------------------------------------------------------------- C05
def check_plan_sections(repo: str, r: Result) -> None:
    r.add("C05 plan-sections")
    plans = plan_files(repo)
    r.stats["plans"] = len(plans)
    for f in plans:
        heads = [h.strip().lower() for h in re.findall(r"^##\s+(?:\d+\.\s*)?(.+)$", read(f), re.M)]
        idx = []
        for sname in SECTIONS:
            pos = next((i for i, h in enumerate(heads) if h.startswith(sname.lower())), None)
            if pos is None:
                r.add("C05 plan-sections", f"{os.path.basename(f)}: missing section '{sname}'")
            else:
                idx.append(pos)
        if idx != sorted(idx):
            r.add("C05 plan-sections", f"{os.path.basename(f)}: sections out of order")


# ---------------------------------------------------------------- C06/C07
GATE_RE = re.compile(r"^###\s+GATE-([A-Z]\d\d)\b.*?$(.*?)(?=^##|\Z)", re.M | re.S)
AC_RE = re.compile(r"\b(\d\d)-AC-(\d+[a-z]?)\b")


def check_dag(repo: str, r: Result) -> None:
    r.add("C06 dag")
    r.add("C07 ac-ids")
    d = load_yaml(os.path.join(repo, "plans", "dag.yaml"))
    nodes = d["nodes"]
    ids = [n["id"] for n in nodes]
    if len(ids) != len(set(ids)):
        r.add("C06 dag", "duplicate node ids")
    known = set(ids)
    graph = {n["id"]: n["deps"] for n in nodes}
    for n in nodes:
        for x in n["deps"] + (n.get("after") or []):
            if x not in known:
                r.add("C06 dag", f"{n['id']}: unknown dependency {x}")
    color: dict[str, int] = {}

    def visit(v: str, path: list[str]) -> None:
        color[v] = 1
        for w in graph.get(v, ()):
            if color.get(w) == 1:
                r.add("C06 dag", f"cycle: {' -> '.join(path + [v, w])}")
            elif color.get(w) is None:
                visit(w, path + [v])
        color[v] = 2

    for v in graph:
        if v not in color:
            visit(v, [])
    for m in d.get("milestones", []):
        if m["node"] not in known:
            r.add("C06 dag", f"milestone {m['id']} references unknown node {m['node']}")

    plans = {os.path.basename(f)[:2]: (os.path.basename(f), read(f)) for f in plan_files(repo)}
    gate_owners: dict[str, set[str]] = {}
    total_acs = 0
    for num, (fname, text) in plans.items():
        seen_ac: set[str] = set()
        for m in re.finditer(r"^\s*[-*]\s+\*\*(\d\d-AC-\d+[a-z]?)\*\*", text, re.M):
            ac = m.group(1)
            if ac in seen_ac:
                r.add("C07 ac-ids", f"{fname}: duplicate acceptance criterion {ac}")
            seen_ac.add(ac)
            if not ac.startswith(num + "-"):
                r.add("C07 ac-ids", f"{fname}: defines {ac}, which is not in its own number space")
        for gm in GATE_RE.finditer(text):
            node = gm.group(1)
            gate_owners.setdefault(node, set()).add(num)
            if node not in known:
                r.add("C06 dag", f"{fname}: GATE-{node} names an unknown DAG node")
    for n in nodes:
        listed = set(n["plans"])
        owners = gate_owners.get(n["id"], set())
        for p in sorted(listed - owners):
            if p not in plans:
                r.add("C06 dag", f"{n['id']}: listed plan {p} does not exist")
            else:
                r.add("C06 dag", f"{n['id']}: listed plan {p} has no GATE-{n['id']} section")
        for p in sorted(owners - listed):
            r.add("C06 dag", f"{n['id']}: plan {p} has a GATE-{n['id']} section but is not listed on the node")
        acs = set()
        for p in owners:
            for gm in GATE_RE.finditer(plans[p][1]):
                if gm.group(1) == n["id"]:
                    acs |= {f"{a}-AC-{b}" for a, b in AC_RE.findall(gm.group(2))}
        if not acs:
            r.add("C06 dag", f"{n['id']}: no acceptance criteria in any GATE-{n['id']} section")
        total_acs += len(acs)
    r.stats.update(dag_nodes=len(nodes), gate_acceptance_criteria=total_acs)


# ---------------------------------------------------------------- C08
def definition_docs(repo: str) -> list[str]:
    files = [os.path.join(repo, x) for x in ("PLAN.md", "STATUS.md")]
    for pat in ("plans/*.md", "plans/reference/*.md", "procedures/*.md", "adr/*.md"):
        files += glob.glob(os.path.join(repo, pat))
    return sorted(f for f in files if os.path.exists(f))


def check_references(repo: str, r: Result) -> None:
    r.add("C08 references")
    adr_nums = {os.path.basename(f)[:4] for f in glob.glob(os.path.join(repo, "adr", "[0-9][0-9][0-9][0-9]-*.md"))}
    plan_nums = {os.path.basename(f)[:2] for f in glob.glob(os.path.join(repo, "plans", "[0-9][0-9]-*.md"))}
    for f in definition_docs(repo):
        text = read(f)
        rel = os.path.relpath(f, repo)
        for p in sorted(set(re.findall(r"procedures/([a-z0-9-]+\.md)", text))):
            if not os.path.exists(os.path.join(repo, "procedures", p)):
                r.add("C08 references", f"{rel}: missing procedures/{p}")
        for c in sorted(set(re.findall(r"contracts/([a-z0-9-]+\.(?:schema\.yaml|openapi\.yaml|yaml))", text))):
            if not os.path.exists(os.path.join(repo, "contracts", c)):
                r.add("C08 references", f"{rel}: missing contracts/{c}")
        for a in sorted(set(re.findall(r"\bADR-(\d{4})\b", text))):
            if a not in adr_nums:
                r.add("C08 references", f"{rel}: missing ADR-{a}")
        for p in sorted(set(re.findall(r"plans/(\d\d)-[a-z0-9-]+\.md", text))):
            if p not in plan_nums:
                r.add("C08 references", f"{rel}: missing plans/{p}-*.md")


# ---------------------------------------------------------------- C09
def check_alerts(repo: str, r: Result) -> None:
    r.add("C09 alerts")
    cat = load_yaml(os.path.join(repo, "contracts", "alert-catalog.yaml"))["spec"]
    names = {a["alertname"] for a in cat["alerts"]}
    aliases = {al for a in cat["alerts"] for al in (a.get("aliases") or [])}
    r.stats["alerts"] = len(names)
    for a in cat["alerts"]:
        rb = a.get("runbook")
        if rb and not os.path.exists(os.path.join(repo, rb.split("#")[0])):
            r.add("C09 alerts", f"{a['alertname']}: runbook {rb} does not exist")
    for f in definition_docs(repo):
        text = read(f)
        for m in sorted(set(re.findall(r"`(SC[A-Z][A-Za-z0-9]+)`", text))):
            if m not in names and m not in aliases:
                r.add("C09 alerts", f"{os.path.relpath(f, repo)}: unknown alert `{m}`")


# ---------------------------------------------------------------- C10
def check_secrets(repo: str, r: Result) -> None:
    r.add("C10 secrets")
    path = os.path.join(repo, "contracts", "secret-classes.yaml")
    reg = load_yaml(path)["spec"]["classes"]
    ids = {c["id"] for c in reg}
    r.stats["secret_classes"] = len(ids)
    raw = read(path)
    comment_aliases = set(re.findall(r"\bSC-[A-Z0-9]+(?:-[A-Z0-9]+)*", " ".join(
        line for line in raw.splitlines() if line.lstrip().startswith("#"))))
    ignore = re.compile(r"^(?:" + "|".join(NON_SECRET_TOKEN_PATTERNS) + r")$")
    # Take maximal SC-… words, then keep only all-uppercase class-shaped tokens.
    # Mixed-case words such as commit trailers (SC-Task:) are not secret classes.
    word = re.compile(r"(?<![A-Za-z0-9-])SC-[A-Za-z0-9-]+")
    shape = re.compile(r"^SC-[A-Z0-9]+(?:-[A-Z0-9]+)*$")
    for f in definition_docs(repo) + [os.path.join(repo, "contracts", x) for x in ("secret-classes.yaml",)]:
        text = read(f)
        found = {w.rstrip("-") for w in word.findall(text)}
        for t in sorted(x for x in found if shape.match(x)):
            if t in ids or t in comment_aliases or ignore.match(t):
                continue
            if any(i.startswith(t + "-") for i in ids):
                continue  # family prefix of registered classes, e.g. SC-BREAK-GLASS
            r.add("C10 secrets", f"{os.path.relpath(f, repo)}: unregistered secret class {t}")


# ---------------------------------------------------------------- C11
def section(text: str, num: int) -> str:
    m = re.search(rf"^## {num}\. .*?$", text, re.M)
    if not m:
        return ""
    rest = text[m.end():]
    n = re.search(r"^## \d+\. ", rest, re.M)
    return rest[: n.start()] if n else rest


def check_inventories(repo: str, r: Result) -> None:
    r.add("C11 inventories")
    mp = read(os.path.join(repo, "plans", "00-master-plan.md"))
    listed_c = set(re.findall(r"^\| `([a-z0-9-]+\.(?:schema\.yaml|openapi\.yaml|yaml))` \|", section(mp, 10), re.M))
    actual_c = {os.path.basename(f) for f in glob.glob(os.path.join(repo, "contracts", "*.yaml"))}
    for c in sorted(actual_c - listed_c):
        r.add("C11 inventories", f"master plan §10 does not list contracts/{c}")
    for c in sorted(listed_c - actual_c):
        r.add("C11 inventories", f"master plan §10 lists missing contracts/{c}")
    listed_p = set(re.findall(r"^\| `([a-z0-9-]+\.md)` \|", section(mp, 12), re.M))
    actual_p = {os.path.basename(f) for f in glob.glob(os.path.join(repo, "procedures", "*.md"))} - {"_template.md"}
    for p in sorted(actual_p - listed_p):
        r.add("C11 inventories", f"master plan §12 does not list procedures/{p}")
    for p in sorted(listed_p - actual_p):
        r.add("C11 inventories", f"master plan §12 lists missing procedures/{p}")
    r.stats.update(contract_files=len(actual_c), procedures=len(actual_p))


# ---------------------------------------------------------------- C12
def check_plan_md(repo: str, r: Result) -> None:
    r.add("C12 plan-md")
    gen = os.path.join(os.path.dirname(os.path.abspath(__file__)), "genplan.py")
    with tempfile.TemporaryDirectory() as tmp:
        for sub in ("plans",):
            os.symlink(os.path.join(os.path.abspath(repo), sub), os.path.join(tmp, sub))
        proc = subprocess.run([sys.executable, "-I", gen, tmp], capture_output=True, text=True, check=False)
        if proc.returncode != 0:
            r.add("C12 plan-md", f"genplan.py failed: {proc.stderr.strip()[:300]}")
            return
        expected = read(os.path.join(tmp, "PLAN.md"))
    actual = read(os.path.join(repo, "PLAN.md")) if os.path.exists(os.path.join(repo, "PLAN.md")) else ""
    if expected != actual:
        r.add("C12 plan-md", "PLAN.md is stale: run `python3 tools/stage0/genplan.py .` and commit the result")


# ---------------------------------------------------------------- C13/C14
def check_blocking(repo: str, r: Result) -> None:
    r.add("C13 blocking")
    pat = re.compile(r"blocking:\s*\**\s*yes|\|\s*yes\s*\|", re.I)
    for f in plan_files(repo):
        oq = section(read(f), 25)
        for line in oq.splitlines():
            if pat.search(line) and not re.search(r"resolved", line, re.I):
                r.add("C13 blocking", f"{os.path.basename(f)}: open question marked blocking: {line.strip()[:160]}")


def check_spec_issues(repo: str, r: Result) -> None:
    r.add("C14 spec-issues")
    n = 0
    for line in read(os.path.join(repo, "plans", "spec-issues.md")).splitlines():
        if not re.match(r"^\| SI-\d+ \|", line):
            continue
        n += 1
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        sid, resolution, blocking = cells[0], cells[-2], cells[-1]
        if not resolution:
            r.add("C14 spec-issues", f"{sid}: empty resolution")
        if re.match(r"^\**yes", blocking, re.I):
            r.add("C14 spec-issues", f"{sid}: marked blocking")
    r.stats["spec_issues"] = n


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--repo", default=os.path.normpath(os.path.join(os.path.dirname(__file__), "..", "..")))
    ap.add_argument("--json", action="store_true", help="print a machine-readable report")
    args = ap.parse_args()
    repo = os.path.abspath(args.repo)
    r = Result()
    check_contracts(repo, r)
    check_openapi(repo, r)
    check_state_machine(repo, r)
    check_plan_sections(repo, r)
    check_dag(repo, r)
    check_references(repo, r)
    check_alerts(repo, r)
    check_secrets(repo, r)
    check_inventories(repo, r)
    check_plan_md(repo, r)
    check_blocking(repo, r)
    check_spec_issues(repo, r)
    if args.json:
        print(json.dumps({"ok": r.ok, "stats": r.stats, "failures": r.checks}, indent=2, sort_keys=True))
    else:
        for name in sorted(r.checks):
            fails = r.checks[name]
            print(f"{'PASS' if not fails else 'FAIL'}  {name}" + (f"  ({len(fails)} problems)" if fails else ""))
            for msg in fails[:50]:
                print(f"      - {msg}")
            if len(fails) > 50:
                print(f"      ... {len(fails) - 50} more")
        print("stats: " + ", ".join(f"{k}={v}" for k, v in sorted(r.stats.items())))
        print("RESULT: " + ("PASS" if r.ok else "FAIL"))
    return 0 if r.ok else 1


if __name__ == "__main__":
    sys.exit(main())
