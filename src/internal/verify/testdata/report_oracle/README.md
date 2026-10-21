# Report-recogniser oracle corpus (F06 S6b, decision D26)

`corpus.json` is **generated, not hand-written**. It is an array of entries
`{"report": "...", "acs": ["AC-1","AC-2"], "visible": {"AC-1": bool, "AC-2": bool}}`.
`visible[id]` is the oracle's verdict on whether the AC id appears as visible text of a rendered heading
(h1-h6, ATX or setext, at any nesting depth, so also inside lists and blockquotes), outside code blocks.
Visible text is the heading's inline text and inline-code content, with soft and hard breaks as a space; images,
raw inline HTML and link destinations or titles are not visible text. The id must match the same word-boundary
pattern the recogniser uses.

- Generator: `gen_corpus.py.fx` in this directory (named `.fx` so that it is not an executable tracked script).
  It holds the fixed cases (review-4 repros and the D23, D24, D25 report cases, each wrapped three ways) and a
  seeded random part (lists, nested lists, blockquotes, fences with varied characters, run lengths and indents
  and outdented or missing closers, HTML blocks, inline markup, tabs, indentation), biased toward the class where a
  line-based recogniser and CommonMark disagree (fences inside list items).
- Oracle: markdown-it-py 3.0.0 (with mdurl 0.1.2), `MarkdownIt("commonmark")`.
- Seed 26, 3000 random entries, recorded 2026-10-21 (3198 entries after de-duplication and the fixed cases).
- Re-record: install the two wheels into a scratch directory, then
  `python3 -I gen_corpus.py.fx <that-dir> 26 3000 corpus.json`. The output is deterministic for a given seed and
  oracle version. Keep `corpus.json` under 1 MiB.
- Use: `TestReportRecogniser_NeverAddressesWhatCommonMarkHides_D26_22AC3` requires that when the oracle says an
  AC is not visible the recogniser does not call the report addressed (no fail-open); when every AC is visible
  either outcome is allowed (fail-closed is permitted).
- Follow-up: re-recording against GFM/goldmark (footnotes, front matter, tables) is not done here.
