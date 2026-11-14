#!/usr/bin/env python3
"""Manual renderer benchmarks in isolated archives of two committed revisions."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import statistics
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
SUITES = {"timing": "Timing", "memory": "Memory", "preview": "Preview"}


def command(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def summarize(samples):
    groups = {}
    for sample in samples:
        baseline = next((r for r in sample["rows"] if r.get("label") == "empty"), {})
        for row in sample["rows"]:
            key = (sample["variant"], sample["case"], row.get("scenario", ""),
                   row.get("label", "update"), row["size"])
            group = groups.setdefault(key, {})
            for field, value in row.items():
                if field == "updates" and value:
                    group.setdefault("updateMS", []).append(statistics.median(value))
                elif isinstance(value, (int, float)) and field != "size":
                    if field in ("heap", "rss", "peakRSS", "peakHeap", "beforeGC"):
                        base_field = {"peakRSS": "rss", "peakHeap": "heap", "beforeGC": "heap"}.get(field, field)
                        value = (value - baseline.get(base_field, 0)) / 1048576
                        field += "MiBAboveEmpty"
                    group.setdefault(field, []).append(value)
    return [dict(zip(("variant", "case", "scenario", "phase", "size"), key),
                 metrics={field: {"median": statistics.median(values), "min": min(values),
                                  "max": max(values), "samples": len(values)}
                          for field, values in metrics.items()})
            for key, metrics in sorted(groups.items())]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", default="main")
    parser.add_argument("--candidate", default="HEAD")
    parser.add_argument("--suite", choices=[*SUITES, "all"], default="timing")
    parser.add_argument("--mode", choices=["compact", "full", "both"], default="both")
    parser.add_argument("--runs", type=int, default=5)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if args.runs < 1:
        parser.error("--runs must be positive")
    if platform.system() not in ("Darwin", "Linux"):
        parser.error("process RSS sampling requires macOS or Linux ps")
    repo = Path(command(["git", "-C", str(HERE), "rev-parse", "--show-toplevel"], capture_output=True).stdout.strip())
    output = Path(args.output).resolve()
    if output.is_relative_to(repo):
        parser.error("output must be outside the repository")
    revisions = {name: command(["git", "-C", str(repo), "rev-parse", "--verify", ref + "^{commit}"], capture_output=True).stdout.strip()
                 for name, ref in (("baseline", args.baseline), ("candidate", args.candidate))}
    output.mkdir(parents=True, exist_ok=False)
    sources = sorted(HERE.glob("*.go.txt"))
    chrome = next((p for p in ["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
                              shutil.which("google-chrome"), shutil.which("chromium"), shutil.which("chromium-browser")]
                   if p and Path(p).is_file()), None)
    if not chrome:
        raise SystemExit("Chrome/Chromium is required")
    manifest = {"revisions": revisions, "arguments": vars(args), "platform": platform.platform(),
                "go": command(["go", "version"], capture_output=True).stdout.strip(),
                "chrome": command([chrome, "--version"], capture_output=True).stdout.strip(),
                "harness_sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in [Path(__file__), *sources]}}
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    cases = []
    for suite in (SUITES if args.suite == "all" else [args.suite]):
        for mode in (["compact", "full"] if args.mode == "both" else [args.mode]):
            for scenario in (["tools", "unmatched", "thinking", "unfinished"] if suite == "memory" else [None]):
                cases.append("/".join(filter(None, ["TestFrontendBenchmark" + SUITES[suite], mode, scenario])))
    samples = []
    with tempfile.TemporaryDirectory(prefix="ov-frontend-bench-") as temporary:
        roots = {}
        for variant, sha in revisions.items():
            root = Path(temporary) / variant
            root.mkdir()
            archive = Path(temporary) / (variant + ".tar")
            command(["git", "-C", str(repo), "archive", "--format=tar", "--output=" + str(archive), sha])
            command(["tar", "-xf", str(archive), "-C", str(root)])
            for source in sources:
                (root / "web/templates/components" / ("frontend_bench_" + source.name.removesuffix(".txt"))).write_bytes(source.read_bytes())
            with (output / (variant + "-build.log")).open("w") as log:
                command(["go", "test", "-c", "-o", str(root / "benchmark.test"), "./web/templates/components"], cwd=root, stdout=log, stderr=subprocess.STDOUT)
            roots[variant] = root
        for run in range(1, args.runs + 1):
            # Alternate execution order; never run browsers or builds concurrently.
            for variant in (["baseline", "candidate"] if run % 2 else ["candidate", "baseline"]):
                root = roots[variant]
                for case in cases:
                    name = f"{variant}-{run}-{case.replace('/', '-')}"
                    print(name, flush=True)
                    pattern = "/".join("^" + part + "$" for part in case.split("/"))
                    env = dict(os.environ, OPENVIBELY_SKIP_BROWSER_TESTS="0")
                    with (output / (name + ".log")).open("w") as log:
                        result = subprocess.run([str(root / "benchmark.test"), "-test.v", "-test.run=" + pattern,
                                                 "-test.timeout=4m"], cwd=root / "web/templates/components",
                                                env=env, stdout=log, stderr=subprocess.STDOUT, timeout=250)
                    text = (output / (name + ".log")).read_text()
                    reports = [json.loads(line.split("FRONTEND_RESULT ", 1)[1])
                               for line in text.splitlines() if "FRONTEND_RESULT " in line]
                    if result.returncode or "--- SKIP:" in text or len(reports) != 1 or not reports[0]:
                        raise RuntimeError(f"Failed or missing benchmark: {name}; see saved log")
                    if {200, 1000, 5000} - {row.get("size") for row in reports[0]}:
                        raise RuntimeError(f"Incomplete sizes: {name}")
                    samples.append({"variant": variant, "run": run, "case": case, "rows": reports[0]})
                    (output / "samples.json").write_text(json.dumps(samples, indent=2) + "\n")
    summary = summarize(samples)
    (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    lines = ["# Frontend benchmark results", "", "Pinned revisions and environment: manifest.json. Raw samples: samples.json.",
             "Memory columns are MiB above the empty-page baseline. RSS sums processes and may double-count shared pages.",
             "Preview and ordinary-output cases are separate. These are renderer updates, not page-load or scrolling timings.", "",
             "| Revision | Case | Scenario | Phase | Calls | Metric | Median | Min | Max | Runs |",
             "|---|---|---|---|---:|---|---:|---:|---:|---:|"]
    for row in summary:
        for metric, value in row["metrics"].items():
            lines.append(f"| {row['variant']} | {row['case']} | {row['scenario']} | {row['phase']} | {row['size']} | {metric} | {value['median']:.3f} | {value['min']:.3f} | {value['max']:.3f} | {value['samples']} |")
    (output / "summary.md").write_text("\n".join(lines) + "\n")
    print(f"Results: {output / 'summary.md'}")


if __name__ == "__main__":
    main()
