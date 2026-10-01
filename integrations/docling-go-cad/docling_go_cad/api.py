"""DoclingDocument 构建与 convert_cad 便捷 API。

本模块只依赖 docling-core（不依赖完整 docling），既能被
backend.py 的 docling 后端复用，也允许仅安装 docling-core 的
调用方直接把 manifest 转成 ``DoclingDocument``。
"""

from __future__ import annotations

import struct
from pathlib import Path
from typing import Optional, Union

from docling_core.types.doc import DocItemLabel, ImageRef
from docling_core.types.doc.document import DoclingDocument

from docling_go_cad.runner import CadCliRunner

__all__ = ["build_cad_document", "convert_cad", "DEFAULT_DPI"]

# manifest 未提供 DPI 时，嵌入图片采用的默认分辨率
DEFAULT_DPI = 150


def _png_dimensions(png_path: Path) -> Optional[tuple]:
    """从 PNG 文件头读取像素尺寸（IHDR 固定偏移），非 PNG 头返回 None。

    先于 PIL 打开文件使用：工程拆图为保证文字清晰会自动放大分辨率，
    单张可达 2~3 亿像素，超过 PIL 默认的防炸弹阈值，若先 open 再取
    尺寸则防护检查直接抛 DecompressionBombError，拿不到尺寸。
    """
    try:
        with open(png_path, "rb") as fp:
            head = fp.read(24)
    except OSError:
        return None
    if head[:8] != b"\x89PNG\r\n\x1a\n" or head[12:16] != b"IHDR":
        return None
    width, height = struct.unpack(">II", head[16:24])
    return width, height


def _image_ref(png_path: Path) -> ImageRef:
    """从 PNG 文件构造 docling-core 的 ImageRef（base64 内嵌）。

    docling-core 不同版本的类工厂名不同（from_pil / from_pil_image），
    这里按安装版本自动选择。

    图片来源是本包驱动的 caddocling 渲染产物（受信内容），因此按
    实际像素尺寸按需上调 PIL 的防炸弹阈值，而不是拒绝真实工程大图。
    """
    from PIL import Image

    dimensions = _png_dimensions(png_path)
    limit = Image.MAX_IMAGE_PIXELS
    if dimensions is not None:
        needed = dimensions[0] * dimensions[1]
        if limit is not None and needed > limit:
            Image.MAX_IMAGE_PIXELS = needed
    elif limit is not None:
        # 读不到 PNG 头（异常产物）时保持可用：解除限制交由调用方
        # 对输入文件的可信度负责
        Image.MAX_IMAGE_PIXELS = None

    with Image.open(png_path) as pil_image:
        factory = getattr(ImageRef, "from_pil", None) or ImageRef.from_pil_image
        return factory(pil_image.convert("RGB"), DEFAULT_DPI)


def _add_picture(
    doc: DoclingDocument,
    png_path: Path,
    caption: Optional[object] = None,
) -> None:
    """向文档追加一张图片；caption 传入 add_text 返回的条目作为图名说明。"""
    kwargs = {}
    if caption is not None:
        kwargs["caption"] = caption
    doc.add_picture(image=_image_ref(png_path), **kwargs)


def build_cad_document(
    manifest: dict,
    outdir: Union[str, Path],
    name: Optional[str] = None,
) -> DoclingDocument:
    """把 caddocling 的 manifest 构建为 :class:`DoclingDocument`。

    结构约定：
      - sheet=0（不在任何图框内）的文本置于文档最前；
      - 每个图框一个节：图名作 section header，图框渲染图作 picture
        （caption 指向该 header），图框内文本逐条 add_text；
      - 存在整图时最后追加整图 picture；
      - 无任何图框时用整图做单节兜底（节名取源文件名）。

    :param manifest: caddocling 产出的 manifest 字典（schema_version=1）。
    :param outdir: 产出目录，manifest 中图片文件名相对该目录。
    :param name: 文档名；缺省时取 manifest.source 的文件主干名。
    :return: 仅含文本项与图片项的 :class:`DoclingDocument`。
    """
    out_dir = Path(outdir)
    if name is None:
        name = Path(str(manifest.get("source") or "drawing")).stem
    doc = DoclingDocument(name=name)

    sheets = manifest.get("sheets") or []
    texts = manifest.get("texts") or []

    # 按 sheet 序号分桶，避免每个图框都全量遍历 texts
    texts_by_sheet: dict = {}
    for text in texts:
        texts_by_sheet.setdefault(text.get("sheet", 0), []).append(text)

    if sheets:
        # 图框外文本（sheet=0）不属于任何节，置于文档开头；
        # 无图框时走整图兜底分支，全部文本归入兜底节
        for text in texts_by_sheet.get(0, []):
            doc.add_text(label=DocItemLabel.TEXT, text=str(text.get("text", "")))

    for sheet in sheets:
        sheet_index = sheet.get("index")
        sheet_name = str(sheet.get("name") or f"sheet-{sheet_index}")
        header = doc.add_text(label=DocItemLabel.SECTION_HEADER, text=sheet_name)

        image_name = sheet.get("image")
        if image_name:
            image_path = out_dir / image_name
            if image_path.is_file():
                _add_picture(doc, image_path, caption=header)

        for text in texts_by_sheet.get(sheet_index, []):
            doc.add_text(label=DocItemLabel.TEXT, text=str(text.get("text", "")))

    full_image = manifest.get("full_image")
    if full_image:
        full_path = out_dir / full_image
        if full_path.is_file():
            if sheets:
                # 已有图框节：整图作为补充视图追加在最后
                _add_picture(doc, full_path)
            else:
                # 无图框兜底：整图 + 单节（节名即文档名）；此时不存在
                # 图框归属，全部文本都归入该节
                header = doc.add_text(
                    label=DocItemLabel.SECTION_HEADER, text=str(name)
                )
                _add_picture(doc, full_path, caption=header)
                for text in texts:
                    doc.add_text(label=DocItemLabel.TEXT, text=str(text.get("text", "")))

    return doc


def convert_cad(
    path: Union[str, Path],
    outdir: Optional[Union[str, Path]] = None,
    register: bool = True,
) -> DoclingDocument:
    """一步把 DWG/DXF/DXFB 转成 :class:`DoclingDocument`。

    不经 ``DocumentConverter`` 路由，直接调用 caddocling 并组装文档，
    适合只要结果的场景；需要与其他格式统一入口时改用
    ``DocumentConverter`` + :class:`~docling_go_cad.backend.CadFormatOption`。

    :param path: 输入 CAD 文件路径。
    :param outdir: caddocling 产出目录；缺省时使用一次性临时目录。
    :param register: 是否先执行 :func:`~docling_go_cad.registry.register_docling`
        （需要完整 docling；仅装 docling-core 时传 False）。
    :return: 解析后的 :class:`DoclingDocument`。
    """
    if register:
        from docling_go_cad.registry import register_docling

        register_docling()
    conversion = CadCliRunner().convert(path, outdir=outdir)
    return build_cad_document(conversion.manifest, conversion.outdir)
