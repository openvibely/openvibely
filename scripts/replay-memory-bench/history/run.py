#!/usr/bin/env python3
"""File-backed synthetic history loading; no providers or browser required."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[3]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--sizes', default='1000,10000,45000', help='display blocks per execution')
    parser.add_argument('--modes', default='completed,running,plan', help='comma-separated completed,running,plan,raw-running')
    parser.add_argument('--runs', type=int, default=3)
    parser.add_argument('--smoke', action='store_true', help='small correctness validation only')
    args = parser.parse_args()
    try:
        sizes = [600] if args.smoke else [int(s) for s in args.sizes.split(',')]
    except ValueError:
        parser.error('sizes must be integers')
    if not sizes or min(sizes) < 128 or args.runs < 1:
        parser.error('sizes >=128 and runs >=1 required')
    modes = args.modes.split(',')
    if not modes or any(m not in ('completed','running','plan','raw-running') for m in modes):
        parser.error('invalid mode')
    output = args.output.resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error('results must be outside repository')
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, TEST_DATABASE_DRIVER='sqlite', OPENVIBELY_PPROF_PORT='0')
    manifest = {'revision': subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),
                'dirty': bool(subprocess.check_output(['git','status','--porcelain'],cwd=ROOT)),
                'host':platform.platform(), 'go':subprocess.check_output(['go','version'],text=True).strip(),
                'modes':modes, 'smoke':args.smoke, 'sizes':sizes, 'runs':1 if args.smoke else args.runs,
                'database':'temporary file-backed SQLite, production reader/writer topology',
                'limitations':'server HTTP only; forced GC before samples; filesystem cache not flushed; driver rows, not SQLite internal visits'}
    files=[Path(__file__).resolve(),ROOT/'internal/handler/history_loading_benchmark_test.go']
    manifest['harness_sha256']={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
    (output/'manifest.json').write_text(json.dumps(manifest,indent=2))
    results=[]
    with tempfile.TemporaryDirectory(prefix='history-harness-') as scratch:
        binary=Path(scratch)/'handler.test'
        subprocess.run(['go','test','-c','-o',str(binary),'./internal/handler'],cwd=ROOT,env=env,check=True)
        for run in range(manifest['runs']):
            modes=list(manifest['modes'])
            if run%2: modes.reverse()
            for size in sizes:
                for mode in modes:
                    name=f'{mode}-{size}-{run+1}'
                    target=output/(name+'.json')
                    case_env=dict(env,OV_HISTORY_RESULT=str(target),OV_HISTORY_BLOCKS=str(size),OV_HISTORY_MODE=mode)
                    with (output/(name+'.log')).open('w') as log:
                        subprocess.run([str(binary),'-test.run=^TestHistoryLoadingHarness$','-test.timeout=15m','-test.v'],cwd=ROOT,env=case_env,stdout=log,stderr=subprocess.STDOUT,check=True,timeout=920)
                    results.append({'case':name,**json.loads(target.read_text())})
                    print(f'PASS {name}',flush=True)
    (output/'results.json').write_text(json.dumps(results,indent=2))
    lines=['# History-loading results','', 'Smoke validation only.' if args.smoke else 'Individual operations; compare identical fixture sizes and revisions.', '', '| Case | Operation | ms | Allocated MiB | Response bytes | Driver rows | DB requests |','|---|---|---:|---:|---:|---:|---:|']
    for case in results:
        for sample in case['samples']:
            lines.append(f"| {case['case']} | {sample['operation']} | {sample['duration_ns']/1e6:.3f} | {sample['allocated_bytes']/2**20:.3f} | {sample['response_bytes']} | {sample['io']['ReturnedRows']} | {sample['io']['Requests']} |")
    (output/'report.md').write_text('\n'.join(lines)+'\n')

if __name__ == '__main__':
    main()
