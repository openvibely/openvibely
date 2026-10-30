#!/usr/bin/env python3
"""Render one repetition's actual time series, never an average of traces."""
import argparse,bisect,json,pathlib
p=argparse.ArgumentParser();p.add_argument('directory');p.add_argument('output');a=p.parse_args();data={}
for d in pathlib.Path(a.directory).iterdir():
 if not (d/'storage.json').exists():continue
 mode,profile,workers,variant,rep=d.name.rsplit('-',4)
 for turn in range(6):
  samples=json.loads((d/f'samples-{turn}.json').read_text());end=samples[-1]['MS'];samples=[s for s in samples[:-1] if s['MS']<end]+samples[-1:];times=[s['MS'] for s in samples];trace=[]
  for s in samples:
   j=bisect.bisect_right(times,s['MS']-1000)-1
   cpu=alloc=objects=None
   if j>=0:
    prev=samples[j];dt=(s['MS']-prev['MS'])/1000
    cpu=(s['CPU']-prev['CPU'])/(dt*10);alloc=(s['Alloc']-prev['Alloc'])/(dt*1048576);objects=(s['Objects']-prev['Objects'])/dt
   trace.append([s['MS']/1000,s['Heap']/1048576,cpu,alloc,objects])
  key=f'{mode} / {profile} / {workers} tasks / turn {turn}'
  data.setdefault(key,{})[variant]=trace
html='''<!doctype html><html><meta charset="utf-8"><title>During streaming: main vs current</title><style>body{font:16px system-ui;margin:32px;max-width:1100px;background:#f8fafc;color:#172033}select{padding:8px;width:100%}.grid{display:grid;grid-template-columns:1fr 1fr;gap:20px}svg{background:white;width:100%;border:1px solid #ddd}h2{font-size:17px}p{line-height:1.5}</style><h1>During streaming: main vs current</h1><p>One real repetition, not averaged curves. Each task completes one response before its next prompt. Orange = main; blue = current. Turn 0 is the first prompt. Includes request preparation and completion. Heap sampled every 50 ms; CPU/allocation rates use rolling ≥1-second windows. Short streams may not have a rate window. These curves are not hard ceilings or total browser memory.</p><select id="scenario"></select><div class="grid" id="charts"></div><script>const data=DATA;const select=document.getElementById('scenario');Object.keys(data).sort().forEach(k=>select.add(new Option(k,k)));function draw(){const series=data[select.value];document.getElementById('charts').innerHTML='';for(const [index,label] of [[1,'Live Go heap (MiB)'],[2,'CPU (% of one core)'],[3,'Allocation rate (MiB/s)'],[4,'Allocation rate (objects/s)']]){const values=Object.values(series).flat();const xmax=Math.max(...values.map(x=>x[0]),1);const ymax=Math.max(...values.filter(x=>x[index]!=null).map(x=>x[index]),1)*1.1;let svg='<svg viewBox="0 0 500 250"><path d="M55 15V215H490" fill="none" stroke="#aaa"/>';for(let i=0;i<=4;i++){let v=ymax*i/4,y=215-i*50;svg+=`<text x="50" y="${y}" text-anchor="end" font-size="11">${v.toFixed(v>100?0:1)}</text>`}for(let i=0;i<=4;i++)svg+=`<text x="${55+i*108}" y="235" text-anchor="middle" font-size="11">${(xmax*i/4).toFixed(1)}s</text>`;for(const [name,trace] of Object.entries(series)){const pts=trace.filter(p=>p[index]!=null).map(p=>`${55+p[0]/xmax*435},${215-p[index]/ymax*200}`).join(' ');svg+=`<polyline points="${pts}" fill="none" stroke="${name==='main'?'#d97706':'#2563eb'}" stroke-width="2"/>`}svg+='</svg>';const el=document.createElement('section');el.innerHTML='<h2>'+label+'</h2>'+svg;document.getElementById('charts').append(el)}}select.onchange=draw;select.value=Object.keys(data).find(k=>k.includes('text-only / large / 10 tasks / turn 5'))||select.value;draw();</script></html>'''
pathlib.Path(a.output).write_text(html.replace('DATA',json.dumps(data,separators=(',',':'))))
