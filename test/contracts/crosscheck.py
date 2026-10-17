"""Cross-implementation guard for `make contracts` (plan 01 section 8.3, last bullet).

usage: crosscheck.py [ROOT [REPORT_JSON]]      (defaults: ".", ROOT/out/contracts/report.json)

Run with the Stage-0 venv of the ci image (Python jsonschema, Draft202012Validator, format checker). It
recomputes, with Python, the pass/fail verdict of every (schema, instance) validation that sc-contracts (Go)
records in its report, and requires the two result sets to be identical.

Pairing. A pair is one validation verdict; the Go verdict is read from the report row, the Python verdict is
recomputed. Report rows with no validation (dialect, $id, title, duplicate-key, ... checks) are not pairs.
  CONTRACT_EXAMPLE_INVALID          detail "examples[i]..."            schema file validates its examples[i]
  CONTRACT_INVALID_EXAMPLE_PASSES   detail "x-invalid-examples[i]..."  schema validates x-invalid-examples[i].value
                                    (row pass = Go rejected the value; rows about a missing/odd item are skipped)
  CONTRACT_DEFS_EXAMPLE_INVALID     detail "x-defs-examples.N[i]..."   #/$defs/N validates x-defs-examples.N[i]
  CONTRACT_OPENAPI_EXAMPLE          detail "<ptr>/examples[i]..."      schema at <ptr> of the OpenAPI document
                                    validates examples[i] (document registered as <idPrefix><stem>/openapi)
  CONTRACT_INSTANCE_INVALID         detail "... against <schema file>"  the instance file validates against that
                                    schema (kind rows: the top level or $defs entry whose properties.kind.const is
                                    the instance's kind; fixed provider rows: InfraPriceCatalog / TofuPlanGuard)
Schemas are registered by $id in a referencing Registry. YAML is read with the 1.2 core scalars (timestamps stay
strings, only true/false are booleans), as sc-contracts does. Files Go could not load (a YAML error row) produce
no verdict; they are counted as skipped in the summary.
Output: `crosscheck: MISMATCH <file> <check> <locator> go=<pass|fail> python=<pass|fail>` per divergence and
`crosscheck: <N> pairs compared, <M> mismatches[, <K> skipped]`. Exit 1 on any mismatch, 2 on usage/I-O errors.
"""
import glob
import json
import os
import re
import sys

import yaml
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ID_PREFIX = "https://strange.company/contracts/"


class Loader(yaml.SafeLoader):
    pass


Loader.yaml_implicit_resolvers = {
    k: [(t, r) for (t, r) in v if t not in ("tag:yaml.org,2002:timestamp", "tag:yaml.org,2002:bool")]
    for k, v in yaml.SafeLoader.yaml_implicit_resolvers.items()
}
Loader.add_implicit_resolver("tag:yaml.org,2002:bool", re.compile(r"^(?:true|True|TRUE|false|False|FALSE)$"), list("tTfF"))


def load(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.load(fh, Loader=Loader)


def verdict(registry, ref, instance):
    v = Draft202012Validator({"$ref": ref}, registry=registry, format_checker=Draft202012Validator.FORMAT_CHECKER)
    return "pass" if v.is_valid(instance) else "fail"


def ptr_get(doc, ptr):
    cur = doc
    if ptr:
        for tok in ptr[1:].split("/"):
            tok = tok.replace("~1", "/").replace("~0", "~")
            cur = cur[int(tok)] if isinstance(cur, list) else cur[tok]
    return cur


def frag(ptr):
    from urllib.parse import quote
    return quote(ptr, safe="/")


def kind_of(node):
    props = node.get("properties") if isinstance(node, dict) else None
    k = props.get("kind") if isinstance(props, dict) else None
    return k.get("const") if isinstance(k, dict) else None


def main(argv):
    root = argv[1] if len(argv) > 1 else "."
    report = argv[2] if len(argv) > 2 else os.path.join(root, "out", "contracts", "report.json")
    try:
        with open(report, encoding="utf-8") as fh:
            rows = json.load(fh)
    except (OSError, ValueError) as e:
        print("crosscheck: cannot read %s: %s" % (report, e), file=sys.stderr)
        return 2

    registry = Registry()
    schemas = {}  # relative path -> document
    for p in sorted(glob.glob(os.path.join(root, "contracts", "*.schema.yaml"))):
        rel = os.path.relpath(p, root).replace(os.sep, "/")
        try:
            doc = load(p)
        except yaml.YAMLError:
            continue
        if isinstance(doc, dict) and isinstance(doc.get("$id"), str) and doc["$id"]:
            schemas[rel] = doc
            registry = registry.with_resource(doc["$id"], Resource.from_contents(doc, default_specification=DRAFT202012))
    openapis = {}
    for p in sorted(glob.glob(os.path.join(root, "contracts", "*.openapi.yaml"))):
        rel = os.path.relpath(p, root).replace(os.sep, "/")
        try:
            doc = load(p)
        except yaml.YAMLError:
            continue
        if isinstance(doc, dict):
            base = ID_PREFIX + os.path.basename(p)[: -len(".openapi.yaml")] + "/openapi"
            openapis[rel] = (base, doc)
            registry = registry.with_resource(base, Resource.from_contents(doc, default_specification=DRAFT202012))

    pairs = mismatches = skipped = 0
    cache = {}

    def compare(row, locator, ref, instance):
        nonlocal pairs, mismatches, skipped
        go = row["result"]
        if row["check"] == "CONTRACT_INVALID_EXAMPLE_PASSES":
            go = "fail" if row["result"] == "pass" else "pass"  # the row passes when Go rejects the value
        try:
            py = verdict(registry, ref, instance)
        except Exception as e:  # unresolvable reference or broken schema in Python only
            print("crosscheck: MISMATCH %s %s %s go=%s python=error(%s)" % (row["file"], row["check"], locator, go, type(e).__name__))
            pairs += 1
            mismatches += 1
            return
        pairs += 1
        if py != go:
            mismatches += 1
            print("crosscheck: MISMATCH %s %s %s go=%s python=%s" % (row["file"], row["check"], locator, go, py))

    for row in rows:
        chk, f, detail = row["check"], row["file"], row.get("detail", "")
        try:
            if chk == "CONTRACT_EXAMPLE_INVALID":
                m = re.match(r"examples\[(\d+)\]", detail)
                s = schemas.get(f)
                if m and s:
                    compare(row, m.group(0), s["$id"], s["examples"][int(m.group(1))])
            elif chk == "CONTRACT_INVALID_EXAMPLE_PASSES":
                m = re.match(r"x-invalid-examples\[(\d+)\]", detail)
                s = schemas.get(f)
                if m and s:
                    item = s["x-invalid-examples"][int(m.group(1))]
                    if row["result"] == "pass" or detail.endswith("validates but must fail"):
                        compare(row, m.group(0), s["$id"], item["value"])
            elif chk == "CONTRACT_DEFS_EXAMPLE_INVALID":
                m = re.match(r"x-defs-examples\.([^\[]+)\[(\d+)\]", detail)
                s = schemas.get(f)
                if m and s:
                    compare(row, m.group(0), s["$id"] + "#/$defs/" + m.group(1), s["x-defs-examples"][m.group(1)][int(m.group(2))])
            elif chk == "CONTRACT_OPENAPI_EXAMPLE":
                m = re.match(r"(/\S*?)/examples\[(\d+)\]", detail)
                if m and f in openapis:
                    base, doc = openapis[f]
                    node = ptr_get(doc, m.group(1))
                    compare(row, m.group(0), base + "#" + frag(m.group(1)), node["examples"][int(m.group(2))])
            elif chk == "CONTRACT_INSTANCE_INVALID":
                m = re.search(r"against (\S+\.schema\.yaml)", detail)
                s = schemas.get(m.group(1)) if m else None
                if not s:
                    continue
                inst = load(os.path.join(root, f))
                ref = s["$id"]
                if "provider.schema.yaml" == os.path.basename(m.group(1)):
                    ref += "#/$defs/" + ("InfraPriceCatalog" if f.startswith("infra/catalog/prices/") else "TofuPlanGuard")
                elif detail.startswith("kind "):
                    kind = inst.get("kind") if isinstance(inst, dict) else None
                    if kind_of(s) != kind:
                        for name, d in (s.get("$defs") or {}).items():
                            if kind_of(d) == kind:
                                ref += "#/$defs/" + name
                                break
                compare(row, "instance", ref, inst)
        except (KeyError, IndexError, TypeError, ValueError, yaml.YAMLError, OSError):
            skipped += 1
    extra = ", %d skipped" % skipped if skipped else ""
    print("crosscheck: %d pairs compared, %d mismatches%s" % (pairs, mismatches, extra))
    return 1 if mismatches else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
