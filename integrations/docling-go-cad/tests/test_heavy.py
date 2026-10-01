"""heavy 集成测试：真实 caddocling + 完整 docling 的 DocumentConverter 全路由。

文件级 importorskip("docling")：本机未安装 docling 时整个文件跳过，
普通 pytest 运行仍保持全绿；安装 docling 并具备 go 工具链后自动生效。
"""

from __future__ import annotations

import os
import shutil
import subprocess
from pathlib import Path

import pytest

pytest.importorskip("docling", reason="heavy 集成测试需要完整 docling")

from docling.datamodel.base_models import ConversionStatus, InputFormat
from docling.document_converter import DocumentConverter

from docling_go_cad import CadCliRunner, CadFormatOption, register_docling

# tests -> docling-go-cad -> integrations -> 仓库根（parents[3]）
REPO_ROOT = Path(__file__).resolve().parents[3]
DWG_SAMPLE = REPO_ROOT / "testdata" / "lw_example2018.dwg"
CLI_PACKAGE_DIR = REPO_ROOT / "cmd" / "caddocling"


@pytest.fixture(scope="module")
def caddocling_binary(tmp_path_factory):
    """编译真实 caddocling；Go 侧未就绪时跳过而非失败。"""
    if shutil.which("go") is None:
        pytest.skip("go 工具链不可用，无法编译 caddocling")
    if not CLI_PACKAGE_DIR.is_dir():
        pytest.skip("cmd/caddocling 尚未实现（Go 侧并行开发中）")
    binary = tmp_path_factory.mktemp("caddocling-bin") / "caddocling"
    proc = subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/caddocling"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        pytest.skip(f"caddocling 编译失败: {proc.stderr.strip()}")
    return binary


@pytest.fixture(scope="module")
def cad_environment(caddocling_binary):
    """模块级设置 CADCLI_BIN，测试结束后恢复。"""
    previous = os.environ.get("CADCLI_BIN")
    os.environ["CADCLI_BIN"] = str(caddocling_binary)
    yield
    if previous is None:
        os.environ.pop("CADCLI_BIN", None)
    else:
        os.environ["CADCLI_BIN"] = previous


@pytest.fixture(scope="module")
def converter(cad_environment):
    """完成 register_docling 注入并构建带 CAD 路由的 DocumentConverter。"""
    register_docling()
    return DocumentConverter(
        format_options={InputFormat.CAD: CadFormatOption()}
    )


@pytest.mark.heavy
def test_document_converter_routes_cad(converter, cad_environment):
    """对真实 DWG 走完整 DocumentConverter 路由，断言 SUCCESS 且 markdown 含文本。"""
    if not DWG_SAMPLE.is_file():
        pytest.skip(f"样例文件不存在: {DWG_SAMPLE}")

    # 先用 runner 取一份 manifest 作为期望基准（图框名应出现在 markdown 中）
    reference = CadCliRunner().convert(DWG_SAMPLE)

    result = converter.convert(DWG_SAMPLE)

    assert result.status == ConversionStatus.SUCCESS
    markdown = result.document.export_to_markdown()
    assert markdown.strip(), "markdown 导出为空"

    sheet_names = [sheet["name"] for sheet in reference.manifest.get("sheets", [])]
    assert sheet_names, "manifest 中没有图框"
    for name in sheet_names:
        assert name in markdown, f"markdown 缺少图框名 {name!r}"

    text_bodies = [
        entry["text"]
        for entry in reference.manifest.get("texts", [])
        if entry.get("text")
    ]
    if text_bodies:
        # 多行 MTEXT 的换行在 markdown 导出时会被规范化为空白，
        # 因此按空白折叠后的内容校验存在性，而非逐字符子串匹配
        def _fold(value: str) -> str:
            return " ".join(value.split())

        markdown_folded = _fold(markdown)
        missing = [text for text in text_bodies if _fold(text) not in markdown_folded]
        assert not missing, f"markdown 缺少文本实体: {missing}"


@pytest.mark.heavy
def test_backend_is_valid_real_dwg(cad_environment):
    """真实 DWG 通过后端真伪校验（扩展名 + 魔数互相印证）。"""
    if not DWG_SAMPLE.is_file():
        pytest.skip(f"样例文件不存在: {DWG_SAMPLE}")

    from docling_go_cad.backend import CadDocumentBackend

    class _StubDoc:
        file = DWG_SAMPLE
        document_hash = "heavy-test"
        format = None

    backend = CadDocumentBackend(_StubDoc(), DWG_SAMPLE)
    assert backend.is_valid()


@pytest.mark.heavy
def test_full_route_with_fake_cli(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """不依赖真实 caddocling：用伪造 CLI 验证完整 docling 的 CAD 路由链路。

    覆盖 register_docling 注入 → filetype 嗅探 → DocumentConverter 路由 →
    CadDocumentBackend 执行 CLI → DoclingDocument 组装 → markdown 导出；
    Go 侧 cmd/caddocling 尚未就绪时，本用例保证 Python 侧链路可独立验证。
    """
    from conftest import make_fake_cli

    register_docling()

    cli = make_fake_cli(tmp_path / "bin")
    monkeypatch.setenv("CADCLI_BIN", str(cli))
    fake_dwg = tmp_path / "demo.dwg"
    fake_dwg.write_bytes(b"AC1015" + b"\x00" * 64)

    converter = DocumentConverter(
        format_options={InputFormat.CAD: CadFormatOption()}
    )
    result = converter.convert(fake_dwg)

    assert result.status == ConversionStatus.SUCCESS
    markdown = result.document.export_to_markdown()
    assert "sheet-1" in markdown  # 图框名作为节头进入 markdown
    assert "hello" in markdown  # 图框内文本
    assert "world" in markdown  # 图框外文本（sheet=0）
