#!/usr/bin/env python3
"""Separate concurrency/mode/profile rows; no combined performance score."""
import argparse,bisect,json,pathlib,statistics
p=argparse.ArgumentParser();p.add_argument('directories',nargs='+');p.add_argument('--output',required=True);a=p.parse_args()
rows=[]
for root in a.directories:
 for d in sorted(pathlib.Path(root).iterdir()):
  if not (d/'storage.json').exists():continue
  if any(d.glob('runtime-*.trace')) or (d/'allocation-diagnostic.json').exists() or (d/'cpu.pprof').exists():
   raise ValueError(f'{d}: diagnostic run is not a performance measurement')
  manifest=json.loads((d/'manifest.json').read_text())
  provider=manifest.get('Provider','openai')
  # mode contains a hyphen; profiles in this suite do not.
  mode,profile,workers,variant,rep=d.name.rsplit('-',4)
  if (pathlib.Path(root)/'experiment.json').exists():
   accepted=json.loads((d/'accepted.json').read_text())
   assert accepted==dict(provider=provider,variant=variant,repetition=int(rep),profiled=False),d
  turns=json.loads((d/'turns.json').read_text());deliveries=json.loads((d/'delivery.json').read_text())
  peer=json.loads((d/'model.json').read_text())
  assert len(turns)==6 and len(deliveries)==int(workers)*6, d
  assert peer['max_active']==int(workers),d
  assert all(r['prior_turn_seen'] or r['checkpoint_seen'] for r in peer['requests']),d
  for turn in turns:
   samples=json.loads((d/f"samples-{turn['turn']}.json").read_text())
   end=samples[-1]['MS']; samples=[s for s in samples[:-1] if s['MS']<end]+samples[-1:] # exclude a ticker racing past the measured stop
   times=[s['MS'] for s in samples];cpu=[];alloc=[];objects=[]
   instant_cpu=[];instant_alloc=[]
   for prev,s in zip(samples,samples[1:]):
    dt=(s['MS']-prev['MS'])/1000
    if dt>=.04:
     instant_cpu.append((s['CPU']-prev['CPU'])/(dt*10));instant_alloc.append((s['Alloc']-prev['Alloc'])/(dt*1048576))
   for i,s in enumerate(samples):
    j=bisect.bisect_right(times,s['MS']-1000)-1
    if j<0:continue
    prev=samples[j];dt=(s['MS']-prev['MS'])/1000
    cpu.append((s['CPU']-prev['CPU'])/(dt*10))
    alloc.append((s['Alloc']-prev['Alloc'])/(dt*1048576))
    objects.append((s['Objects']-prev['Objects'])/dt)
   turn.update(peak_sample_cpu_percent=max(instant_cpu,default=None),peak_sample_alloc_mib_s=max(instant_alloc,default=None),peak_1s_cpu_percent=max(cpu,default=None),peak_1s_alloc_mib_s=max(alloc,default=None),peak_1s_objects_s=max(objects,default=None))
  rows.append(dict(run=d.name,provider=provider,mode=mode,profile=profile,workers=int(workers),variant=variant,turns=turns,
   retained=json.loads((d/'heap.json').read_text()),storage=json.loads((d/'storage.json').read_text()),
   max_delta_gap_ms=max(x['MaxDeltaGapMS'] for x in deliveries),max_persist_gap_ms=max(x['MaxPersistGapMS'] for x in deliveries),
   peak_rss=max(x[1] for x in json.loads((d/'rss.json').read_text()))))
out=pathlib.Path(a.output);out.mkdir(parents=True,exist_ok=True);(out/'results.json').write_text(json.dumps(rows,indent=2))
lines=['# Concurrent streaming measurements','','Main → branch, medians of fresh processes. CPU % uses one core = 100%; peaks are rolling ≥1-second windows, not instantaneous peaks. Each turn row covers the concurrent wave, not one task. No pooled score.','']
for provider in sorted(set(r['provider'] for r in rows)):
 for mode in sorted(set(r['mode'] for r in rows)):
  for profile in sorted(set(r['profile'] for r in rows)):
   for workers in sorted(set(r['workers'] for r in rows)):
    selected=[r for r in rows if r['provider']==provider and (r['mode'],r['profile'],r['workers'])==(mode,profile,workers)]
    if not selected:continue
    lines += [f'## {provider}: {mode}, {profile}, {workers} task(s)','','|Turn|CPU ms|Allocated MiB|Objects|Peak heap MiB|Peak 1s CPU %|Peak 1s allocation MiB/s|GC pause ms|Elapsed ms|','|---|---:|---:|---:|---:|---:|---:|---:|---:|']
    for turn in range(6):
     cells=[]
     for key,scale in [('cpu_ms',1),('alloc_bytes',1048576),('alloc_count',1),('sampled_peak_heap',1048576),('peak_1s_cpu_percent',1),('peak_1s_alloc_mib_s',1),('gc_pause_ns',1000000),('elapsed_ms',1)]:
      vals=[]
      for version in ['main','branch']:
       data=[r['turns'][turn][key] for r in selected if r['variant']==version and r['turns'][turn][key] is not None]
       vals.append(f'{statistics.median(data)/scale:.2f}' if data else 'n/a')
      cells.append(' → '.join(vals))
     lines.append('|'+str(turn)+'|'+'|'.join(cells)+'|')
    lines+=['','|Turn|Peak sampled CPU % (~50 ms)|Peak sampled allocation MiB/s (~50 ms)|Peak 1s allocations/s|','|---|---:|---:|---:|']
    for turn in range(6):
     cells=[]
     for key in ['peak_sample_cpu_percent','peak_sample_alloc_mib_s','peak_1s_objects_s']:
      vals=[]
      for version in ['main','branch']:
       data=[r['turns'][turn][key] for r in selected if r['variant']==version and r['turns'][turn][key] is not None]
       vals.append(f'{statistics.median(data):.2f}' if data else 'n/a')
      cells.append(' → '.join(vals))
     lines.append('|'+str(turn)+'|'+'|'.join(cells)+'|')
    lines+=['','|Across six turns (median run peak/total)|Main|Branch|','|---|---:|---:|']
    for label,get,scale in [('Peak process RSS MiB',lambda r:r['peak_rss'],1048576),('Retained Go heap MiB',lambda r:r['retained']['retained_heap'],1048576),('DB MiB',lambda r:r['storage']['checkpointed_db_bytes'],1048576),('Longest observed live delta gap ms',lambda r:r['max_delta_gap_ms'],1),('Longest observed persistence notification gap ms',lambda r:r['max_persist_gap_ms'],1)]:
     vals=[]
     for v in ['main','branch']:
      data=[get(r) for r in selected if r['variant']==v];vals.append(f'{statistics.median(data)/scale:.2f}' if data else 'n/a')
     lines.append('|'+label+'|'+'|'.join(vals)+'|')
    lines+=['','|Worst observed across all six turns and repetitions|Main|Branch|','|---|---:|---:|']
    for label,key,scale in [('Sampled heap MiB','sampled_peak_heap',1048576),('Sampled CPU % (~50 ms)','peak_sample_cpu_percent',1),('Sampled allocation MiB/s (~50 ms)','peak_sample_alloc_mib_s',1),('Rolling 1s CPU %','peak_1s_cpu_percent',1),('Rolling 1s allocation MiB/s','peak_1s_alloc_mib_s',1),('Rolling 1s allocations/s','peak_1s_objects_s',1)]:
     vals=[]
     for v in ['main','branch']:
      data=[t[key] for r in selected if r['variant']==v for t in r['turns'] if t[key] is not None]
      vals.append(f'{max(data)/scale:.2f}' if data else 'n/a')
     lines.append('|'+label+'|'+'|'.join(vals)+'|')
    for label,key,scale in [('Process RSS MiB','peak_rss',1048576),('Live delta gap ms','max_delta_gap_ms',1),('Persistence notification gap ms','max_persist_gap_ms',1)]:
     vals=[f'{max(r[key] for r in selected if r["variant"]==v)/scale:.2f}' for v in ['main','branch']]
     lines.append('|'+label+'|'+'|'.join(vals)+'|')
    lines+=['']
(out/'RESULTS.md').write_text('\n'.join(lines)+'\n')