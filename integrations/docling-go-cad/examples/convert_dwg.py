"""convert_dwg.py：docling-go-cad 一键演示脚本。

对一张 DWG/DXF 图纸执行完整的 docling 转换，并把产物落盘：
  - <输出目录>/doc.md     —— DoclingDocument 的 Markdown 导出（含图名与图纸文本）
  - <输出目录>/doc.json   —— DoclingDocument 的完整 JSON
  - <输出目录>/images/    —— 智能拆图的每张 PNG（与 manifest 一一对应）

用法：
  python examples/convert_dwg.py                    # 演示仓库自带样例
  python examples/convert_dwg.py 你的图纸.dwg -o out

前置条件：
  - Python 侧：pip install "docling-go-cad[docling]"（或本仓库源码安装）
  - Go 侧 CLI：任选其一
      go install github.com/unitedrhino/go-cad/cmd/caddocling@latest
      或从 GitHub Releases 下载 caddocling 放入 PATH
      或用环境变量 CADCLI_BIN 指向二进制路径
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

# 仓库根目录（examples -> docling-go-cad -> integrations -> 仓库根）
REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_SAMPLE = REPO_ROOT / "testdata" / "lw_example2018.dwg"


def main() -> int:
    parser = argparse.ArgumentParser(description="DWG/DXF → docling DoclingDocument 一键演示")
    parser.add_argument("input", nargs="?", default=str(DEFAULT_SAMPLE), help="输入 DWG/DXF 路径（缺省用仓库样例）")
    parser.add_argument("-o", "--outdir", default="caddocling-demo", help="产物输出目录（缺省 ./caddocling-demo）")
    args = parser.parse_args()

    input_path = Path(args.input)
    if not input_path.is_file():
        print(f"输入文件不存在: {input_path}", file=sys.stderr)
        return 1

    # CLI 未就绪时给出可直接执行的安装指引，而不是裸报错
    from docling_go_cad.runner import CadCliNotFoundError, find_binary

    try:
        find_binary()
    except CadCliNotFoundError as exc:
        print(exc, file=sys.stderr)
        return 1

    from docling_go_cad import register_docling
    from docling_go_cad.api import convert_cad

    register_docling()  # 让 docling 的 DocumentConverter 认识 .dwg/.dxf

    outdir = Path(args.outdir)
    doc = convert_cad(input_path, outdir=outdir)

    outdir.mkdir(parents=True, exist_ok=True)
    (outdir / "doc.md").write_text(doc.export_to_markdown(), encoding="utf-8")
    (outdir / "doc.json").write_text(
        json.dumps(doc.export_to_dict(), ensure_ascii=False, indent=1), encoding="utf-8"
    )

    manifest_path = outdir / "manifest.json"
    sheets = 0
    if manifest_path.is_file():
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        sheets = len(manifest.get("sheets", []))
        images = outdir / "images"
        images.mkdir(exist_ok=True)
        for image in sorted(outdir.glob("*.png")) + sorted(outdir.glob("*.svg")):
            shutil.copy2(image, images / image.name)

    print(f"转换完成: {input_path.name}")
    print(f"  拆图数量: {sheets}")
    print(f"  Markdown: {outdir / 'doc.md'}")
    print(f"  JSON:     {outdir / 'doc.json'}")
    if sheets:
        print(f"  拆图图片: {outdir / 'images'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
