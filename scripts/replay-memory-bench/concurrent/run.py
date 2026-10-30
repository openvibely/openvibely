#!/usr/bin/env python3
"""One app process/shared database; external model; ps RSS samples outside app."""
import argparse, hashlib, json, os, pathlib, platform, subprocess, time
p=argparse.ArgumentParser()
p.add_argument('--output', required=True)
p.add_argument('--main-binary', required=True)
p.add_argument('--branch-binary', required=True)
p.add_argument('--model-binary', required=True)
p.add_argument('--runs', type=int, default=3)
p.add_argument('--first-run',type=int,default=1)
p.add_argument('--profiles', default='small,medium,large')
p.add_argument('--modes', default='text-only,tool-use')
p.add_argument('--workers', default='1,10')
p.add_argument('--profile', action='store_true')
p.add_argument('--provider', choices=['openai','anthropic','openai-compatible'], default='openai')
p.add_argument('--variants', default='main,branch', help='Comma-separated main,branch; branch alone for attribution-only runs')
p.add_argument('--baseline-revision', default='unspecified')
p.add_argument('--candidate-revision', default='unspecified')
a=p.parse_args()
selected=[binary for variant,binary in [('main',a.main_binary),('branch',a.branch_binary)] if variant in a.variants.split(',')]
if not selected:raise ValueError('no selected variants')
for binary in dict.fromkeys(selected):
 listed=subprocess.run([binary,'-test.list=^TestConcurrentFlow$'],text=True,capture_output=True,check=True)
 if 'TestConcurrentFlow' not in listed.stdout.splitlines():raise SystemExit('missing TestConcurrentFlow harness in '+binary)
root=pathlib.Path(a.output).resolve();root.mkdir(parents=True,exist_ok=False)
metadata=dict(vars(a), host=platform.platform(), machine=platform.machine(), started_unix=time.time(),
 binaries={str(path):hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest() for path in selected+[a.model_binary]},
 harness_sha256=hashlib.sha256(pathlib.Path(__file__).with_name('flow_test.go.txt').read_bytes()).hexdigest())
(root/'experiment.json').write_text(json.dumps(metadata,indent=2))
for rep in range(a.first_run,a.first_run+a.runs):
 for mode in a.modes.split(','):
  for profile in a.profiles.split(','):
   for workers in a.workers.split(','):
    variants=[('main',a.main_binary),('branch',a.branch_binary)]
    if rep%2==0: variants.reverse()
    variants=[v for v in variants if v[0] in a.variants.split(',')]
    if not variants: raise ValueError('no selected variants')
    for variant,binary in variants:
     d=root/f'{mode}-{profile}-{workers}-{variant}-{rep}';d.mkdir()
     with (d/'peer.log').open('w') as pl,(d/'app.log').open('w') as al:
      peer=subprocess.Popen([a.model_binary,'--directory',str(d)],stdout=pl,stderr=subprocess.STDOUT)
      app=None
      try:
       deadline=time.monotonic()+10
       while not (d/'model-url').exists():
        if peer.poll() is not None or time.monotonic()>deadline: raise RuntimeError('peer startup failed')
        time.sleep(.05)
       env=dict(os.environ,OV_FLOW_DIR=str(d),OV_FLOW_MODE=mode,OV_FLOW_PROFILE=profile,OV_FLOW_WORKERS=workers,OV_FLOW_PROVIDER=a.provider)
       cmd=[binary,'-test.run=^TestConcurrentFlow$','-test.v','-test.timeout=5m']
       if a.profile: cmd += ['-test.cpuprofile='+str(d/'cpu.pprof'),'-test.memprofile='+str(d/'mem.pprof')]
       print('START',d.name,flush=True)
       app=subprocess.Popen(cmd,env=env,stdout=al,stderr=subprocess.STDOUT)
       rss=[];start=time.monotonic()
       while app.poll() is None:
        if time.monotonic()-start>310: raise RuntimeError('app timeout')
        r=subprocess.run(['ps','-o','rss=','-p',str(app.pid)],capture_output=True,text=True)
        if r.stdout.strip(): rss.append([time.monotonic()-start,int(r.stdout.strip())*1024])
        time.sleep(.1)
       (d/'rss.json').write_text(json.dumps(rss))
       if app.returncode: raise RuntimeError((d/'app.log').read_text()[-10000:])
       missing=[name for name in ('turns.json','heap.json','storage.json','model.json','manifest.json','delivery.json',*(f'samples-{i}.json' for i in range(6))) if not (d/name).is_file()]
       if missing:raise RuntimeError(f'{d.name}: harness produced no complete result: {missing}')
       (d/'accepted.json').write_text(json.dumps(dict(provider=a.provider,variant=variant,repetition=rep,profiled=a.profile)))
       print('PASS',d.name,flush=True)
      finally:
       if app and app.poll() is None: app.kill();app.wait()
       peer.terminate();peer.wait(timeout=5)
