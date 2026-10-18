#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 -I - "$project_root" "$@" <<'PY'
from pathlib import Path
import subprocess
import sys

root = Path(sys.argv[1])
arguments = sys.argv[2:]
if arguments not in ([], ["--check"]):
    print("usage: format-go.sh [--check]", file=sys.stderr)
    sys.exit(2)

check_only = arguments == ["--check"]
failed = False
paths = sorted(
    path for path in root.rglob("*.go")
    if not {".git", "vendor"}.intersection(path.relative_to(root).parts[:-1])
)
for path in paths:
    relative = path.relative_to(root)
    result = subprocess.run(["gofmt", str(path)], capture_output=True)
    if result.returncode != 0:
        print(result.stderr.decode("utf-8", errors="replace"), file=sys.stderr, end="")
        failed = True
        continue

    formatted = result.stdout.decode("utf-8").expandtabs(4)
    data = formatted.encode("utf-8")
    if path.read_bytes() != data:
        if check_only:
            print(f"{relative}: run ./scripts/format-go.sh", file=sys.stderr)
            failed = True
        else:
            path.write_bytes(data)

    for number, line in enumerate(formatted.splitlines(), 1):
        if len(line) > 120:
            print(f"{relative}:{number}: {len(line)} characters; maximum is 120", file=sys.stderr)
            failed = True

sys.exit(1 if failed else 0)
PY
