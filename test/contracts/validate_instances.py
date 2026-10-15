"""Instance validation of the governed `.strange-company/*.yaml` files (plan 01 section 8.3, 01-AC-11).

F01 interim: run by `make contracts` with the Stage-0 venv of the ci image. The schema is selected from
apiVersion + kind (a schema's top level or one of its $defs carries `kind: {const: ...}`). F02 replaces this
with the Go `sc-contracts` validator.
"""
import glob
import os
import sys

import yaml
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

API_VERSION = "strange.company/v1alpha1"
GLOBS = [".strange-company/*.yaml"]


def kind_const(schema):
    if not isinstance(schema, dict):
        return None
    kind = (schema.get("properties") or {}).get("kind")
    return kind.get("const") if isinstance(kind, dict) else None


def load(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


def main():
    root = os.getcwd()
    schemas = {}  # file name -> document
    for path in sorted(glob.glob(os.path.join(root, "contracts", "*.schema.yaml"))):
        schemas[os.path.basename(path)] = load(path)
    registry = Registry()
    for doc in schemas.values():
        registry = registry.with_resource(doc["$id"], Resource.from_contents(doc, default_specification=DRAFT202012))

    by_kind = {}  # kind -> (priority, file, ref)
    for name, doc in schemas.items():
        cands = []
        if kind_const(doc):
            cands.append((0, kind_const(doc), doc["$id"]))
        for def_name, sub in (doc.get("$defs") or {}).items():
            if kind_const(sub):
                cands.append((1, kind_const(sub), doc["$id"] + "#/$defs/" + def_name))
        for prio, kind, ref in cands:
            if kind not in by_kind or (prio, name) < by_kind[kind][:2]:
                by_kind[kind] = (prio, name, ref)

    failures = 0
    files = sorted(f for g in GLOBS for f in glob.glob(os.path.join(root, g)))
    if not files:
        print("contracts: no governed files found", file=sys.stderr)
        return 1
    for path in files:
        rel = os.path.relpath(path, root)
        doc = load(path)
        kind = doc.get("kind") if isinstance(doc, dict) else None
        if not isinstance(doc, dict) or doc.get("apiVersion") != API_VERSION or kind not in by_kind:
            print("%s: FAIL CONTRACT_KIND_UNKNOWN apiVersion=%r kind=%s has no schema"
                  % (rel, doc.get("apiVersion") if isinstance(doc, dict) else None, kind))
            failures += 1
            continue
        _, schema_file, ref = by_kind[kind]
        validator = Draft202012Validator({"$ref": ref}, registry=registry, format_checker=Draft202012Validator.FORMAT_CHECKER)
        errors = sorted(validator.iter_errors(doc), key=lambda e: [str(p) for p in e.absolute_path])
        if errors:
            failures += 1
            print("%s: FAIL %s does not validate against %s" % (rel, kind, schema_file))
            for e in errors:
                print("  %s: %s" % ("/".join(str(p) for p in e.absolute_path) or "(root)", e.message[:300]))
        else:
            print("%s: ok kind %s validated against %s" % (rel, kind, schema_file))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
