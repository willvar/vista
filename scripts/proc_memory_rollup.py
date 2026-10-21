#!/usr/bin/env python3
"""Summarize Linux smaps_rollup for a Vista Server and its direct Workers."""

import argparse
import json
from pathlib import Path


def process(pid: int) -> dict | None:
    root = Path('/proc') / str(pid)
    try:
        command = (root / 'cmdline').read_bytes().split(b'\0')
        if len(command) < 2 or command[1] not in (b'serve', b'worker'):
            return None
        status = (root / 'status').read_text()
        parent = next(int(line.split()[1]) for line in status.splitlines() if line.startswith('PPid:'))
        values = {}
        for line in (root / 'smaps_rollup').read_text().splitlines():
            if ':' in line:
                key, raw = line.split(':', 1)
                parts = raw.split()
                if len(parts) == 2 and parts[1] == 'kB':
                    values[key] = round(int(parts[0]) / 1024, 1)
        return {'pid': pid, 'parent': parent, 'role': command[1].decode(), 'memoryMiB': values}
    except (FileNotFoundError, PermissionError, StopIteration, ValueError):
        return None


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('server_pid', type=int)
args = parser.parse_args()
server = process(args.server_pid)
if server is None or server['role'] != 'serve':
    parser.error('server_pid must identify a running Vista Server')
workers = []
for entry in Path('/proc').iterdir():
    if entry.name.isdigit():
        item = process(int(entry.name))
        if item and item['role'] == 'worker' and item['parent'] == args.server_pid:
            workers.append(item)
keys = sorted({key for worker in workers for key in worker['memoryMiB']})
worker_total = {key: round(sum(worker['memoryMiB'].get(key, 0) for worker in workers), 1) for key in keys}
print(json.dumps({'server': server, 'workerCount': len(workers), 'workerTotalMiB': worker_total}, indent=2))
