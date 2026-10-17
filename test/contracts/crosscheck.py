"""Cross-implementation guard for `make contracts` (plan 01 section 8.3, last bullet; CD-60(b)).

usage: crosscheck.py [ROOT [REPORT_JSON]]      (defaults: ".", ROOT/out/contracts/report.json)

Run with the Stage-0 venv of the ci image (Python jsonschema, Draft202012Validator, format checker). It
recomputes, with Python, the pass/fail verdict of every (schema, instance) validation that sc-contracts (Go)
records in its report, and requires the two result sets to be identical.

Pairing. Every report row that records a validation verdict carries
  "pair": {"schema": "<file or URI>#<JSON pointer>", "instance": "<file>#<JSON pointer>"}
Rows without `pair` (dialect, $id, title, ... checks) are not validations and are ignored. The schema side is a
repo-relative contract file (*.schema.yaml, resolved through its $id; *.openapi.yaml, resolved through the
document base URI https://strange.company/contracts/<stem>/openapi), the embedded OAS 3.1 schema URI, or the
2020-12 metaschema URI. The instance side is a repo-relative file and a JSON pointer into the loaded document.
For CONTRACT_INVALID_EXAMPLE_PASSES the row passes when Go rejects the value, so the expected verdict is inverted.

YAML is read with the same rules as sc-contracts (go.yaml.in/yaml/v3 resolver; timestamps and !!binary stay
strings, "<<" is an ordinary key, duplicate keys are an error).

Output: `crosscheck: MISMATCH <file> <check> <schema> <instance> go=<pass|fail> python=<pass|fail>`,
`crosscheck: UNEVALUABLE <file> <check> <pair>: <reason>` and the summary
`crosscheck: <N> pairs compared, <M> mismatches, <U> unevaluable` (N = rows carrying a pair).
Exit 1 on any mismatch or unevaluable pair, or when the report has no pair at all; 2 on usage/I-O errors.
"""
import glob
import json
import math
import os
import re
import sys
from urllib.parse import quote

import yaml
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ID_PREFIX = "https://strange.company/contracts/"
DIALECT = "https://json-schema.org/draft/2020-12/schema"
OAS_URL = "https://spec.openapis.org/oas/3.1/schema/2022-10-07"
OAS_FILE = os.path.join("src", "internal", "contracts", "oas31-schema-2022-10-07.json")

# ---------------------------------------------------------------- YAML as sc-contracts reads it

_MAP = {}
for _s in ("true", "True", "TRUE"):
    _MAP[_s] = ("bool", True)
for _s in ("false", "False", "FALSE"):
    _MAP[_s] = ("bool", False)
for _s in ("", "~", "null", "Null", "NULL"):
    _MAP[_s] = ("null", None)
for _s in (".nan", ".NaN", ".NAN"):
    _MAP[_s] = ("float", math.nan)
for _s in (".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF"):
    _MAP[_s] = ("float", math.inf)
for _s in ("-.inf", "-.Inf", "-.INF"):
    _MAP[_s] = ("float", -math.inf)

_FLOAT_RE = re.compile(r"^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$")
_DOT_FLOAT_RE = re.compile(r"^\.[0-9]+([eE][-+]?[0-9]+)?$")
_DIGITS = {2: "01", 8: "01234567", 10: "0123456789", 16: "0123456789abcdefABCDEF"}


def _go_int(plain):
    """strconv.ParseInt/ParseUint with base 0 (and int64/uint64 range), or None."""
    sign = ""
    body = plain
    if body[:1] in ("+", "-"):
        sign, body = body[0], body[1:]
    base = 10
    if body[:2] in ("0x", "0X"):
        base, body = 16, body[2:]
    elif body[:2] in ("0b", "0B"):
        base, body = 2, body[2:]
    elif body[:2] in ("0o", "0O"):
        base, body = 8, body[2:]
    elif len(body) > 1 and body[0] == "0":
        base, body = 8, body[1:]
    if not body or any(c not in _DIGITS[base] for c in body):
        return None
    v = int(body, base)
    if sign == "-":
        v = -v
    if -(1 << 63) <= v < (1 << 63) or (sign == "" and v < (1 << 64)):
        return v
    return None


def go_scalar(text):
    """Resolve an unquoted scalar as go.yaml.in/yaml/v3 does: ('int'|'float'|'bool'|'null'|'str', value)."""
    if text in _MAP:
        return _MAP[text]
    c = text[:1]
    if c == ".":
        if _DOT_FLOAT_RE.match(text):
            return ("float", float(text))
    elif c.isascii() and (c.isdigit() or c in "+-"):
        plain = text.replace("_", "")
        v = _go_int(plain)
        if v is not None:
            return ("int", v)
        if _FLOAT_RE.match(plain):
            return ("float", float(plain))
    return ("str", text)


class Loader(yaml.SafeLoader):
    """SafeLoader with the sc-contracts scalar rules."""

    def resolve(self, kind, value, implicit):
        if kind is yaml.ScalarNode and implicit[0]:
            return "tag:yaml.org,2002:" + go_scalar(value)[0]
        return super().resolve(kind, value, implicit)

    def construct_mapping(self, node, deep=False):
        out = {}
        for key_node, value_node in node.value:
            if not isinstance(key_node, yaml.ScalarNode):
                raise yaml.YAMLError("non-scalar mapping key at line %d" % (key_node.start_mark.line + 1))
            key = key_node.value
            if key in out:
                raise yaml.YAMLError("duplicate mapping key %r at line %d" % (key, key_node.start_mark.line + 1))
            out[key] = self.construct_object(value_node, deep=True)
        return out


def _construct_typed(kind):
    def construct(loader, node):
        got, val = go_scalar(node.value)
        if got == kind:
            return val
        if kind == "float" and got == "int":
            return float(val)
        raise yaml.YAMLError("cannot decode %s %r as a %s" % (got, node.value, kind))
    return construct


for _k in ("int", "float", "bool"):
    Loader.add_constructor("tag:yaml.org,2002:" + _k, _construct_typed(_k))
Loader.add_constructor("tag:yaml.org,2002:null", lambda loader, node: None)
Loader.add_constructor("tag:yaml.org,2002:timestamp", lambda loader, node: node.value)
Loader.add_constructor("tag:yaml.org,2002:binary", lambda loader, node: node.value)
Loader.add_constructor("tag:yaml.org,2002:merge", lambda loader, node: node.value)


def load(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.load(fh, Loader=Loader)


# ---------------------------------------------------------------- pointers and pairs

def ptr_get(doc, ptr):
    cur = doc
    if ptr:
        if not ptr.startswith("/"):
            raise KeyError(ptr)
        for tok in ptr[1:].split("/"):
            tok = tok.replace("~1", "/").replace("~0", "~")
            cur = cur[int(tok)] if isinstance(cur, list) else cur[tok]
    return cur


def split_locator(s):
    f, _, ptr = s.partition("#")  # the schema URIs of the metaschema and the OAS schema carry no fragment
    return f, ptr


class Unevaluable(Exception):
    pass


def main(argv):
    root = argv[1] if len(argv) > 1 else "."
    report = argv[2] if len(argv) > 2 else os.path.join(root, "out", "contracts", "report.json")
    try:
        with open(report, encoding="utf-8") as fh:
            rows = json.load(fh)
        if not isinstance(rows, list):
            raise ValueError("report is not a list")
    except (OSError, ValueError) as e:
        print("crosscheck: cannot read %s: %s" % (report, e), file=sys.stderr)
        return 2

    registry = Registry()
    base_of = {}  # repo-relative contract file -> base URI
    for p in sorted(glob.glob(os.path.join(root, "contracts", "*.schema.yaml"))):
        rel = os.path.relpath(p, root).replace(os.sep, "/")
        try:
            doc = load(p)
        except (yaml.YAMLError, OSError, UnicodeDecodeError):
            continue
        if isinstance(doc, dict) and isinstance(doc.get("$id"), str) and doc["$id"]:
            base_of[rel] = doc["$id"]
            registry = registry.with_resource(doc["$id"], Resource.from_contents(doc, default_specification=DRAFT202012))
    for p in sorted(glob.glob(os.path.join(root, "contracts", "*.openapi.yaml"))):
        rel = os.path.relpath(p, root).replace(os.sep, "/")
        try:
            doc = load(p)
        except (yaml.YAMLError, OSError, UnicodeDecodeError):
            continue
        if isinstance(doc, dict):
            base = ID_PREFIX + os.path.basename(p)[: -len(".openapi.yaml")] + "/openapi"
            base_of[rel] = base
            registry = registry.with_resource(base, Resource.from_contents(doc, default_specification=DRAFT202012))
    try:
        with open(os.path.join(root, OAS_FILE), encoding="utf-8") as fh:
            registry = registry.with_resource(OAS_URL, Resource.from_contents(json.load(fh), default_specification=DRAFT202012))
    except (OSError, ValueError):
        pass

    fmt = Draft202012Validator.FORMAT_CHECKER
    validators = {}
    docs = {}

    def validator(schema_loc):
        if schema_loc in validators:
            return validators[schema_loc]
        f, ptr = split_locator(schema_loc)
        if f == DIALECT:
            v = Draft202012Validator(Draft202012Validator.META_SCHEMA, format_checker=fmt)
        else:
            base = OAS_URL if f == OAS_URL else base_of.get(f)
            if base is None:
                raise Unevaluable("schema %s is not a loadable contract" % f)
            v = Draft202012Validator({"$ref": base + "#" + quote(ptr, safe="/")}, registry=registry, format_checker=fmt)
        validators[schema_loc] = v
        return v

    def instance(loc):
        f, ptr = split_locator(loc)
        if f not in docs:
            docs[f] = load(os.path.join(root, f))
        return ptr_get(docs[f], ptr)

    pairs = mismatches = unevaluable = 0
    for row in rows:
        pair = row.get("pair") if isinstance(row, dict) else None
        if not pair:
            continue
        pairs += 1
        go = row.get("result")
        if row.get("check") == "CONTRACT_INVALID_EXAMPLE_PASSES":
            go = "fail" if go == "pass" else "pass"  # the row passes when Go rejects the value
        label = "%s %s" % (row.get("file"), row.get("check"))
        try:
            v = validator(pair["schema"])
            py = "pass" if v.is_valid(instance(pair["instance"])) else "fail"
        except Exception as e:  # missing file/pointer, YAML error, unresolvable reference, broken schema
            unevaluable += 1
            print("crosscheck: UNEVALUABLE %s %s -> %s: %s: %s" % (label, pair.get("schema"), pair.get("instance"), type(e).__name__, e))
            continue
        if py != go:
            mismatches += 1
            print("crosscheck: MISMATCH %s %s -> %s go=%s python=%s" % (label, pair["schema"], pair["instance"], go, py))
    print("crosscheck: %d pairs compared, %d mismatches, %d unevaluable" % (pairs, mismatches, unevaluable))
    if pairs == 0:
        print("crosscheck: the report has no validation pair; nothing was cross-checked")
        return 1
    return 1 if mismatches or unevaluable else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
