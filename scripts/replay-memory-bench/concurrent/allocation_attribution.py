#!/usr/bin/env python3
"""Difference full-sampling profiles by allocation stack; never use as timings."""
import argparse
import collections
import json
from pathlib import Path
import subprocess

KEYS = ['allocated_objects','reclaimed_objects','net_live_objects','allocated_bytes','reclaimed_bytes','net_live_bytes']


def category(stack):
    if any(s.startswith('runtime/pprof.') or 'TestConcurrentFlow.func5 ' in s for s in stack):
        return 'Diagnostic snapshot machinery (excluded from app rankings)'
    if any('websocket.(*Conn).setupReadTimeout ' in s for s in stack):
        return 'WebSocket read cancellation setup'
    if any('forwardResponsesWebsocketEvent ' in s for s in stack):
        return 'WebSocket to SSE framing'
    if any('responsesWebsocketDecoder).decode ' in s for s in stack):
        return 'WebSocket event decoding/inspection'
    for s in stack:
        if 'github.com/openvibely/openvibely/' in s and 'TestConcurrentFlow' not in s and 'appFlow' not in s:
            return s.split(' ')[0].replace('github.com/openvibely/openvibely/', '')
    if any('/concurrent_measure_test.go:' in s for s in stack):
        return 'Other harness work (kept visible)'
    return 'Runtime/library/background work'


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory')
    p.add_argument('--helper', required=True)
    p.add_argument('--output', required=True)
    a = p.parse_args()
    out = Path(a.output)
    out.mkdir(parents=True, exist_ok=True)
    (out/'stacks').mkdir(exist_ok=True)
    summaries = []
    lines = ['# Full allocation-site diagnostics', '',
             'Current branch only, one diagnostic run per scenario. MemProfileRate=1; two forced GCs at each snapshot. These runs are deliberately perturbed and excluded from performance reports. Counts are cumulative-profile differences at allocation sites. Reclaimed counts can include objects allocated before the interval; net live is the change in outstanding allocations at that site, not a cohort survival rate.', '',
             'The middle interval lies between approximately 1 and 3 seconds after wave start. The report checks it against peer streaming timestamps. Early and late intervals include some streaming: they are not pure preparation/completion. Full stacks are retained. Snapshot machinery is shown separately and omitted only from app rankings.', '']
    for d in sorted(Path(a.directory).iterdir()):
        if not (d/'storage.json').exists(): continue
        assert (d/'allocation-diagnostic.json').exists()
        mode, profile, workers, variant, rep = d.name.rsplit('-',4)
        model = json.loads((d/'model.json').read_text())
        assert model['max_active'] == int(workers)
        for turn in [0,5]:
            captures = {c['Name']: c for c in json.loads((d/f'allocation-captures-{turn}.json').read_text())}
            streams = [r for r in model['requests'] if r['turn']==turn and r.get('first_text_delta_unix_ns')]
            assert len(streams)==int(workers)
            first = max(r['first_text_delta_unix_ns'] for r in streams)
            last = min(r['last_text_delta_unix_ns'] for r in streams)
            valid = first+100_000_000 < captures['stream-start']['StartNS'] and captures['stream-end']['EndNS'] < last-100_000_000
            lines += [f'## {mode}, {workers} tasks, turn {turn}', '', f'Middle snapshot interval entirely inside all-stream overlap (100 ms margins): **{valid}**.', '']
            for window, before, after in [('whole-wave','before','after'),('preparation-plus-early-text','before','stream-start'),('middle-text','stream-start','stream-end'),('late-text-plus-completion','stream-end','after')]:
                raw = subprocess.check_output([a.helper, str(d/f'alloc-{turn}-{before}.pprof'),str(d/f'alloc-{turn}-{after}.pprof')])
                sites = json.loads(raw)
                (out/'stacks'/f'{d.name}-turn-{turn}-{window}.json').write_bytes(raw)
                groups = collections.defaultdict(lambda: dict.fromkeys(KEYS,0))
                for site in sites:
                    group = groups[category(site['stack'])]
                    for k in KEYS: group[k] += site[k]
                grouped = [dict(source=k,**v) for k,v in groups.items()]
                grouped.sort(key=lambda x:x['allocated_objects'],reverse=True)
                instrumentation = [r for r in grouped if r['source'].startswith('Diagnostic snapshot')]
                apps = [r for r in grouped if not r['source'].startswith('Diagnostic snapshot')]
                total = {k:sum(r[k] for r in apps) for k in KEYS}
                summary = dict(run=d.name,mode=mode,workers=int(workers),turn=turn,window=window,middle_overlap_valid=valid,counts_excluding_snapshot=total,snapshot_machinery=instrumentation,groups=grouped)
                summaries.append(summary)
                lines += [f'### {window}', '', '|Allocation stack group|Created|Reclaimed|Net live change|Allocated MiB|', '|---|---:|---:|---:|---:|']
                for r in apps[:12]:
                    lines.append(f"|{r['source']}|{r['allocated_objects']:,}|{r['reclaimed_objects']:,}|{r['net_live_objects']:+,}|{r['allocated_bytes']/2**20:.3f}|")
                lines += ['', 'Snapshot machinery (excluded above): ' + ', '.join(f"{r['allocated_objects']:,} allocations, {r['allocated_bytes']/2**20:.2f} MiB" for r in instrumentation) + '.', '']
    assert summaries
    (out/'attribution.json').write_text(json.dumps(summaries,indent=2))
    (out/'SITES.md').write_text('\n'.join(lines))

if __name__=='__main__': main()
