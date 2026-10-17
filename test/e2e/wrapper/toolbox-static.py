"""Static assertions for the F03 S2 toolbox build inputs (plan 02 sections 9.2, 9.4, 14.1; 02-AC-1/2/3/9).
Called by toolbox-static.sh; prints one line per check: "ok<TAB>name" or "FAIL<TAB>name<TAB>detail".
Reads files only; no network, no engine."""
import os
import posixpath
import re
import shlex
import sys

import yaml

root = sys.argv[1]


def res(name, good, detail=""):
    print(("ok\t%s" % name) if good else ("FAIL\t%s\t%s" % (name, detail)))


def rd(p):
    with open(os.path.join(root, p), encoding="utf-8") as f:
        return f.read()


# ---------------------------------------------------------------- baseline lines
base = yaml.safe_load(rd("plans/compatibility-baseline.yaml"))
lines = {c["name"]: c["line"] for c in base["spec"]["components"]}


def in_line(version, line):
    pre = re.sub(r"\.x$", "", line)
    v = re.sub(r"^(v|jq-)", "", str(version))
    return v == pre or v.startswith(pre + ".")


# key -> (aliases of the lock entry name or install target basename, baseline component or literal line)
RUNTIME = {
    "tofu": (["tofu", "opentofu"], "OpenTofu"),
    "kubectl": (["kubectl"], "kubectl"),
    "helm": (["helm"], "Helm"),
    "flux": (["flux"], "Flux"),
    "sops": (["sops"], "SOPS"),
    "age": (["age"], "age"),
    "jq": (["jq"], "jq"),
    "yq": (["yq"], "yq (mikefarah)"),
    "aws": (["aws", "awscli", "aws-cli"], "AWS CLI v2"),
    "kubectl-cnpg": (["kubectl-cnpg"], "CloudNativePG operator"),
    "etcdutl": (["etcdutl"], "=3.6.x"),
    "cmctl": (["cmctl"], "cmctl"),
    "gitleaks": (["gitleaks"], "gitleaks"),
    "cosign": (["cosign"], "cosign"),
    "syft": (["syft"], "syft"),
    "trivy": (["trivy"], "trivy"),
}
CI = {
    "actionlint": (["actionlint"], None),
    "woodpecker-cli": (["woodpecker-cli"], "Woodpecker server and agent"),
    "kubeconform": (["kubeconform"], "kubeconform"),
    "k3d": (["k3d"], "k3d"),
    "promtool": (["promtool"], "Prometheus"),
    "helm-unittest": (["helm-unittest", "helm_unittest", "unittest"], "helm-unittest"),
    "weed": (["weed", "seaweedfs"], "SeaweedFS (CI S3 emulator)"),
}

lock = yaml.safe_load(rd("toolbox/tools.lock.yaml"))["spec"]
tools = lock.get("tools", [])


def find(aliases):
    for t in tools:
        names = {t.get("name")}
        to = (t.get("install") or {}).get("to")
        if to:
            names.add(posixpath.basename(to))
        if names & set(aliases):
            return t
    return None


for table, want_set in ((RUNTIME, "runtime"), (CI, "ci")):
    for key, (aliases, comp) in sorted(table.items()):
        n = "02-AC-2 lock lists %s with set: %s" % (key, want_set)
        t = find(aliases)
        if t is None:
            res(n, False, "no tools[] entry named/installed as %s" % aliases)
            continue
        res(n, t.get("set") == want_set, "set is %r" % t.get("set"))
        sha = t.get("sha256") or {}
        res("02-R-1 lock %s has sha256 for amd64 and arm64" % key,
            all(re.fullmatch(r"[0-9a-f]{64}", str(sha.get(a, ""))) for a in ("amd64", "arm64")), "sha256=%r" % sha)
        res("02-R-1 lock %s versionRegex contains {version}" % key, "{version}" in str(t.get("versionRegex", "")),
            "versionRegex=%r" % t.get("versionRegex"))
        res("02-R-1 lock %s has a versionCommand" % key, bool(t.get("versionCommand")))
        if comp:
            line = comp[1:] if comp.startswith("=") else lines.get(comp)
            res("plan 02 s9.4 lock %s version %s is inside the baseline line %s" % (key, t.get("version"), line),
                bool(line) and in_line(t.get("version"), line), "version=%r" % t.get("version"))

# ---------------------------------------------------------------- python and collections
req = rd("toolbox/requirements.txt")
logical = re.sub(r"\\\n", " ", req).splitlines()
reqs = [l.strip() for l in logical if l.strip() and not l.strip().startswith("#") and not l.strip().startswith("-")]
res("02-R-1 requirements.txt pins ansible-core 2.21.x", any(re.match(r"ansible-core==2\.21\.\d+\b", l) for l in reqs),
    "requirements: %s" % reqs[:3])
res("02-R-1 every requirements.txt entry is hash-locked", bool(reqs) and all("--hash=sha256:" in l for l in reqs),
    "unhashed: %s" % [l for l in reqs if "--hash=sha256:" not in l][:3])
colls = {c.get("name"): c for c in lock.get("ansibleCollections", [])}
for name, major in (("community.general", "13"), ("ansible.posix", "2")):
    c = colls.get(name)
    ok = bool(c) and str(c.get("version", "")).split(".")[0] == major and str(c.get("url", "")).startswith("https://") \
        and re.fullmatch(r"[0-9a-f]{64}", str(c.get("sha256", "")))
    res("02-R-1 lock pins collection %s by version, https url and sha256" % name, ok, "entry=%r" % c)

# ---------------------------------------------------------------- Containerfile
instr = []  # (stage, keyword, rest)
stage = None
stages = {}  # name -> (from ref)
text = re.sub(r"\\\s*\n", " ", rd("toolbox/Containerfile"))
for raw in text.splitlines():
    s = raw.strip()
    if not s or s.startswith("#"):
        continue
    kw, _, rest = s.partition(" ")
    kw = kw.upper()
    if kw == "FROM":
        m = re.match(r"(?:--\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?\s*$", rest.strip(), re.I)
        stage = m.group(2).lower() if m and m.group(2) else "stage%d" % len(stages)
        stages[stage] = m.group(1) if m else ""
    instr.append((stage, kw, rest.strip()))

for st in ("gobuild", "base", "fetch", "toolbox", "ci"):
    res("plan 02 s14.1 Containerfile has stage %s" % st, st in stages)
res("plan 02 s14.1 stage ci is FROM toolbox", stages.get("ci", "").lower() == "toolbox", "FROM %s" % stages.get("ci"))
res("plan 02 s14.1 stage toolbox is FROM base", stages.get("toolbox", "").lower() == "base", "FROM %s" % stages.get("toolbox"))
bi = lock.get("baseImages", {})
res("plan 02 s14.1 stage gobuild is FROM the lock golang digest", bi.get("golang", {}).get("digest", "?") in stages.get("gobuild", ""))
res("plan 02 s14.1 stage base is FROM the lock debian digest", bi.get("debian", {}).get("digest", "?") in stages.get("base", ""))
ext = [(s, r) for s, r in stages.items() if r.lower() not in stages and r != "scratch"]
res("02-R-1 every external FROM is digest-pinned",
    bool(ext) and all(re.search(r"@sha256:[0-9a-f]{64}$", r) for _, r in ext), "FROMs: %s" % ext)

runs = [(s, r) for s, k, r in instr if k == "RUN"]
allrun = "\n".join(r for _, r in runs)
res("plan 02 s14.9 Containerfile does not use fetch-pinned", "fetch-pinned" not in allrun, "RUN still calls fetch-pinned")
res("plan 02 s14.1 runtime tools come from `sc-bootstrap toolbox fetch --set runtime`",
    bool(re.search(r"sc-bootstrap\s+toolbox\s+fetch\s+--set\s+runtime", allrun)))
res("plan 02 s14.1 ci tools come from `sc-bootstrap toolbox fetch --set ci` in stage ci",
    any(s == "ci" and re.search(r"sc-bootstrap\s+toolbox\s+fetch\s+--set\s+ci", r) for s, r in runs))
for st in ("toolbox", "ci"):
    sr = [r for s, r in runs if s == st]
    res("plan 02 s14.1 step 6 the last RUN of stage %s is selfcheck --embedded-only" % st,
        bool(sr) and bool(re.search(r"sc-bootstrap\s+toolbox\s+selfcheck\s+--embedded-only", sr[-1])),
        "last RUN: %s" % (sr[-1][:120] if sr else None))
    iis = [r for r in sr if "image-info.json" in r]
    res("plan 02 s7.2 stage %s writes image-info.json with target %s" % (st, st),
        bool(iis) and bool(re.search(r"target[^A-Za-z0-9]*%s\b" % st, iis[-1])), "image-info RUNs: %s" % [r[:80] for r in iis])
    if st == "ci":
        res("plan 02 s14.1 stage ci runs apt as root (USER 0) before fetching ci tools",
            any(s == "ci" and k == "USER" and r.split(":")[0] == "0" for s, k, r in instr))

env = {}
for s, k, r in instr:
    if k != "ENV":
        continue
    toks = shlex.split(r)
    if toks and "=" not in toks[0]:
        env[toks[0]] = " ".join(toks[1:])
    else:
        for t in toks:
            n, _, v = t.partition("=")
            env[n] = v
res("02-AC-9 no ENV name=value matches (secret|token|password|key)=",
    bool(env) and not [n for n, v in env.items() if re.search(r"(?i)(secret|token|password|key)=", "%s=%s" % (n, v))],
    "offending: %s" % [n for n, v in env.items() if re.search(r"(?i)(secret|token|password|key)=", "%s=%s" % (n, v))])
res("plan 02 s7.2 ENV SC_IN_CONTAINER=1", env.get("SC_IN_CONTAINER") == "1")
res("plan 02 s9.4 ENV AWS_REQUEST_CHECKSUM_CALCULATION=when_required", env.get("AWS_REQUEST_CHECKSUM_CALCULATION") == "when_required")
res("plan 02 s9.4 ENV AWS_RESPONSE_CHECKSUM_VALIDATION=when_required", env.get("AWS_RESPONSE_CHECKSUM_VALIDATION") == "when_required")
res("plan 02 s9.4 ENV PATH=/opt/sc/bin:/opt/sc/ansible-venv/bin:/usr/local/bin:/usr/bin:/bin (stage toolbox)",
    any(k == "ENV" and s == "toolbox" and "PATH=/opt/sc/bin:/opt/sc/ansible-venv/bin:/usr/local/bin:/usr/bin:/bin" in shlex.split(r)
        for s, k, r in instr))
res("plan 02 s9.4 KUBECTL_EXTERNAL_DIFF is not set", "KUBECTL_EXTERNAL_DIFF" not in env)
# ANSIBLE_CONFIG / TF_PLUGIN_CACHE_DIR / HELM_CACHE_HOME depend on $SC_REPO_ROOT, known only at run time: they may be
# image ENV or exported by sc-entrypoint; either way they must be defined by the image's own files.
ent = rd("toolbox/bin/sc-entrypoint")
cf = rd("toolbox/Containerfile")
for name, frag in (("ANSIBLE_CONFIG", "infra/ansible/ansible.cfg"), ("TF_PLUGIN_CACHE_DIR", ".cache/tofu-plugins"),
                   ("HELM_CACHE_HOME", ".cache/helm")):
    res("plan 02 s9.4 image defines %s (%s)" % (name, frag),
        any(re.search(r"%s=\S*%s" % (name, re.escape(frag)), t) for t in (cf, ent)))
res("02-AC-2 ci image sets KUBEBUILDER_ASSETS for the envtest assets",
    any(k == "ENV" and "KUBEBUILDER_ASSETS" in r for _, k, r in instr) or "KUBEBUILDER_ASSETS" in ent)
