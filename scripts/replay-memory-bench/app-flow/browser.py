#!/usr/bin/env python3
"""Cold Chromium profile + cached navigation on each generated conversation."""
import argparse, json, os, pathlib, subprocess, time
p=argparse.ArgumentParser()
p.add_argument('--results',required=True)
p.add_argument('--branch-binary',required=True)
p.add_argument('--main-binary',required=True)
p.add_argument('--branch-browser',required=True)
p.add_argument('--main-browser',required=True)
p.add_argument('--cwd',default=os.getcwd())
p.add_argument('--match',default='*')
a=p.parse_args()
checks=[(getattr(a,v+'_'+kind),test) for v in ('main','branch') for kind,test in (('binary','TestAppFlow'),('browser','TestAppFlowBrowser'))]
for binary,test in dict.fromkeys(checks):
    listed=subprocess.run([binary,'-test.list=^'+test+'$'],cwd=a.cwd,text=True,capture_output=True,check=True)
    if test not in listed.stdout.splitlines():raise SystemExit('missing '+test+' harness in '+binary)
directories=sorted(d for d in pathlib.Path(a.results).glob(a.match) if (d/'manifest.json').is_file())
if not directories:raise SystemExit('no generated conversations matched '+a.results)
for d in directories:
    variant='branch' if '-branch-' in d.name else 'main'
    binary=getattr(a,variant+'_binary');browser=getattr(a,variant+'_browser')
    for name in ('stop-server','app-url','browser.json'): (d/name).unlink(missing_ok=True)
    env=os.environ.copy();env.update(OV_FLOW_DIR=str(d.resolve()),OV_FLOW_SERVE='1')
    with (d/'server.log').open('w') as log, (d/'browser.log').open('w') as blog:
        proc=subprocess.Popen([binary,'-test.run=^TestAppFlow$','-test.timeout=5m'],cwd=a.cwd,env=env,stdout=log,stderr=subprocess.STDOUT)
        try:
            for _ in range(200):
                if (d/'app-url').exists():break
                if proc.poll() is not None: raise RuntimeError((d/'server.log').read_text()[-4000:])
                time.sleep(.05)
            print('BROWSER',d.name,flush=True)
            r=subprocess.run([browser,'-test.run=^TestAppFlowBrowser$','-test.v','-test.timeout=3m'],cwd=a.cwd,env=env,stdout=blog,stderr=subprocess.STDOUT,timeout=185)
            if r.returncode: print((d/'browser.log').read_text()[-6000:]);raise SystemExit(r.returncode)
            if not (d/'browser.json').is_file():raise RuntimeError(d.name+': browser harness produced no result')
            result=json.loads((d/'browser.json').read_text())
            if not result.get('cached_preview_verified'):raise RuntimeError(d.name+': cache hit was not verified')
            print('PASS',d.name,flush=True)
        finally:
            (d/'stop-server').touch()
            try: proc.wait(timeout=5)
            except subprocess.TimeoutExpired:proc.terminate();proc.wait(timeout=5)
