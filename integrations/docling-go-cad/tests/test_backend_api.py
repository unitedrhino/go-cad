"""backend / api 的单测：仅用 docling-core 直接构建 DoclingDocument 并断言结构。"""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from docling_core.types.doc import DocItemLabel
from docling_core.types.doc.document import DoclingDocument

from docling_go_cad import build_cad_document, convert_cad
from docling_go_cad.runner import CadConversion


def _labels(doc: DoclingDocument) -> list:
    return [item.label for item in doc.texts]


def test_build_document_structure(manifest_outdir: Path, fake_manifest: dict):
    """图框：节头 + 图片(caption=图名) + 图框内文本；图框外文本在最前。"""
    doc = build_cad_document(fake_manifest, manifest_outdir, name="demo")

    assert isinstance(doc, DoclingDocument)
    assert doc.name == "demo"

    # 文本项：1 个节头 + 2 条正文（hello 在 sheet-1，world 在图框外）
    assert _labels(doc) == [
        DocItemLabel.TEXT,          # world（sheet=0，文档开头）
        DocItemLabel.SECTION_HEADER,  # sheet-1 节头
        DocItemLabel.TEXT,          # hello
    ]
    plain_texts = [
        item.text
        for item in doc.texts
        if item.label == DocItemLabel.TEXT
    ]
    assert sorted(plain_texts) == ["hello", "world"]

    # 图片项：图框图 + 整图
    assert len(doc.pictures) == 2

    # 图框图的 caption 指向节头条目，其文本为图名
    first_picture = doc.pictures[0]
    captions = [ref.resolve(doc) for ref in first_picture.captions]
    assert [item.text for item in captions] == ["sheet-1"]

    # 整图无 caption
    assert not list(doc.pictures[1].captions)

    # 图片以内嵌 ImageRef 方式携带
    assert first_picture.image is not None


def test_build_document_default_name_from_source(manifest_outdir: Path, fake_manifest: dict):
    """name 缺省时取 manifest.source 的文件主干名。"""
    doc = build_cad_document(fake_manifest, manifest_outdir)
    assert doc.name == "demo"  # FAKE_MANIFEST.source = /data/demo.dwg


def test_build_document_without_sheets_falls_back_to_full_image(
    tmp_path: Path, fake_manifest: dict
):
    """无图框时用整图单节兜底：节头（节名=文档名）+ 整图 + 全部文本归入该节。"""
    fake_manifest["sheets"] = []
    from conftest import write_manifest_fixture

    outdir = tmp_path / "out"
    write_manifest_fixture(outdir, fake_manifest)

    doc = build_cad_document(fake_manifest, outdir, name="fallback")

    assert _labels(doc) == [
        DocItemLabel.SECTION_HEADER,
        DocItemLabel.TEXT,
        DocItemLabel.TEXT,
    ]
    assert doc.texts[0].text == "fallback"
    # 无图框时所有文本（含 sheet=1 的 hello）都归入兜底节
    assert sorted(item.text for item in doc.texts[1:]) == ["hello", "world"]
    assert len(doc.pictures) == 1
    captions = [ref.resolve(doc) for ref in doc.pictures[0].captions]
    assert [item.text for item in captions] == ["fallback"]


def test_build_document_empty_manifest(tmp_path: Path, fake_manifest: dict):
    """无图框且无整图时得到空文档（无文本项、无图片项）。"""
    fake_manifest["sheets"] = []
    fake_manifest["full_image"] = None
    fake_manifest["texts"] = []

    doc = build_cad_document(fake_manifest, tmp_path, name="empty")

    assert doc.texts == []
    assert doc.pictures == []


def test_convert_cad_with_fake_cli(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    input_dwg: Path,
):
    """convert_cad 走伪造 CLI：注册关闭（无 docling）也能一步得到文档。"""
    from conftest import make_fake_cli

    cli = make_fake_cli(tmp_path / "bin")
    monkeypatch.setenv("CADCLI_BIN", str(cli))

    doc = convert_cad(input_dwg, register=False)

    assert isinstance(doc, DoclingDocument)
    assert doc.name == "demo"  # manifest.source 决定文档名
    assert len(doc.pictures) == 2
    section_headers = [
        item.text
        for item in doc.texts
        if item.label == DocItemLabel.SECTION_HEADER
    ]
    assert section_headers == ["sheet-1"]


def test_runner_returns_named_tuple(manifest_outdir: Path, fake_manifest: dict):
    """CadConversion 携带 manifest dict 与 outdir，字段可直接取用。"""
    conversion = CadConversion(manifest=fake_manifest, outdir=manifest_outdir)

    assert conversion.manifest["schema_version"] == 1
    assert conversion.outdir == manifest_outdir
    assert json.loads(
        (conversion.outdir / "manifest.json").read_text(encoding="utf-8")
    )["dwg_version"] == "AC1015"


def test_image_ref_raises_pil_pixel_limit(tmp_path, monkeypatch):
    """大像素工程图超过 PIL 默认防炸弹阈值时应按需上调而非拒绝。

    工程拆图为保证文字清晰会自动放大，单张可达 2~3 亿像素；这里把
    阈值人为压到极小模拟触发场景，断言 _image_ref 读取成功且阈值被
    上调到实际尺寸。
    """
    from PIL import Image

    from docling_go_cad.api import _image_ref

    monkeypatch.setattr(Image, "MAX_IMAGE_PIXELS", 64)  # 远小于 256×256
    png_path = tmp_path / "big.png"
    Image.new("RGB", (256, 256), color=(10, 20, 30)).save(png_path, format="PNG")

    try:
        ref = _image_ref(png_path)
        assert ref is not None
        assert Image.MAX_IMAGE_PIXELS >= 256 * 256
    finally:
        monkeypatch.undo()


def test_png_dimensions_reads_ihdr(tmp_path):
    """_png_dimensions 从 PNG 头读尺寸；非 PNG 文件返回 None。"""
    from PIL import Image

    from docling_go_cad.api import _png_dimensions

    png_path = tmp_path / "sized.png"
    Image.new("RGB", (64, 48)).save(png_path, format="PNG")
    assert _png_dimensions(png_path) == (64, 48)

    bogus = tmp_path / "bogus.png"
    bogus.write_bytes(b"not a png at all")
    assert _png_dimensions(bogus) is None
