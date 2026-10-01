"""pytest 共享夹具：伪造 caddocling 二进制与 manifest fixture。

不依赖完整 docling，只依赖 docling-core（含 pillow）。
"""

from __future__ import annotations

import base64
import json
import stat
import sys
from pathlib import Path

import pytest

# 1x1 红色 PNG 的 base64，用作 manifest 引用的最小图片
_PNG_1PX_B64 = (
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
)

FAKE_PNG_BYTES = base64.b64decode(_PNG_1PX_B64)

# 默认伪造 manifest：1 个图框 + 图框内/外各一条文本 + 整图
FAKE_MANIFEST = {
    "schema_version": 1,
    "generator": "fake-caddocling",
    "source": "/data/demo.dwg",
    "dwg_version": "AC1015",
    "sheets": [
        {
            "index": 1,
            "name": "sheet-1",
            "image": "sheet-1.png",
            "width": 100,
            "pass_rate": 0.99,
            "rerenders": 1,
            "text_count": 1,
        }
    ],
    "full_image": "full.png",
    "texts": [
        {
            "text": "hello",
            "x": 1.0,
            "y": 2.0,
            "height": 3.0,
            "rotation": 0.0,
            "layer": "0",
            "sheet": 1,
        },
        {
            "text": "world",
            "x": 4.0,
            "y": 5.0,
            "height": 3.0,
            "rotation": 90.0,
            "layer": "TEXT",
            "sheet": 0,
        },
    ],
}

# 伪造 CLI 的脚本模板：解析 -o 参数，落盘 manifest 与 PNG，
# stdout 打印日志行 + manifest 行（均可通过 format 参数定制）。
# shebang 用当前解释器绝对路径：定位测试会把 PATH 改掉，
# /usr/bin/env python3 将找不到解释器。
# 约定：manifest 引用的文件名含 "ghost" 时故意不写盘，模拟 CLI 漏写。
_CLI_TEMPLATE = """#!{python}
import base64, json, os, sys

argv = sys.argv[1:]
outdir = argv[argv.index("-o") + 1] if "-o" in argv else "."
os.makedirs(outdir, exist_ok=True)
manifest = json.loads({payload!r})

png = base64.b64decode({_png_b64!r})
referenced = [s.get("image") for s in manifest.get("sheets", []) if s.get("image")]
if manifest.get("full_image"):
    referenced.append(manifest["full_image"])
for name in referenced:
    if "ghost" in name:
        continue
    with open(os.path.join(outdir, name), "wb") as fh:
        fh.write(png)

manifest_path = os.path.abspath(os.path.join(outdir, "manifest.json"))
with open(manifest_path, "w", encoding="utf-8") as fh:
    json.dump(manifest, fh)

print("rendering ...")
print({tail!r}.format(manifest_path=manifest_path))
sys.stderr.write({stderr!r})
sys.exit({exit_code})
"""


def make_fake_cli(
    directory: Path,
    manifest: dict | None = None,
    exit_code: int = 0,
    stdout_tail: str | None = None,
    stderr: str = "",
) -> Path:
    """在 directory 下生成可执行的伪造 caddocling。

    :param manifest: CLI 将落盘的 manifest 内容；None 用 FAKE_MANIFEST。
    :param exit_code: CLI 退出码，用于模拟失败。
    :param stdout_tail: 覆盖 stdout 末行；None 打印标准 manifest 行。
    :param stderr: CLI 写到 stderr 的内容。
    :return: 伪造二进制路径。
    """
    directory.mkdir(parents=True, exist_ok=True)
    script = directory / "caddocling"
    payload = manifest if manifest is not None else FAKE_MANIFEST
    tail = stdout_tail if stdout_tail is not None else "manifest: {manifest_path}"
    script.write_text(
        _CLI_TEMPLATE.format(
            python=sys.executable,
            payload=json.dumps(payload),
            _png_b64=_PNG_1PX_B64,
            tail=tail,
            stderr=stderr,
            exit_code=exit_code,
        )
    )
    script.chmod(script.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
    return script


def write_manifest_fixture(outdir: Path, manifest: dict | None = None) -> Path:
    """把 manifest 与其引用的 PNG 文件真实写到 outdir，返回 manifest 路径。"""
    outdir.mkdir(parents=True, exist_ok=True)
    payload = manifest if manifest is not None else FAKE_MANIFEST
    for name in [s.get("image") for s in payload.get("sheets", []) if s.get("image")]:
        (outdir / name).write_bytes(FAKE_PNG_BYTES)
    if payload.get("full_image"):
        (outdir / payload["full_image"]).write_bytes(FAKE_PNG_BYTES)
    path = outdir / "manifest.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


@pytest.fixture
def fake_manifest() -> dict:
    """默认伪造 manifest（深拷贝，测试可自由改写）。"""
    return json.loads(json.dumps(FAKE_MANIFEST))


@pytest.fixture
def manifest_outdir(tmp_path: Path, fake_manifest: dict) -> Path:
    """已落盘伪造 manifest 及 PNG 的产出目录。"""
    write_manifest_fixture(tmp_path / "out", fake_manifest)
    return tmp_path / "out"


@pytest.fixture
def input_dwg(tmp_path: Path) -> Path:
    """带 DWG 魔数的伪输入文件。"""
    dwg = tmp_path / "demo.dwg"
    dwg.write_bytes(b"AC1015" + b"\x00" * 32)
    return dwg
