#!/usr/bin/env python3
"""Record bounded macOS process snapshots without command arguments or environment.

One snapshot:
  python3 scripts/diagnose-process-pressure.py
Capture while reproducing (survives closing Terminal):
  nohup python3 scripts/diagnose-process-pressure.py --watch > /tmp/ov-process-monitor.log 2>&1 &

Snapshots rotate in /tmp/ov-process-pressure-<uid> (override with --output).
Watch mode stops after one hour by default. This does not stop any app processes.
"""

import argparse
from collections import Counter
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import resource
import subprocess
import time


def snapshot():
    result = {"time": datetime.now(timezone.utc).isoformat(),
              "uid": os.getuid(), "process_limit": resource.getrlimit(resource.RLIMIT_NPROC)}
    try:
        output = subprocess.run(
            ["/bin/ps", "-axo", "pid=,ppid=,uid=,stat=,rss=,comm="],
            capture_output=True, text=True, timeout=5, check=True,
        ).stdout
    except (OSError, subprocess.SubprocessError) as exc:
        # Still persist a timestamp and failure when the system cannot fork ps.
        result["error"] = str(exc)
        return result
    processes = []
    for line in output.splitlines():
        fields = line.split(None, 5)
        if len(fields) != 6:
            continue
        pid, parent, uid, state, rss, executable = fields
        processes.append({"pid": int(pid), "ppid": int(parent), "uid": int(uid),
                          "state": state, "rss_kib": int(rss), "executable": executable})
    owned = [p for p in processes if p["uid"] == os.getuid()]
    result.update(
        process_count=len(processes), user_process_count=len(owned),
        zombie_count=sum(p["state"].startswith("Z") for p in processes),
        top_executables=Counter(p["executable"] for p in owned).most_common(30),
        top_parents=Counter(p["ppid"] for p in owned).most_common(30),
        processes=processes,
    )
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--watch", action="store_true")
    parser.add_argument("--interval", type=float, default=5, help="seconds between samples (minimum 1)")
    parser.add_argument("--duration", type=float, default=3600, help="watch duration in seconds")
    parser.add_argument("--output", type=Path, default=Path(f"/tmp/ov-process-pressure-{os.getuid()}"))
    args = parser.parse_args()
    if not 1 <= args.interval <= 3600 or not 0 < args.duration <= 86400:
        parser.error("interval must be 1–3600 seconds and duration must be >0 and <=86400 seconds")
    os.umask(0o077)
    args.output.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + args.duration
    index = 0
    while True:
        data = snapshot()
        # Retain 120 snapshots (10 minutes at the default interval).
        destination = args.output / f"snapshot-{index % 120:03d}.json"
        temporary = destination.with_suffix(".tmp")
        temporary.write_text(json.dumps(data, indent=2) + "\n")
        temporary.replace(destination)
        print(f"{data['time']} processes={data.get('process_count', '?')} "
              f"user={data.get('user_process_count', '?')} "
              f"error={data.get('error', 'none')} file={destination}", flush=True)
        index += 1
        remaining = deadline - time.monotonic()
        if not args.watch or remaining <= 0:
            break
        time.sleep(min(args.interval, remaining))
        if time.monotonic() >= deadline:
            break


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
