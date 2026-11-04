#!/usr/bin/env python3
"""构建并打包一个独立版本目录。

使用：
  python scripts/release.py

输出：
  dist/CampusAutoLogin-v4.3.2-win64/
  dist/CampusAutoLogin-v4.3.2-win64.zip

不会覆盖其它版本目录；如果目标目录已存在，会直接退出。
"""
from __future__ import annotations

import argparse
import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VERSION_FILE = ROOT / "internal" / "version" / "version.txt"
DIST = ROOT / "dist"
SEMVER = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def run(cmd: list[str]) -> None:
    print("+", " ".join(cmd))
    subprocess.run(cmd, cwd=ROOT, check=True)


def find_tool(name: str) -> str:
    from shutil import which

    path = which(name)
    if not path:
        raise SystemExit(f"找不到 {name}；请确认 Go / MinGW 工具链已在 PATH 中")
    return path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--skip-tests", action="store_true", help="仅用于本机快速试包，正式发布不要用")
    args = parser.parse_args()

    version = VERSION_FILE.read_text(encoding="utf-8").strip()
    if not SEMVER.fullmatch(version):
        raise SystemExit(f"版本文件不是 x.y.z 格式：{version!r}")

    tag = f"v{version}"
    pkg_dir = DIST / f"CampusAutoLogin-{tag}-win64"
    zip_path = DIST / f"CampusAutoLogin-{tag}-win64.zip"
    if pkg_dir.exists() or zip_path.exists():
        raise SystemExit(f"发布产物已存在，禁止覆盖：{pkg_dir} / {zip_path}")

    go = find_tool("go")
    windres = find_tool("windres")
    run([windres, "-i", str(ROOT / "assets" / "app.rc"), "-O", "coff",
         "-o", str(ROOT / "rsrc_windows_amd64.syso")])

    if not args.skip_tests:
        run([go, "test", "./..."])

    exe = ROOT / "build" / f"CampusAutoLogin-{tag}.exe"
    exe.parent.mkdir(parents=True, exist_ok=True)
    run([go, "build", "-trimpath",
         "-ldflags", "-s -w -H windowsgui",
         "-o", str(exe), "."])

    pkg_dir.mkdir(parents=True, exist_ok=False)
    shutil.copy2(exe, pkg_dir / "校园网认证.exe")
    usage = ROOT / "release" / "使用说明.txt"
    if usage.exists():
        shutil.copy2(usage, pkg_dir / "使用说明.txt")

    shutil.make_archive(str(zip_path.with_suffix("")), "zip", root_dir=DIST, base_dir=pkg_dir.name)
    print(f"packed: {zip_path}")


if __name__ == "__main__":
    main()
