#!/usr/bin/env python3
"""Attribute sampled resource intervals to observed model text-stream phases.

Only sample intervals wholly inside a phase are included. No interpolation or
claim that peer send times equal app receive times. Raw boundaries are retained.
"""
import argparse
import bisect
import json
from pathlib import Path
import statistics

p = argparse.ArgumentParser()
p.add_argument('directories', nargs='+')
p.add_argument('--output', required=True)
a = p.parse_args()
rows = []
for root in a.directories:
    for d in sorted(Path(root).iterdir()):
        if not (d / 'storage.json').exists():
            continue
        if any(d.glob('runtime-*.trace')) or (d/'allocation-diagnostic.json').exists():
            raise ValueError(f'{d}: diagnostic run is not a performance measurement')
        mode, profile, workers, variant, rep = d.name.rsplit('-', 4)
        workers = int(workers)
        requests = json.loads((d / 'model.json').read_text())['requests']
        for turn in json.loads((d / 'turns.json').read_text()):
            streams = [r for r in requests if r['turn'] == turn['turn'] and r.get('first_text_delta_unix_ns')]
            assert len(streams) == workers, (d, turn['turn'], len(streams))
            start = turn['wave_start_unix_ns']
            firsts = [(r['first_text_delta_unix_ns'] - start) / 1e6 for r in streams]
            lasts = [(r['last_text_delta_unix_ns'] - start) / 1e6 for r in streams]
            # Exclude 100 ms on either side of the all-streams overlap so local
            # delivery/buffering and startup/drain do not masquerade as steady text.
            bounds = [0, min(firsts), max(firsts) + 100, min(lasts) - 100, max(lasts), turn['elapsed_ms']]
            assert bounds == sorted(bounds), (d, bounds)
            samples = json.loads((d / f"samples-{turn['turn']}.json").read_text())
            end = samples[-1]['MS']
            samples = [s for s in samples[:-1] if s['MS'] < end] + samples[-1:]
            phases = ['pre-text', 'ramp-up', 'steady-text', 'drain', 'completion-tail']
            all_assigned = 0
            for label, lo, hi in zip(phases, bounds, bounds[1:]):
                inside = [s for s in samples if lo <= s['MS'] <= hi]
                row = dict(run=d.name, mode=mode, profile=profile, workers=workers, variant=variant, repetition=int(rep), turn=turn['turn'], phase=label, start_ms=lo, end_ms=hi, samples=len(inside))
                if len(inside) >= 2:
                    x, y = inside[0], inside[-1]
                    sec = (y['MS'] - x['MS']) / 1000
                    alloc = y['Alloc'] - x['Alloc']
                    objs = y['Objects'] - x['Objects']
                    all_assigned += alloc
                    row.update(measured_ms=sec*1000, cpu_ms=y['CPU']-x['CPU'], allocated_bytes=alloc, objects=objs, objects_s=objs/sec, objects_s_per_task=objs/sec/workers, alloc_mib_s=alloc/sec/2**20, peak_heap=max(s['Heap'] for s in inside), gc_pause_ns=y['Pause']-x['Pause'])
                    brief = [(s['CPU']-prev['CPU'])/((s['MS']-prev['MS'])/1000*10) for prev,s in zip(inside,inside[1:]) if s['MS']-prev['MS']>=40]
                    rates = []
                    times = [s['MS'] for s in inside]
                    for s in inside:
                        j = bisect.bisect_right(times, s['MS']-1000)-1
                        if j >= 0:
                            prev = inside[j]
                            dt = (s['MS']-prev['MS'])/1000
                            rates.append(((s['Objects']-prev['Objects'])/dt, (s['Alloc']-prev['Alloc'])/dt/2**20))
                    row.update(peak_sample_cpu_percent=max(brief, default=None), peak_1s_objects_s=max((r[0] for r in rates),default=None), peak_1s_alloc_mib_s=max((r[1] for r in rates),default=None))
                rows.append(row)
            # Keep crossing intervals visible instead of hiding the very bursts
            # this analysis is intended to explain. These disjoint intervals do
            # not support a continuous one-second rate.
            crossing = [(x,y) for x,y in zip(samples,samples[1:]) if not any(lo <= x['MS'] and y['MS'] <= hi for lo,hi in zip(bounds,bounds[1:]))]
            sec = sum((y['MS']-x['MS'])/1000 for x,y in crossing)
            alloc = sum(y['Alloc']-x['Alloc'] for x,y in crossing)
            objects = sum(y['Objects']-x['Objects'] for x,y in crossing)
            assert alloc + all_assigned == turn['alloc_bytes'], (d, turn['turn'])
            row = dict(run=d.name, mode=mode, profile=profile, workers=workers, variant=variant, repetition=int(rep), turn=turn['turn'], phase='boundary-intervals', allocated_bytes=alloc)
            if sec > 0:
                row.update(measured_ms=sec*1000, cpu_ms=sum(y['CPU']-x['CPU'] for x,y in crossing), objects=objects, objects_s_per_task=objects/sec/workers, peak_heap=max(max(x['Heap'],y['Heap']) for x,y in crossing), peak_sample_cpu_percent=max(((y['CPU']-x['CPU'])/((y['MS']-x['MS'])/1000*10) for x,y in crossing if y['MS']-x['MS']>=40),default=None))
            rows.append(row)

out = Path(a.output)
out.mkdir(parents=True, exist_ok=True)
(out/'phases.json').write_text(json.dumps(rows,indent=2))
lines=['# Resource use by observed text-stream phase', '', 'Main → branch: medians of three runs when three are supplied. Every wave is separate. CPU is processor time; peak CPU uses ≥40 ms sample intervals. All counts cover the shared app process. Per-task rates divide by task count; they are not measurements of individual goroutines.', '', 'Phases use peer first/last final-answer delta timestamps on the same host. Pre-text includes preparation and, in tool-use, tool work. Steady-text is the overlap when every task is emitting its final answer, trimmed by 100 ms at each edge. Ramp-up/drain retain overlapping work. Completion-tail begins after the last text delta is sent. These are observable windows, not exact application-function boundaries. Short phases may have no complete sample interval (n/a). Boundary-crossing intervals are shown separately, never silently included in steady streaming. Their rates cover disjoint intervals, not a continuous second. Refer to the whole-wave report for complete peaks and totals.', '']
keys=[('Measured ms','measured_ms',1),('CPU ms','cpu_ms',1),('Allocated MiB','allocated_bytes',2**20),('Objects/s per task','objects_s_per_task',1),('Peak 1s objects/s, all tasks','peak_1s_objects_s',1),('Peak ≥40ms CPU %','peak_sample_cpu_percent',1),('Peak heap MiB','peak_heap',2**20)]
for mode, profile, workers in sorted(set((r['mode'],r['profile'],r['workers']) for r in rows)):
    lines += [f'## {mode}, {profile}, {workers} task(s)', '', '|Turn|Phase|'+'|'.join(k[0] for k in keys)+'|', '|---|---|'+'---:|'*len(keys)]
    subset=[r for r in rows if (r['mode'],r['profile'],r['workers'])==(mode,profile,workers)]
    for turn in range(6):
        for phase in ['pre-text','ramp-up','steady-text','drain','completion-tail','boundary-intervals']:
            cells=[]
            for _,key,scale in keys:
                values=[]
                for v in ['main','branch']:
                    data=[r[key] for r in subset if r['turn']==turn and r['phase']==phase and r['variant']==v and r.get(key) is not None]
                    values.append(f'{statistics.median(data)/scale:.2f}' if data else 'n/a')
                cells.append(' → '.join(values))
            lines.append('|'+str(turn)+'|'+phase+'|'+'|'.join(cells)+'|')
    lines += ['']
(out/'PHASES.md').write_text('\n'.join(lines)+'\n')
