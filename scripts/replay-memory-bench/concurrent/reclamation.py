#!/usr/bin/env python3
"""Report natural allocation/reclamation counters, with exact net-live checks.

Frees in an interval can reclaim objects allocated in previous intervals/turns.
Nothing here measures the time an individual object became unreachable.
"""
import argparse
import bisect
import json
from pathlib import Path
from statistics import median


def analyze(root):
    rows = []
    phases = []
    for d in sorted(Path(root).iterdir()):
        if not (d / 'storage.json').exists():
            continue
        if (d / 'allocation-diagnostic.json').exists() or any(d.glob('runtime-*.trace')):
            raise ValueError(f'{d}: diagnostic run must not enter natural GC measurements')
        mode, profile, workers, variant, rep = d.name.rsplit('-', 4)
        workers = int(workers)
        peer = json.loads((d / 'model.json').read_text())
        assert peer['max_active'] == workers
        assert all(r['prior_turn_seen'] or r['checkpoint_seen'] for r in peer['requests'])
        turns = json.loads((d / 'turns.json').read_text())
        assert len(turns) == 6
        for turn in turns:
            samples = json.loads((d / f"samples-{turn['turn']}.json").read_text())
            end = samples[-1]['MS']
            samples = [s for s in samples[:-1] if s['MS'] < end] + samples[-1:]
            first = samples[0]
            for s in samples:
                assert s['Objects'] - s['Frees'] == s['LiveObjects'] - first['LiveObjects'], (d, turn['turn'], s)
                assert s['Alloc'] - s['FreedBytes'] == s['Heap'] - first['Heap'], (d, turn['turn'], s)
            assert samples[-1]['Frees'] == turn['free_count']
            assert turn['alloc_count'] - turn['free_count'] == turn['end_live_objects'] - turn['start_live_objects']
            times = [s['MS'] for s in samples]
            rates = []
            for s in samples:
                j = bisect.bisect_right(times, s['MS'] - 1000) - 1
                if j >= 0:
                    prev = samples[j]
                    sec = (s['MS'] - prev['MS']) / 1000
                    rates.append({k: (s[k] - prev[k]) / sec for k in ['Objects', 'Frees', 'Alloc', 'FreedBytes']})
            row = dict(turn, run=d.name, mode=mode, workers=workers, repetition=int(rep), variant=variant)
            row['net_live_objects'] = turn['end_live_objects'] - turn['start_live_objects']
            row['net_heap_bytes'] = turn['end_heap'] - turn['start_heap']
            row['peak_1s_allocations_s'] = max((r['Objects'] for r in rates), default=None)
            row['peak_1s_frees_s'] = max((r['Frees'] for r in rates), default=None)
            row['peak_1s_reclaimed_bytes_s'] = max((r['FreedBytes'] for r in rates), default=None)
            rows.append(row)
            streams = [r for r in peer['requests'] if r['turn'] == turn['turn'] and r.get('first_text_delta_unix_ns')]
            assert len(streams) == workers
            start = turn['wave_start_unix_ns']
            firsts = [(r['first_text_delta_unix_ns'] - start) / 1e6 for r in streams]
            lasts = [(r['last_text_delta_unix_ns'] - start) / 1e6 for r in streams]
            bounds = [0, min(firsts), max(firsts) + 100, min(lasts) - 100, max(lasts), end]
            assert bounds == sorted(bounds), (d, bounds)
            labels = ['pre-text', 'ramp-up', 'steady-text', 'drain', 'completion-tail']
            buckets = {label: [] for label in labels + ['boundary-intervals']}
            for x, y in zip(samples, samples[1:]):
                label = next((label for label, lo, hi in zip(labels, bounds, bounds[1:]) if lo <= x['MS'] and y['MS'] <= hi), 'boundary-intervals')
                buckets[label].append((x, y))
            sums = dict.fromkeys(['Objects', 'Frees', 'Alloc', 'FreedBytes'], 0)
            for label, intervals in buckets.items():
                r = dict(run=d.name, mode=mode, workers=workers, repetition=int(rep), turn=turn['turn'], phase=label, samples=len(intervals))
                sec = sum((y['MS'] - x['MS']) / 1000 for x, y in intervals)
                r['seconds'] = sec
                for key in sums:
                    value = sum(y[key] - x[key] for x, y in intervals)
                    sums[key] += value
                    r[key] = value
                    r[key + '_s_per_task'] = value / sec / workers if sec else None
                r['net_objects'] = r['Objects'] - r['Frees']
                phases.append(r)
            for key in sums:
                assert sums[key] == samples[-1][key], (d, key)
    return rows, phases


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory')
    p.add_argument('--output', required=True)
    a = p.parse_args()
    rows, phases = analyze(a.directory)
    assert rows, 'no completed measurements'
    out = Path(a.output)
    out.mkdir(parents=True, exist_ok=True)
    (out / 'reclamation.json').write_text(json.dumps(dict(turns=rows, phases=phases), indent=2))
    lines = ['# Natural allocation and reclamation measurements', '',
             'Current branch only, medians of supplied repetitions. No profiling or forced GC within turns. Each row covers a full wave, not one task. Freed objects/bytes may have been allocated in an earlier turn. Peak creation and reclamation rates can occur in different seconds. Medians of separate counters need not subtract exactly; raw samples satisfy net-live identities.', '',
             'These are Go heap counters, not native SQLite/browser memory or object lifetime tracking. Go can batch multiple tiny pointer-free allocations into one block, so counts are runtime allocation units rather than a one-to-one count of language-level objects. Reclaimed Go heap is reusable by the allocator; it need not be returned immediately to the OS.', '']
    groups = sorted({(r['mode'], r['workers']) for r in rows})
    def fmt(data, key, scale=1):
        values = [r[key] for r in data if r.get(key) is not None]
        return f'{median(values)/scale:,.2f}' if values else 'n/a'
    for mode, workers in groups:
        selected = [r for r in rows if (r['mode'], r['workers']) == (mode, workers)]
        lines += [f'## {mode}, {workers} task(s)', '', '|Turn|Created objects|Reclaimed objects|Net live objects|Allocated MiB|Reclaimed MiB|End live objects|End heap MiB|Peak created/sec|Peak reclaimed/sec|', '|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|']
        keys = [('alloc_count',1),('free_count',1),('net_live_objects',1),('alloc_bytes',2**20),('freed_bytes',2**20),('end_live_objects',1),('end_heap',2**20),('peak_1s_allocations_s',1),('peak_1s_frees_s',1)]
        for turn in range(6):
            data = [r for r in selected if r['turn'] == turn]
            lines.append('|' + str(turn) + '|' + '|'.join(fmt(data,k,s) for k,s in keys) + '|')
        lines += ['', '|Turn|Phase|Covered seconds|Created/sec/task|Reclaimed/sec/task|Net live objects, all tasks|', '|---|---|---:|---:|---:|---:|']
        for turn in range(6):
            for phase in ['pre-text','ramp-up','steady-text','drain','completion-tail','boundary-intervals']:
                data = [r for r in phases if (r['mode'],r['workers'],r['turn'],r['phase']) == (mode,workers,turn,phase)]
                lines.append('|' + str(turn) + '|' + phase + '|' + '|'.join(fmt(data,k) for k in ['seconds','Objects_s_per_task','Frees_s_per_task','net_objects']) + '|')
        lines += ['']
    lines += ['Phase boundaries use peer first/last final-text sends; steady-text excludes 100 ms at either edge of the overlap. Intervals crossing phase boundaries are retained separately. Short phases may lack a full sample interval (n/a). Boundary rates describe disjoint intervals, not a sustained rate. No claim of instantaneous object disposal can be made from GC reclamation times.', '']
    (out/'RESULTS.md').write_text('\n'.join(lines))

if __name__ == '__main__':
    main()
