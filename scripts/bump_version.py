#!/usr/bin/env python3
"""Campus Auto Login 版本号更新工具。

默认把 patch 位 +1；也可以显式指定完整版本号：
  python scripts/bump_version.py            # 4.3.2 -> 4.3.3
  python scripts/bump_version.py --set 4.4.0

minor / major 只在用户明确要求时手动 --set，不要日常随手改。
"""
from __future__ import annotations

import argparse
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VERSION_FILE = ROOT / "internal" / "version" / "version.txt"
RC_FILE = ROOT / "assets" / "app.rc"
README_FILE = ROOT / "release" / "使用说明.txt"
SEMVER = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def read_version() -> tuple[int, int, int]:
    raw = VERSION_FILE.read_text(encoding="utf-8").strip()
    if not SEMVER.fullmatch(raw):
        raise SystemExit(f"版本文件不是 x.y.z 格式：{raw!r}")
    a, b, c = (int(part) for part in raw.split("."))
    return a, b, c


def write_version(major: int, minor: int, patch: int) -> None:
    number = f"{major}.{minor}.{patch}"
    VERSION_FILE.write_text(number + "\n", encoding="utf-8", newline="\n")

    rc = RC_FILE.read_text(encoding="utf-8")
    rc = re.sub(r"(FILEVERSION\s+)\d+,\s*\d+,\s*\d+,\s*\d+", rf"\g<1>{major},{minor},{patch},0", rc)
    rc = re.sub(r"(PRODUCTVERSION\s+)\d+,\s*\d+,\s*\d+,\s*\d+", rf"\g<1>{major},{minor},{patch},0", rc)
    rc = re.sub(r'(VALUE "FileVersion",\s*)"\d+\.\d+\.\d+\.\d+"', rf'\g<1>"{number}.0"', rc)
    rc = re.sub(r'(VALUE "ProductVersion",\s*)"\d+\.\d+\.\d+\.\d+"', rf'\g<1>"{number}.0"', rc)
    RC_FILE.write_text(rc, encoding="utf-8", newline="\n")

    if README_FILE.exists():
        text = README_FILE.read_text(encoding="utf-8")
        text = re.sub(r"校园网自动认证\s+v\d+\.\d+\.\d+", f"校园网自动认证  v{number}", text, count=1)
        README_FILE.write_text(text, encoding="utf-8", newline="\n")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--set", dest="set_version", help="显式设置 x.y.z；默认只递增 patch")
    args = parser.parse_args()

    major, minor, patch = read_version()
    if args.set_version:
        if not SEMVER.fullmatch(args.set_version):
            raise SystemExit("版本号必须是 x.y.z")
        major, minor, patch = (int(part) for part in args.set_version.split("."))
    else:
        patch += 1

    old = f"{major}.{minor}.{patch - 1}" if not args.set_version else None
    write_version(major, minor, patch)
    print(f"version: {old + ' -> ' if old else ''}{major}.{minor}.{patch}")


if __name__ == "__main__":
    main()
