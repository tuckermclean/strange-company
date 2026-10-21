"""Generates corpus.json for the F06 report-recogniser oracle test (decisions D26 and D27).

Usage:  python3 -I gen_corpus.py.fx <markdown-it-py-3.0.0-lib-dir> <seed> <n-random> <out.json>
Recorded with seed 27, 3000 random entries (plus the fixed cases below, wrapped three ways each, then
de-duplicated), markdown-it-py 3.0.0 in "commonmark" mode, on 2026-10-21 (D27 re-record; the D26 recording used
seed 26).

The oracle verdict for an AC id is: the id appears (matching the same word-boundary regex the recogniser uses)
in the visible text of at least one rendered heading (ATX or setext, any level, at any nesting depth). Visible
text is the concatenation of the heading's inline text and inline-code tokens, soft/hard breaks as a space;
images, raw inline HTML, link destinations and titles are not visible text.
"""
import json, random, re, sys

sys.path.insert(0, sys.argv[1])
from markdown_it import MarkdownIt  # noqa: E402

md = MarkdownIt("commonmark")
ACS = ["AC-1", "AC-2"]
IDRE = {a: re.compile(r"(^|[^A-Za-z0-9-])" + re.escape(a) + r"($|[^A-Za-z0-9])") for a in ACS}


def vis(children):
    s = []
    for c in children:
        if c.type == "image":
            continue
        if c.type in ("text", "code_inline"):
            s.append(c.content)
        elif c.type in ("softbreak", "hardbreak"):
            s.append(" ")
    return "".join(s)


def verdict(report):
    out = {a: False for a in ACS}
    toks = md.parse(report)
    for i, t in enumerate(toks):
        if t.type == "heading_open":
            v = vis(toks[i + 1].children)
            for a in ACS:
                if IDRE[a].search(v):
                    out[a] = True
    return out


FIXED = [
    # review 4 repros (D26 a)
    "- x\n\n  ```\n  y\n```\n## AC-1\n```\n",
    "-\n  ```\n  y\n```\n## AC-1\n```\n",
    "1. x\n\n   ```\n   y\n ```\n## AC-1\n```\n",
    "* x\n  ~~~\n~~~\n## AC-1\n~~~\n",
    "- x\n    ```\n  ## AC-1\n",
    "- x\n\t```\n  ## AC-1\n",
    # D23
    "<!--\n## AC-1\n-->\n",
    "<script>\n## AC-1\n</script>\n",
    "<pre>\n## AC-1\n</pre>\n",
    "````\n```\n## AC-1\n````\n",
    "```\n```go\n## AC-1\n```\n",
    "~~~\n~~~ x\n## AC-1\n~~~\n",
    "~~~~\n~~~\n## AC-1\n~~~~\n",
    "```\n~~~\n## AC-1\n```\n",
    "# R\r```\r## AC-1\r```\n",
    "AC-1\n====\n",
    "AC-1\n----\n",
    "    ## AC-1\n",
    "```\nx\n   ```\n## AC-1\n",
    "```\nx\n    ```\n## AC-1\n```\n",
    "````\n```\nx\n`````\n## AC-1\n",
    "   ###### AC-1\n",
    "```\n## AC-1\n```\n\n## AC-1 again\n",
    # D24
    "- ```\n  ## AC-1\n  ```\n",
    "1. ```\n   ## AC-1\n   ```\n",
    "* ~~~\n  ## AC-1\n  ~~~\n",
    "- ```\n  ## AC-1\n",
    "- <!--\n  ## AC-1\n  -->\n",
    "+ <div>\n  ## AC-1\n",
    "> ```\n> ## AC-1\n> ```\n",
    "- - <div>\n    ## AC-1\n",
    "- a\n  - ```\n    ## AC-1\n    ```\n",
    "> - ```\n>   ## AC-1\n",
    "1) ```\n   ## AC-1\n",
    "-\t<pre>\n  ## AC-1\n",
    "## <!-- AC-1 -->\n",
    "## <AC-1>\n",
    "## [](AC-1)\n",
    "## ![](AC-1)\n",
    "## [x](AC-1)\n",
    "## AC-1 <b>x</b>\n",
    "## AC-1\n\n- one\n- two\n\n1. first\n2. second\n",
    "- see <b>this</b> now\n\n## AC-1\n",
    "- 3 items < 5\n\n## AC-1\n",
    # D25
    "## <span title=\"a>AC-1\">x</span>\n",
    "## <span title='a>AC-1'>x</span>\n",
    "## [x](<a)AC-1>)\n",
    "## [x](a\\)AC-1)\n",
    "## [x](a(b)AC-1)\n",
    "## [x](u \"a)AC-1\")\n",
    "## [x][AC-1]\n\n[AC-1]: /u\n",
    "## ![AC-1](u)\n",
    "## <span hidden>AC-1</span>\n",
    "## AC-1 [link](x)\n",
    "## AC-1 a < b\n",
    "## [[AC-1]](u)\n",
    # review 4 extras
    "## `<` AC-1\n",
    "## *<*AC-1\n",
    "## [AC-1]\n\n[AC-1]: /u\n",
    "## [AC-1][]\n\n[AC-1]: /u\n",
    "## x[^AC-1]\n\n[^AC-1]: note\n",
    "## x\nAC-1\n",
    "## x\\\nAC-1\n",
    "---\n## AC-1\n---\n",
    "## &lt;x AC-1\n",
    "## AC-1 ##\n",
    # D27 (a) repros: indented HTML opener on a list-item continuation line
    "- x\n\n\t<div>\n  ## AC-1\n",
    "- x\n\n    <div>\n  ## AC-1\n",
    "- x\n    <div>\n  ## AC-1\n",
    "* x\n\n\t<!--\n   ## AC-1\n",
    "- x\n\t <!--\n  ## AC-1\n-->\n",
    # D27 (a) variants
    "- x\n\n <div>\n  ## AC-1\n",
    "- x\n\n  <div>\n  ## AC-1\n",
    "1. x\n\n   <pre>\n   ## AC-1\n   </pre>\n",
    "1. x\n\n     <div>\n   ## AC-1\n",
    "> - x\n>\n>     <div>\n>   ## AC-1\n",
    "> - x\n>\n>\t<!--\n>   ## AC-1\n",
    "- > x\n  >\n  >     <div>\n  >   ## AC-1\n",
    "- x\n\n      <script>\n  ## AC-1\n",
    # D27 (b) twins: `<` lines inside a top-level fence
    "```xml\n<settings>\n  <cache/>\n```\n## AC-1\n",
    "```go\n  <-done\n```\n## AC-1\n",
    "```\n<!--\n```\n## AC-1\n",
    "~~~html\n<div>\n<pre>\n~~~\n## AC-1\n",
    "## AC-1\n```xml\n<settings>\n</settings>\n```\n",
]

# Fragment pools. Each generator returns one line.
IND = ["", "", "", " ", "  ", "   ", "    ", "\t", "  \t"]
MARK = ["- ", "* ", "+ ", "1. ", "2) ", "10. ", "-\t", "-", "1.", "> ", ">", "- - ", "> - ", "- > ", "1. - ", "* * "]
FENCE = ["```", "~~~", "````", "~~~~", "`````", "``"]
HEAD = ["## AC-1", "## AC-2", "# AC-1", "### AC-1 x", "## x AC-1", "## `AC-1`", "## AC-1 ##", "#\tAC-1",
        "## <AC-1>", "## [x](AC-1)", "## [AC-1]", "## ![x](u) AC-1", "## *AC-1*", "## <b>AC-1</b>", "## AC-1 <b>x</b>",
        "## x &lt; AC-1", "## [x][AC-1]", "[AC-1]: /u", "## `<` AC-1", "## AC-1\\", "AC-1", "AC-1\n===", "## AC-2"]
PLAIN = ["x", "y", "text", "", "", "***", "---", "<div>", "<!--", "-->", "<pre>", "</pre>", "<script>", "<b>x</b>",
         "a < b", "`code`", "[l](u)", "*em*", "    code", "\ttabbed"]


def line(rng, mode):
    r = rng.random()
    if mode == "list_fence":
        w = [(0.22, "fence"), (0.18, "mark_fence"), (0.2, "head"), (0.18, "mark_text"), (0.12, "plain"), (0.1, "indent_head")]
    elif mode == "html":
        w = [(0.15, "fence"), (0.1, "mark_fence"), (0.25, "head"), (0.1, "mark_text"), (0.4, "plain")]
    else:
        w = [(0.12, "fence"), (0.08, "mark_fence"), (0.3, "head"), (0.15, "mark_text"), (0.3, "plain"), (0.05, "indent_head")]
    acc = 0
    kind = w[-1][1]
    for p, k in w:
        acc += p
        if r < acc:
            kind = k
            break
    if kind == "fence":
        f = rng.choice(FENCE)
        tail = rng.choice(["", "", "", "go", " x", "`", " AC-1"])
        return rng.choice(IND) + f + tail
    if kind == "mark_fence":
        return rng.choice(IND[:4]) + rng.choice(MARK) + rng.choice(FENCE) + rng.choice(["", "", "go"])
    if kind == "head":
        return rng.choice(IND[:5]) + rng.choice(HEAD)
    if kind == "indent_head":
        return rng.choice(IND) + rng.choice(HEAD[:4])
    if kind == "mark_text":
        return rng.choice(IND[:5]) + rng.choice(MARK) + rng.choice(PLAIN)
    return rng.choice(PLAIN)


def item_fence_body(rng):
    """A list item (or blockquote) holding a fence whose closer is outdented or missing, then headings: the
    class where a line-based recogniser and CommonMark disagree about what is code."""
    lines = []
    if rng.random() < 0.5:
        lines.append(rng.choice(PLAIN[:4]))
    marker = rng.choice(MARK[:12])
    cont = " " * rng.choice([2, 2, 3, 3, 4, 1, 0])
    f = rng.choice(FENCE[:5])
    if rng.random() < 0.4:
        lines.append(marker + f)
    else:
        lines.append(marker + rng.choice(PLAIN[:4]))
        if rng.random() < 0.5:
            lines.append("")
        lines.append(rng.choice([cont, " " * rng.randint(1, 4), "\t"]) + f)
    for _ in range(rng.randint(0, 3)):
        lines.append(rng.choice([cont, ""]) + rng.choice(["y", "## AC-1", "x", "", "## AC-2", "    ## AC-1"]))
    if rng.random() < 0.8:
        closer = f[0] * rng.choice([len(f), len(f), len(f) + 1, 3])
        lines.append(rng.choice(["", " ", "  ", "   ", cont, cont]) + closer)
    for _ in range(rng.randint(0, 3)):
        lines.append(rng.choice(["", " ", "  ", "   "]) + rng.choice(HEAD[:8] + PLAIN[:4] + ["```", "~~~"]))
    return "\n".join(lines) + "\n"


HTML_OPEN = ["<div>", "<!--", "<pre>", "<script>", "<b>x</b>", "<span>", "</div>", "<style>", "<p>", "<?x", "<![CDATA[",
             "<a href=\"u\">", "<br/>", "< x", "<-", "<settings>"]
HIND = ["\t", " ", "  ", "   ", "    ", "     ", "      ", "\t ", " \t", "  \t"]


def item_html_body(rng):
    """D27(c): a list item or blockquote whose continuation line carries an indented HTML opener (tab, 1-6 spaces)
    that CommonMark renders as an HTML block, hiding the headings after it."""
    lines = []
    quote = rng.random() < 0.35
    q = "> " if quote else ""
    marker = rng.choice(["- ", "* ", "+ ", "1. ", "2) ", "10. ", "-\t", "- - "])
    cont = " " * rng.choice([2, 2, 3, 3, 4, 0, 1])
    if rng.random() < 0.3:
        lines.append(q + marker.rstrip())
    else:
        lines.append(q + marker + rng.choice(PLAIN[:4] + ["see <b>this</b>", "3 < 5"]))
    if rng.random() < 0.5:
        lines.append(q.rstrip())
    for _ in range(rng.randint(1, 2)):
        lines.append(q + rng.choice(HIND + [cont]) + rng.choice(HTML_OPEN))
    for _ in range(rng.randint(1, 3)):
        lines.append(q + rng.choice([cont, "  ", "", " ", "    "]) + rng.choice(
            ["## AC-1", "## AC-2", "y", "", "## AC-1", "-->", "</div>", "</pre>", "    ## AC-1"]))
    if rng.random() < 0.3:
        lines.append(rng.choice(["", " ", "  "]) + rng.choice(["## AC-1", "-->", "</pre>"]))
    return "\n".join(lines) + "\n"


def fenced_lt_body(rng):
    """D27(c): a top-level fence at column 0 holding XML/HTML or `<-` lines (and sometimes an indented opener
    or heading text), then headings. Fenced `<` lines hide nothing."""
    f = rng.choice(FENCE[:5])
    info = rng.choice(["", "xml", "go", "html", " x"])
    inner = [rng.choice(["", "  ", "   ", "    ", "\t"]) + rng.choice(HTML_OPEN + ["<cache/>", "## AC-1", "x"])
             for _ in range(rng.randint(1, 4))]
    lines = [f + info] + inner
    if rng.random() < 0.85:
        lines.append(rng.choice(["", "", " ", "  "]) + f[0] * rng.choice([len(f), len(f), len(f) + 1]))
    for _ in range(rng.randint(0, 2)):
        lines.append(rng.choice(["## AC-1", "## AC-2", "x", "", "<div>", "## x"]))
    body = "\n".join(lines) + "\n"
    return rng.choice(["# R\n\n## AC-2\n\n", "## AC-2\n", "# R\n\n"]) + body


def random_report(rng):
    r0 = rng.random()
    if r0 < 0.22:
        return rng.choice(["# R\n\n## AC-2\n\n", "# R\n\n## AC-2\n\n", "## AC-2\n"]) + item_html_body(rng)
    if r0 < 0.34:
        return fenced_lt_body(rng)
    if rng.random() < 0.45:
        return rng.choice(["# R\n\n## AC-2\n\n", "# R\n\n## AC-2\n\n", "## AC-2\n"]) + item_fence_body(rng)
    mode = rng.choice(["list_fence", "list_fence", "list_fence", "html", "mixed", "mixed"])
    n = rng.randint(2, 11)
    lines = [line(rng, mode) for _ in range(n)]
    body = "\n".join(lines) + rng.choice(["\n", "\n", "\n\n", ""])
    r = rng.random()
    if r < 0.65:
        return "# R\n\n## AC-2\n\n" + body  # AC-2 plainly addressed: only AC-1 depends on the body
    if r < 0.8:
        return "## AC-2\n" + body
    if r < 0.9:
        return "# R\n\n" + body + "\n## AC-2\n"
    return body


def main():
    seed, n, out = int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
    rng = random.Random(seed)
    reports, seen, tries = [], set(), 0

    def add(rep):
        if rep not in seen:
            seen.add(rep)
            reports.append(rep)

    for f in FIXED:
        add("# R\n\n## AC-2\n\n" + f)
        add(f + "\n## AC-2\n")
        add(f + "\n## AC-1\n## AC-2\n")
    target = len(reports) + n
    while len(reports) < target and tries < 100 * n:
        tries += 1
        add(random_report(rng))
    entries = [{"report": r, "acs": ACS, "visible": verdict(r)} for r in reports]
    with open(out, "w") as fh:
        json.dump(entries, fh, ensure_ascii=True, separators=(",", ":"))
        fh.write("\n")


main()
