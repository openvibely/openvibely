#!/usr/bin/env python3
"""Archive raw JSON and render per-turn medians; never combine unlike operations."""
import argparse, json, pathlib, statistics
p=argparse.ArgumentParser();p.add_argument('--mode',choices=['text-only','tool-use'],required=True);p.add_argument('--results',required=True);p.add_argument('--output',required=True);p.add_argument('--metadata',required=True);a=p.parse_args()
root=pathlib.Path(a.results);out=pathlib.Path(a.output);out.mkdir(parents=True,exist_ok=True)
runs=[]
for path in sorted(root.glob('*/*/turns.json')):
    name=path.parent.name
    profile,variant,rep=name.rsplit('-',2)
    # mixed-paced contains a hyphen; rsplit preserves the profile.
    row={'mode':a.mode,'profile':profile,'variant':variant,'repetition':int(rep)}
    for key in ('turns','heap','storage','model','browser'):
        f=path.parent/(key+'.json')
        if not f.exists():raise SystemExit('missing '+str(f))
        row[key]=json.loads(f.read_text())
    if any(t['mode']!=a.mode for t in row['turns']):raise SystemExit('mixed benchmark modes '+name)
    if len(row['turns'])!=6:raise SystemExit('incomplete turns '+name)
    req=row['model']['requests']
    if profile!='compaction' and any(not r['prior_turn_seen'] for r in req):raise SystemExit('lost previous context '+name)
    if not row['browser']['cached_preview_verified']:raise SystemExit('not a cache hit '+name)
    for turn in range(6):
        normal=[r for r in req if r['turn']==turn and not r['compact'] and not r['summary']]
        if len(normal)!=(2 if a.mode=='tool-use' else 1):raise SystemExit('unexpected model exchanges '+name)
        # Native compaction may consume the tool result before the next normal
        # response; receipt by the provider's compaction request counts too.
        if a.mode=='tool-use' and not any(r['turn']==turn and r['tool_result_seen'] for r in req):raise SystemExit('missing tool exchange '+name)
        if a.mode=='text-only' and any(r['tool_result_seen'] or r['with_tools'] for r in normal):raise SystemExit('tools in text baseline '+name)
        for r in normal:
            limit=r.get('requested_output_tokens')
            if limit and r['estimated_output_tokens']>limit:raise SystemExit('invalid output budget '+name)
    if profile=='compaction':
        compacted=[r['turn'] for r in req if r['compact']]
        if not compacted or not any(r['turn']>min(compacted) and r['checkpoint_seen'] for r in req):raise SystemExit('missing checkpoint continuation '+name)
    runs.append(row)
metadata=json.loads(pathlib.Path(a.metadata).read_text())
(out/'results.json').write_text(json.dumps({'metadata':metadata,'runs':runs},indent=2)+'\n')
profiles=['small','medium','large','mixed-paced','compaction']
profiles=[x for x in profiles if any(r['profile']==x for r in runs)]

def values(profile,variant,key,turn=None,section='turns'):
    group=[r for r in runs if r['profile']==profile and r['variant']==variant]
    if len(group)!=3:raise SystemExit(f'{profile}/{variant}: expected 3 repetitions, found {len(group)}')
    return [r[section][turn][key] if turn is not None else r[section][key] for r in group]
def med(*args,**kwargs):return statistics.median(values(*args,**kwargs))
def pair(profile,key,turn=None,section='turns',scale=1,dec=2):
    return ' → '.join(f'{med(profile,v,key,turn,section)/scale:,.{dec}f}' for v in ('main','branch'))
lines=['# '+a.mode+' task-flow measurements','', 'All cells are **local main → branch**; medians of three independent fresh-process runs. Every turn row covers one prompt through its completed streamed response; the next prompt waits. Compaction-labelled rows include the entire turn, not compaction alone. No aggregate winner is calculated. See [methodology](../README.md) for exact boundaries and limitations.','',f'Main: `{metadata["main"]}`. Branch: `{metadata["branch"]}` (includes the local-main sync).','']
for profile in profiles:
    lines+=['## '+profile,'','| User operation | CPU ms | Elapsed s | Allocated MiB | Allocation count | GC collections | GC pause ms |','|---|---:|---:|---:|---:|---:|---:|']
    for turn in range(6):
        label='New task' if turn==0 else f'Follow-up {turn}'
        if profile=='compaction':
            if any(q['turn']==turn and q['compact'] for r in runs if r['profile']==profile for q in r['model']['requests']):label+=' + native compaction'
            if any(q['turn']==turn and q['summary'] for r in runs if r['profile']==profile for q in r['model']['requests']):label+=' + summary fallback'
        if profile=='mixed-paced':label+=f' ({[2,8,16][turn%3]} KiB text)'
        lines+=['| '+label+' | '+' | '.join([pair(profile,'cpu_ms',turn),pair(profile,'elapsed_ms',turn,scale=1000),pair(profile,'alloc_bytes',turn,scale=1048576),pair(profile,'alloc_count',turn,dec=0),pair(profile,'gc_count',turn,dec=0),pair(profile,'gc_pause_ns',turn,scale=1000000)])+' |']
    lines+=['','| User operation | Sampled peak Go heap MiB | End heap without forced GC MiB | Process RSS high-water MiB¹ | DB logical MiB | WAL file MiB |','|---|---:|---:|---:|---:|---:|']
    for turn in range(6):
        label='New task' if turn==0 else f'Follow-up {turn}'
        wal=[]
        for v in ('main','branch'):
            wal.append(statistics.median([r['turns'][turn]['files'].get('db-wal',0)/1048576 for r in runs if r['profile']==profile and r['variant']==v]))
        lines+=['| '+label+' | '+' | '.join([pair(profile,'sampled_peak_heap',turn,scale=1048576),pair(profile,'end_heap',turn,scale=1048576),pair(profile,'process_peak_rss',turn,scale=1048576),pair(profile,'db_pages_bytes',turn,scale=1048576),' → '.join(f'{n:.2f}' for n in wal)])+' |']
    lines+=['','¹ RSS includes migrations/startup and preceding turns; it is not a separate operation peak. GC pauses are not total GC CPU.','']
lines+=['## Memory and storage after six turns','','The application remains alive during the retained-heap measurement. Database size includes schema and indexes; growth subtracts the recorded initial schema/configuration footprint.','','| Scenario | Idle baseline heap MiB | Retained heap after GC MiB | Initial DB MiB | Final checkpointed DB MiB | DB growth MiB |','|---|---:|---:|---:|---:|---:|']
for profile in profiles:
    growth=[]
    for v in ('main','branch'):
        growth.append(statistics.median([(r['storage']['checkpointed_db_bytes']-r['heap']['initial_db_pages_bytes'])/1048576 for r in runs if r['profile']==profile and r['variant']==v]))
    lines+=['| '+profile+' | '+' | '.join([pair(profile,'baseline_heap',section='heap',scale=1048576),pair(profile,'retained_heap',section='heap',scale=1048576),pair(profile,'initial_db_pages_bytes',section='heap',scale=1048576),pair(profile,'checkpointed_db_bytes',section='storage',scale=1048576),' → '.join(f'{n:.2f}' for n in growth)])+' |']
lines+=['','## Actual task-page cold and cache opens','','Fresh server/browser for each cold open; then navigation away/back in the same browser, with a verified cached preview. Cold does not mean the OS disk cache was flushed. Renderer CPU is Chromium TaskDuration, not total multi-process browser CPU.','','| Scenario | Cold ready ms | Cached ready ms | Cold renderer CPU ms | Cached renderer CPU ms |','|---|---:|---:|---:|---:|']
for profile in profiles:
    lines+=['| '+profile+' | '+' | '.join(pair(profile,k,section='browser') for k in ('cold_ready_ms','cached_ready_ms','cold_renderer_cpu_ms','cached_renderer_cpu_ms'))+' |']
lines+=['','| Scenario | Cold sampled JS peak MiB | Cached reopen sampled JS peak MiB | Retained JS heap while away MiB | Initial loaded source MiB | Cached source MiB² |','|---|---:|---:|---:|---:|---:|']
for profile in profiles:
    lines+=['| '+profile+' | '+' | '.join(pair(profile,k,section='browser',scale=1048576) for k in ('cold_sampled_peak_heap_bytes','cached_sampled_peak_heap_bytes','cached_retained_heap_bytes','initial_source_bytes','cached_unique_source_bytes'))+' |']
lines+=['','² Unique source text only; not total cache/browser memory. JS heap excludes native/browser/GPU allocations and worker heaps.','', 'Loading benchmarks alone do not establish that block grouping, preserved tool state or touch navigation are correct. Those behaviors require separate regression tests.','', 'Raw repetitions, GC data, file sizes and model request/checkpoint traces: [results.json](results.json).','']
(out/'RESULTS.md').write_text('\n'.join(lines))
