#!/usr/bin/env python3
"""Run fresh app processes against a separate deterministic model peer.
Build binaries first (README); the output directory must not exist.
"""
import argparse, os, pathlib, subprocess, time
p=argparse.ArgumentParser()
p.add_argument('--output',required=True)
p.add_argument('--mode',choices=['text-only','tool-use'],required=True)
p.add_argument('--branch-binary',required=True)
p.add_argument('--main-binary')
p.add_argument('--model-binary',required=True)
p.add_argument('--profiles',default='small,medium,large,compaction')
p.add_argument('--runs',type=int,default=3)
p.add_argument('--first-run',type=int,default=1)
p.add_argument('--cwd',default=os.getcwd())
a=p.parse_args()
# A test binary exits successfully even when the injected harness is absent.
# Reject it before starting a peer or reporting a misleading PASS.
for binary in dict.fromkeys(b for b in (a.main_binary,a.branch_binary) if b):
    listed=subprocess.run([binary,'-test.list=^TestAppFlow$'],cwd=a.cwd,text=True,capture_output=True,check=True)
    if 'TestAppFlow' not in listed.stdout.splitlines():raise SystemExit('missing TestAppFlow harness in '+binary)
root=pathlib.Path(a.output).resolve();root.mkdir(parents=True,exist_ok=False)
for run in range(a.first_run,a.first_run+a.runs):
    variants=[('main',a.main_binary),('branch',a.branch_binary)]
    if run%2==0: variants.reverse()
    for profile in a.profiles.split(','):
        for variant,binary in variants:
            if not binary: continue
            d=root/f'{profile}-{variant}-{run}';d.mkdir()
            with (d/'peer.log').open('w') as peerlog, (d/'app.log').open('w') as log:
                peer=subprocess.Popen([a.model_binary,'--directory',str(d)],stdout=peerlog,stderr=subprocess.STDOUT)
                try:
                    for _ in range(100):
                        if (d/'model-url').exists():break
                        if peer.poll() is not None:raise RuntimeError('model peer exited')
                        time.sleep(.05)
                    env=os.environ.copy();env.update(OV_FLOW_DIR=str(d),OV_FLOW_PROFILE=profile,OV_FLOW_MODE=a.mode)
                    print(f'START {d.name}',flush=True)
                    result=subprocess.run([binary,'-test.run=^TestAppFlow$','-test.v','-test.timeout=5m'],env=env,cwd=a.cwd,stdout=log,stderr=subprocess.STDOUT,timeout=310)
                    if result.returncode:
                        print((d/'app.log').read_text()[-12000:]);raise SystemExit(result.returncode)
                    missing=[name for name in ('turns.json','heap.json','storage.json','model.json','manifest.json','app.db') if not (d/name).is_file()]
                    if missing:raise RuntimeError(f'{d.name}: harness produced no complete result: {missing}')
                    print(f'PASS {d.name}',flush=True)
                finally:
                    peer.terminate();peer.wait(timeout=5)
