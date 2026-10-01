"""registry 注入逻辑的单测：用 stub docling 验证幂等注入、追加语义与错误指引。

真实 docling 的注入已由 heavy 测试覆盖；本文件保证仅装 docling-core
的普通环境下 registry 行为可回归。filetype 匹配器用真实库验证。
"""

from __future__ import annotations

import sys
import types
from enum import Enum

import pytest

from docling_go_cad import registry as registry_module


def _install_stub_docling(monkeypatch: pytest.MonkeyPatch) -> types.ModuleType:
    """构造最小 docling.datamodel.base_models stub 并挂到 sys.modules。

    结构按 docling 2.131 实际源码：InputFormat(str, Enum)、
    FormatToExtensions、FormatToMimeType、以及由后者推导的
    MimeTypeToFormat 组合 dict。
    """

    class InputFormat(str, Enum):
        PDF = "pdf"
        MD = "md"

    format_to_extensions = {InputFormat.PDF: ["pdf"], InputFormat.MD: ["md", "txt"]}
    format_to_mime = {
        InputFormat.PDF: ["application/pdf"],
        InputFormat.MD: ["text/plain", "text/markdown"],
    }
    mime_to_format = {
        mime: [fmt for fmt in format_to_mime if mime in format_to_mime[fmt]]
        for values in format_to_mime.values()
        for mime in values
    }

    base_models = types.ModuleType("docling.datamodel.base_models")
    base_models.InputFormat = InputFormat
    base_models.FormatToExtensions = format_to_extensions
    base_models.FormatToMimeType = format_to_mime
    base_models.MimeTypeToFormat = mime_to_format

    datamodel = types.ModuleType("docling.datamodel")
    docling_pkg = types.ModuleType("docling")
    docling_pkg.datamodel = datamodel
    datamodel.base_models = base_models

    monkeypatch.setitem(sys.modules, "docling", docling_pkg)
    monkeypatch.setitem(sys.modules, "docling.datamodel", datamodel)
    monkeypatch.setitem(sys.modules, "docling.datamodel.base_models", base_models)
    return base_models


def test_register_injects_cad_format(monkeypatch: pytest.MonkeyPatch):
    """注入后 InputFormat.CAD 与三张路由表全部就位。"""
    base_models = _install_stub_docling(monkeypatch)

    registry_module.register_docling()

    cad = base_models.InputFormat.CAD
    assert cad.value == "cad"
    assert base_models.FormatToExtensions[cad] == ["dwg", "dxf", "dxfb"]
    assert base_models.FormatToMimeType[cad] == ["image/vnd.dwg", "image/vnd.dxf"]
    # MimeTypeToFormat 是追加而非覆盖：text/plain 原有 MD 仍保留
    assert cad in base_models.MimeTypeToFormat["image/vnd.dwg"]
    assert cad in base_models.MimeTypeToFormat["image/vnd.dxf"]
    assert base_models.InputFormat.MD in base_models.MimeTypeToFormat["text/plain"]
    # 枚举常规行为对新成员生效
    assert base_models.InputFormat("cad") is cad
    assert cad in list(base_models.InputFormat)


def test_register_is_idempotent(monkeypatch: pytest.MonkeyPatch):
    """重复注册不产生重复成员或重复表项。"""
    base_models = _install_stub_docling(monkeypatch)

    registry_module.register_docling()
    cad = base_models.InputFormat.CAD
    dwg_holders = base_models.MimeTypeToFormat["image/vnd.dwg"]
    member_count = len(list(base_models.InputFormat))

    registry_module.register_docling()
    registry_module.register_docling()

    assert base_models.InputFormat.CAD is cad
    assert len(list(base_models.InputFormat)) == member_count
    assert base_models.MimeTypeToFormat["image/vnd.dwg"] == dwg_holders
    assert base_models.MimeTypeToFormat["image/vnd.dwg"].count(cad) == 1


def test_register_without_docling_gives_hint(monkeypatch: pytest.MonkeyPatch):
    """docling 缺失时抛 ImportError 并附安装指引。"""
    monkeypatch.delitem(sys.modules, "docling", raising=False)

    # 屏蔽真实 docling 的导入（venv 中可能已安装）
    import builtins

    original_import = builtins.__import__

    def _blocked(name, *args, **kwargs):
        if name == "docling" or name.startswith("docling."):
            raise ImportError(f"No module named {name!r}")
        return original_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", _blocked)

    with pytest.raises(ImportError, match=r"docling-go-cad\[docling\]"):
        registry_module.register_docling()


def test_filetype_matchers_registered(monkeypatch: pytest.MonkeyPatch):
    """filetype 存在时，DWG/DXF/DXFB 内容被嗅探为对应 MIME。"""
    filetype = pytest.importorskip("filetype", reason="需要 filetype 库")
    _install_stub_docling(monkeypatch)

    registry_module.register_docling()

    assert filetype.guess_mime(b"AC1015" + b"\x00" * 32) == "image/vnd.dwg"
    assert (
        filetype.guess_mime(b"  0\nSECTION\n  2\nHEADER\n  0\nENDSEC\n")
        == "image/vnd.dxf"
    )
    assert (
        filetype.guess_mime(b"AutoCAD Binary DXF\r\n\x1a\x00" + b"\x00" * 16)
        == "image/vnd.dxf"
    )
    # 不误报常见格式
    assert filetype.guess_mime(b"\x89PNG\r\n\x1a\n" + b"\x00" * 16) == "image/png"
    assert filetype.guess_mime(b"NOTDWGFILE123456") is None


def test_register_without_filetype_is_tolerated(monkeypatch: pytest.MonkeyPatch):
    """filetype 缺失（老版 docling）时注册静默跳过，不报错。"""
    _install_stub_docling(monkeypatch)
    monkeypatch.setitem(sys.modules, "filetype", None)  # None = 禁止导入

    registry_module.register_docling()
