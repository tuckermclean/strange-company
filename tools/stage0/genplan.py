"""Generate PLAN.md from plans/dag.yaml and the GATE-<node> sections (deterministic; ADR-0021).
Usage: python3 tools/stage0/genplan.py <repo-root>
"""
import re,glob,yaml,sys
repo=sys.argv[1]
plans={f.split('/')[-1]:open(f).read() for f in sorted(glob.glob(f'{repo}/plans/[0-9][0-9]-*.md'))}
title={k:(re.search(r'^#\s+(.+)$',v,re.M).group(1).strip() if re.search(r'^#\s+(.+)$',v,re.M) else k) for k,v in plans.items()}
def gates(node):
    out=[]
    for f,t in sorted(plans.items()):
        for m in re.finditer(rf"^###\s+GATE-{node}\b.*?$(.*?)(?=^##|\Z)", t, re.M|re.S):
            ids=sorted(set(re.findall(r"\b(\d\d-AC-\d+[a-z]?)\b", m.group(1))), key=lambda x:(x[:2],int(re.match(r'\d+',x[6:]).group()),x[6:]))
            if ids: out.append((f,ids))
    return out
d=yaml.safe_load(open(f'{repo}/plans/dag.yaml'))
phase_names={1:'Phase 1 — Foundations (no paid infrastructure; interim CI on GitHub Actions, ADR-0018)',2:'Phase 2 — Platform on one node (ends at the Bootstrap Definition of Done, SPEC §130)',3:'Phase 3 — Control plane (ends at the orchestration DoD: all fake TDD flows on the live cluster)',4:'Phase 4 — Real harness and the Self-Hosting Threshold (SPEC §117–§118)',6:'Phase 6 — Small-HA (sequenced after the threshold; hard dependency only on P12)'}
L=[]
L.append("# PLAN — Implementation sequence\n")
L.append("**Status:** Stage-0 output. Implementation has not started.\n**Generated from:** `plans/dag.yaml` plus the `### GATE-<node>` sections of every subsystem plan. Regenerate after any gate changes; do not hand-edit the per-node tables.\n**Authority:** subordinate to `SPEC.md` and `plans/00-master-plan.md`.\n")
L.append("""## How to execute a node

Every node is a bounded engineering task whose success criteria already exist (SPEC §132). For each node:

1. Read `SPEC.md`, `plans/00-master-plan.md`, `plans/reviews/consolidation-decisions.md`, then the node's owning plans and the contracts and ADRs they reference.
2. **Verification first (SPEC §1.2, §4.4).** Turn the node's gate criteria into executable tests or checks. Commit them **first** and show the tests failing for the right reason. Phase-1 nodes do this under the bootstrap discipline of plan 52; from P08 on, Woodpecker; from the threshold on, the Strange Company TDD workflow itself.
3. Implement until the gate's checks pass in trusted CI (interim GitHub Actions until P08, then Woodpecker; tier-E e2e lane per ADR-0303).
4. Record gate evidence (CI run links, check outputs) in `STATUS.md` under the node. A node is complete when its evidence passes, not when its files exist (SPEC §124).
5. A node may start only when every node in its `deps` has passed its gate. Nodes whose deps are satisfied may proceed in parallel.
6. Re-verify every `(verify)`-marked third-party assumption the node relies on. These are written as acceptance criteria in the owning plans. If an assumption is false, stop and amend the plan, recording it in `plans/spec-issues.md` if it touches SPEC.

Version-sensitive facts (chart values, API endpoints, CLI flags) are pinned and recorded in the version locks of plan 01 when the node is implemented.
""")
L.append("## Milestones\n")
for m in d['milestones']:
    L.append(f"- **{m['id']}** = gate of node {m['node']} ({m['spec']})")
L.append("")
cur=None
for n in d['nodes']:
    if n['phase']!=cur:
        cur=n['phase']; L.append(f"\n## {phase_names[cur]}\n")
    deps=', '.join(n['deps']) or '—'
    after=(' · sequenced after '+', '.join(n['after'])) if n.get('after') else ''
    L.append(f"### {n['id']} — {n['name']}\n")
    L.append(f"- **Depends on:** {deps}{after}")
    L.append(f"- **Owning plans:** " + ', '.join(f"`plans/{f}`" for f in sorted(plans) if f[:2] in n['plans']))
    g=gates(n['id'])
    tot=sum(len(i) for _,i in g)
    L.append(f"- **Gate `GATE-{n['id']}`:** {tot} acceptance criteria")
    for f,ids in g:
        L.append(f"  - `plans/{f}`: {', '.join(ids)}")
    L.append("")
L.append("""## Critical path

F01 → F02 → F08 → P01 → P02 → P03 → P04 → P05 → P06 → P08 → P09 → P10 → P12 → P11 → C01 → (C03, C07) → C02 → C04 → C09 → C10 → R01 → R02. P07 runs beside P08. P11 (installation supply chain) runs after control transfer (CD-19).

## Operator decisions that affect sequencing
- **Node size (SI-01/SI-12).** It must be decided before P02 orders hardware. See `STATUS.md`.
- **OVH product line (plan 04 spike S-1..S-7).** It is decided inside F09/P02. The `existing` provider is always available as the fallback.
""")
open(f'{repo}/PLAN.md','w').write('\n'.join(L)+'\n')
print('PLAN.md lines', len(L))
