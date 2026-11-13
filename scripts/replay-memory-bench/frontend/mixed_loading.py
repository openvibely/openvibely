#!/usr/bin/env python3
"""Compare actual task loading with mixed output in isolated Git revisions."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import statistics
import subprocess

HERE = Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument("--baseline", default="main")
    parser.add_argument("--candidate", default="HEAD")
    parser.add_argument("--runs", type=int, default=3)
    parser.add_argument("--sizes", nargs="+", type=int, default=[1, 10, 50])
    parser.add_argument("--huge", action="store_true")
    parser.add_argument("--mode", choices=["compact", "full", "both"], default="both")
    parser.add_argument("--resume", action="store_true")
    args = parser.parse_args()
    repo = Path(subprocess.check_output(
        ["git", "-C", str(HERE), "rev-parse", "--show-toplevel"], text=True).strip())
    output = Path(args.output).resolve()
    if output.is_relative_to(repo):
        parser.error("output must be outside the repository")
    if args.runs < 1 or any(size not in [1, 10, 50] for size in args.sizes):
        parser.error("runs must be positive; sizes must be 1, 10, or 50")
    output.mkdir(parents=True, exist_ok=args.resume)
    harness = (HERE / "mixed_loading_test.go.fixture").read_bytes()
    manifest = {"revisions": {}, "arguments": vars(args),
                "harness_sha256": hashlib.sha256(harness).hexdigest()}
    for name, ref in [("baseline", args.baseline), ("candidate", args.candidate)]:
        sha = subprocess.check_output(
            ["git", "-C", str(repo), "rev-parse", ref + "^{commit}"], text=True).strip()
        manifest["revisions"][name] = sha
        root = output / name
        injected = root / "internal/handler/mixed_loading_benchmark_test.go"
        if args.resume and (root / "benchmark.test").exists():
            saved = json.loads((output / "manifest.json").read_text())
            if saved["revisions"][name] != sha:
                raise RuntimeError("resume revision changed")
            if (saved["arguments"]["huge"] != args.huge or
                    saved["arguments"]["sizes"] != args.sizes):
                raise RuntimeError("resume fixture changed")
            if injected.read_bytes() != harness:
                raise RuntimeError("resume harness changed")
            continue
        root.mkdir()
        archive = output / (name + ".tar")
        subprocess.run(["git", "-C", str(repo), "archive", "--output=" + str(archive), sha], check=True)
        subprocess.run(["tar", "-xf", str(archive), "-C", str(root)], check=True)
        archive.unlink()
        injected.write_bytes(harness)
        with (output / (name + "-build.log")).open("w") as log:
            subprocess.run(["go", "test", "-c", "-o", str(root / "benchmark.test"),
                            "./internal/handler"], cwd=root, stdout=log,
                           stderr=subprocess.STDOUT, check=True)
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2))
    samples_path = output / "samples.json"
    samples = json.loads(samples_path.read_text()) if args.resume and samples_path.exists() else []
    for run in range(1, args.runs + 1):
        for size in args.sizes:
            for mode in (["compact", "full"] if args.mode == "both" else [args.mode]):
                for variant in (["baseline", "candidate"] if run % 2 else ["candidate", "baseline"]):
                    if any((s["variant"], s["sizeMiB"], s["mode"], s["run"]) ==
                           (variant, size, mode, run) for s in samples):
                        continue
                    name = f"{variant}-{size}-{mode}-{run}"
                    print(name, flush=True)
                    root = output / variant
                    env = dict(os.environ, OV_MIXED_MB=str(size),
                               OV_MIXED_HUGE="1" if args.huge else "0")
                    log_path = output / (name + ".log")
                    with log_path.open("w") as log:
                        process = None
                        try:
                            process = subprocess.Popen([
                                str(root / "benchmark.test"), "-test.v", "-test.timeout=4m",
                                "-test.run=^TestMixedRunningLoadingInChrome$/^" + mode + "$"],
                                cwd=root / "internal/handler", env=env, stdout=log,
                                stderr=subprocess.STDOUT, start_new_session=True)
                            status = process.wait(timeout=250)
                        except subprocess.TimeoutExpired:
                            status = "timeout"
                        finally:
                            if process is not None:
                                try:
                                    os.killpg(process.pid, signal.SIGKILL)
                                except ProcessLookupError:
                                    pass
                                process.wait()
                    rows = [json.loads(line[line.index("{"):])
                            for line in log_path.read_text().splitlines()
                            if '{"fallback"' in line or '{"streamBytes"' in line]
                    samples.append(dict(variant=variant, sizeMiB=size, mode=mode,
                                        run=run, status=status, rows=rows))
                    samples_path.write_text(json.dumps(samples, indent=2))
    summary = []
    for variant in ["baseline", "candidate"]:
        for size in args.sizes:
            for mode in (["compact", "full"] if args.mode == "both" else [args.mode]):
                subset = [s for s in samples if (s["variant"], s["sizeMiB"], s["mode"]) ==
                          (variant, size, mode)]
                rows = [row for sample in subset if sample["status"] == 0 for row in sample["rows"]]
                summary.append(dict(variant=variant, sizeMiB=size, mode=mode, passed=len(rows),
                                    attempted=len(subset), medians={
                                        key: statistics.median(row[key] for row in rows)
                                        for key in rows[0]} if rows else {}))
    (output / "summary.json").write_text(json.dumps(summary, indent=2))
    print(output / "summary.json")
    if any(sample["status"] != 0 or len(sample["rows"]) != 1 for sample in samples):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
