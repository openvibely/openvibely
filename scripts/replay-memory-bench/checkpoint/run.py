#!/usr/bin/env python3
"""Synthetic checkpoint follow-ups against a separate local mock model process."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[3]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--providers', default='openai,anthropic,openai-compatible')
    parser.add_argument('--modes', default='reload,reuse,update,evict')
    parser.add_argument('--turns', type=int, default=6)
    parser.add_argument('--runs', type=int, default=1)
    args = parser.parse_args()
    providers, modes = args.providers.split(','), args.modes.split(',')
    if not set(providers) <= {'openai', 'anthropic', 'openai-compatible'}:
        parser.error('unsupported provider')
    if not set(modes) <= {'reload', 'reuse', 'update', 'evict'}:
        parser.error('unsupported mode')
    if args.turns < 4 or args.runs < 1:
        parser.error('at least four turns and one run required')
    output = args.output.resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error('keep generated results outside the repository')
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, OPENVIBELY_PPROF_PORT='0')
    manifest = {'revision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                'dirty': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT)),
                'go': subprocess.check_output(['go', 'version'], text=True).strip(),
                'host': platform.platform(), 'turns': args.turns, 'runs': args.runs,
                'providers': providers, 'modes': modes, 'fixture_base64_bytes': 11 << 20,
                'gc': 'forced after each turn; retained heap diagnostic, not latency benchmark'}
    files = [Path(__file__).resolve(), ROOT / 'internal/service/checkpoint_followup_benchmark_test.go']
    files += sorted((ROOT / 'scripts/replay-memory-bench/concurrent/model-server').glob('*.go'))
    manifest['harness_sha256'] = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
    (output / 'manifest.json').write_text(json.dumps(manifest, indent=2))
    with tempfile.TemporaryDirectory(prefix='checkpoint-bench-') as scratch:
        scratch = Path(scratch)
        peer_binary, app_binary = scratch / 'peer', scratch / 'app.test'
        subprocess.run(['go', 'build', '-o', str(peer_binary), './scripts/replay-memory-bench/concurrent/model-server'], cwd=ROOT, env=env, check=True)
        subprocess.run(['go', 'test', '-c', '-o', str(app_binary), './internal/service'], cwd=ROOT, env=env, check=True)
        with (output / 'correctness.log').open('w') as log:
            subprocess.run(['go', 'test', './internal/llm/preparedhistory', '-run', '^TestReusableState', '-count=1'], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
            subprocess.run([str(app_binary), '-test.run=^Test(ActiveCheckpoint|PortableCheckpointAcrossSavedModelSelections|CheckpointResumeLoadsOnlyUncoveredReplay|CheckpointBenchmarkUsesProductionReplayRows)', '-test.timeout=10m'], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
        for repetition in range(args.runs):
            # Alternate mode order to reduce systematic warm-machine bias.
            for provider in providers:
                for mode in modes if repetition % 2 == 0 else list(reversed(modes)):
                    directory = output / f'{provider}-{mode}-{repetition+1}'
                    directory.mkdir()
                    with (directory / 'peer.log').open('w') as peer_log:
                        peer = subprocess.Popen([str(peer_binary), '-directory', str(directory)], stdout=peer_log, stderr=subprocess.STDOUT)
                        try:
                            deadline = time.monotonic() + 15
                            while not (directory / 'model-url').exists():
                                if peer.poll() is not None or time.monotonic() > deadline:
                                    raise RuntimeError('mock model failed to start')
                                time.sleep(.05)
                            case_env = dict(env, OV_CHECKPOINT_MODEL_URL=(directory / 'model-url').read_text(),
                                            OV_CHECKPOINT_PROVIDER=provider, OV_CHECKPOINT_MODE=mode,
                                            OV_CHECKPOINT_TURNS=str(args.turns), OV_CHECKPOINT_RESULT=str(directory / 'result.json'))
                            with (directory / 'app.log').open('w') as log:
                                app = subprocess.Popen([str(app_binary), '-test.run=^TestCheckpointFollowupMock$', '-test.timeout=10m', '-test.v'], cwd=ROOT, env=case_env, stdout=log, stderr=subprocess.STDOUT)
                                samples = []
                                start = time.monotonic()
                                try:
                                    while app.poll() is None:
                                        if time.monotonic() - start > 620:
                                            raise TimeoutError('benchmark process timed out')
                                        sample = subprocess.run(['ps', '-o', 'rss=', '-p', str(app.pid)], capture_output=True, text=True)
                                        if sample.returncode == 0 and sample.stdout.strip():
                                            samples.append([time.monotonic()-start, int(sample.stdout.strip()) * 1024])
                                        time.sleep(.1)
                                finally:
                                    if app.poll() is None:
                                        app.kill()
                                    app.wait()
                                (directory / 'rss.json').write_text(json.dumps(samples))
                                if app.returncode:
                                    raise RuntimeError(f'failed: {directory / "app.log"}')
                            print(f'PASS {provider}/{mode}/{repetition+1}', flush=True)
                        finally:
                            peer.terminate()
                            try:
                                peer.wait(timeout=5)
                            except subprocess.TimeoutExpired:
                                peer.kill()
                                peer.wait()
    print(f'Results: {output}')


if __name__ == '__main__':
    main()
