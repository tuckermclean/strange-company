"""Validate YAML instance files against one schema of contracts/*.schema.yaml (plan 01 section 6.3, check 1).

usage: validate_schema.py SCHEMA_FILE DEF_NAME_OR_- FILE:KIND...

F01 interim helper behind `sc-lint locks` and `sc-lint quarantine` (run with the Stage-0 venv of the ci image;
F02 replaces it with the Go sc-contracts validator). A DEF_NAME of "-" validates against the schema's top level;
otherwise against `#/$defs/<DEF_NAME>`. Each FILE is validated against the def named by its KIND suffix when the
DEF_NAME is "@kind". Unquoted YAML dates are read as strings, as plan 01 section 8.2 writes them.
Prints a JSON list of {"file", "path", "message"}; exit 0 even with violations, 2 on usage or I/O errors.
"""
import datetime
import json
import os
import sys

import yaml
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012


def plain(v):
    if isinstance(v, (datetime.date, datetime.datetime)):
        return v.isoformat()
    if isinstance(v, dict):
        return {str(k): plain(x) for k, x in v.items()}
    if isinstance(v, list):
        return [plain(x) for x in v]
    return v


def main(argv):
    if len(argv) < 4:
        print(__doc__, file=sys.stderr)
        return 2
    schema_file, def_name, items = argv[1], argv[2], argv[3:]
    with open(schema_file, encoding="utf-8") as fh:
        schema = yaml.safe_load(fh)
    registry = Registry().with_resource(schema["$id"], Resource.from_contents(schema, default_specification=DRAFT202012))
    out = []
    for item in items:
        path, _, kind = item.partition(":")
        name = kind if def_name == "@kind" else def_name
        ref = schema["$id"] + ("" if name == "-" else "#/$defs/" + name)
        try:
            with open(path, encoding="utf-8") as fh:
                doc = plain(yaml.safe_load(fh))
        except (OSError, yaml.YAMLError) as e:
            out.append({"file": path, "path": "(root)", "message": "cannot read YAML: %s" % str(e)[:200]})
            continue
        validator = Draft202012Validator({"$ref": ref}, registry=registry, format_checker=Draft202012Validator.FORMAT_CHECKER)
        for e in sorted(validator.iter_errors(doc), key=lambda e: [str(p) for p in e.absolute_path]):
            out.append({"file": path, "path": "/".join(str(p) for p in e.absolute_path) or "(root)", "message": e.message[:300]})
    json.dump(out, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
