#!/usr/bin/env python3
"""Attribute external RSS samples to app phases; never mix diagnostics with timings."""
import argparse
import bisect
import json
import pathlib

parser = argparse.ArgumentParser()
parser.add_argument('roots', nargs='+')
parser.add_argument('--output', required=True)
args = parser.parse_args()
results = []
for root in args.roots:
    for path in sorted(pathlib.Path(root).iterdir()):
        if not (path / 'accepted.json').exists():
            continue
        phases = json.loads((path / 'phases.json').read_text())
        times = [p['UnixNS'] for p in phases]
        buckets = {}
        for sample in json.loads((path / 'rss.json').read_text()):
            if len(sample) < 3:
                raise ValueError(f'{path}: RSS samples lack wall-clock timestamps')
            _, rss, timestamp = sample
            index = bisect.bisect_right(times, timestamp) - 1
            name = phases[index]['Name'] if index >= 0 else 'process-start'
            bucket = buckets.setdefault(name, dict(samples=0, peak_rss=0, peak_unix_ns=0))
            bucket['samples'] += 1
            if rss > bucket['peak_rss']:
                bucket.update(peak_rss=rss, peak_unix_ns=timestamp)
        manifest = json.loads((path / 'manifest.json').read_text())
        peak_phase = max(buckets, key=lambda name: buckets[name]['peak_rss'])
        results.append(dict(run=path.name, provider=manifest['Provider'],
                            diagnostic=(path / 'memory-diagnostic.json').exists(),
                            peak_phase=peak_phase, phases=buckets))
output = pathlib.Path(args.output)
output.mkdir(parents=True, exist_ok=True)
(output / 'phases.json').write_text(json.dumps(results, indent=2))
lines = ['# RSS by application phase', '',
         'External RSS sampled about every 100 ms. Unsampled short phases are absent, not zero. '
         'Each value is a process RSS high-water within that phase, not memory owned by the phase. '
         'Diagnostic captures perturb memory and are listed separately.', '',
         '|Provider / run|Diagnostic|Peak phase|Request waves MiB|Verification MiB|Overall MiB|',
         '|---|---|---|---:|---:|---:|']
for result in results:
    phases = result['phases']
    def peak(names):
        values = [v['peak_rss'] for k, v in phases.items() if names(k)]
        return f'{max(values)/1048576:.2f}' if values else 'not sampled'
    lines.append('|{} / {}|{}|{}|{}|{}|{}|'.format(
        result['provider'], result['run'], result['diagnostic'], result['peak_phase'],
        peak(lambda k: k.startswith('turn-')), peak(lambda k: k == 'verification'),
        peak(lambda k: True)))
(output / 'PHASES.md').write_text('\n'.join(lines) + '\n')
