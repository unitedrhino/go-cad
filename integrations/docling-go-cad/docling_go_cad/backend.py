"""docling 集成层：CadFormatOption 与 CadDocumentBackend。

本模块依赖完整 docling（backend 基类、FormatOption、SimplePipeline），
由 ``docling_go_cad/__init__.py`` 按需导入；仅安装 docling-core 时
不会加载本模块。文档组装逻辑在 :mod:`docling_go_cad.api` 中实现。
"""

from __future__ import annotations

import logging
import tempfile
from io import BytesIO
from pathlib import Path
from typing import Optional, Type, Union

from docling.backend.abstract_backend import (
    AbstractDocumentBackend,
    DeclarativeDocumentBackend,
)
from docling.document_converter import FormatOption
from docling.pipeline.simple_pipeline import SimplePipeline

from docling_go_cad.api import build_cad_document
from docling_go_cad.runner import (
    CadCliError,
    CadCliRunner,
    is_dwg_header,
    is_dxf_ascii_header,
    is_dxf_binary_header,
)

__all__ = ["CadFormatOption", "CadDocumentBackend"]

_log = logging.getLogger(__name__)

# is_valid 时读取的文件头长度，需覆盖 DWG 魔数与 DXF 文本特征判断
_HEAD_SIZE = 4096


class CadDocumentBackend(DeclarativeDocumentBackend):
    """基于 caddocling CLI 的 CAD 声明式后端。

    ``__init__`` 阶段即调用 caddocling 解析源文件并缓存 manifest，
    ``is_valid`` 依据缓存的解析结果与文件头特征判真伪，
    ``convert`` 把 manifest 组装为 DoclingDocument。
    """

    def __init__(
        self,
        in_doc: object,
        path_or_stream: Union[BytesIO, Path],
        options: Optional[object] = None,
    ) -> None:
        """初始化后端并完成一次 caddocling 转换。

        CLI 失败不在此处抛出，而是记录到 ``self._error`` 交给
        ``is_valid`` 返回 False，让 docling 以 BACKEND_FAILURE 拒绝
        该文档而不是中断整个批处理。
        """
        super().__init__(in_doc, path_or_stream, options)
        self._source_path = self._resolve_source(path_or_stream)
        self._error: Optional[CadCliError] = None
        self._manifest: Optional[dict] = None
        self._outdir: Optional[Path] = None
        self._temp_source: Optional[Path] = None
        try:
            conversion = CadCliRunner().convert(self._source_path)
        except CadCliError as exc:
            self._error = exc
            _log.warning(
                "caddocling 解析失败 (%s): %s", self._source_path, exc
            )
        else:
            self._manifest = conversion.manifest
            self._outdir = conversion.outdir

    def _resolve_source(self, path_or_stream: Union[BytesIO, Path]) -> Path:
        """把输入归一化为磁盘路径供 CLI 使用。

        文件路径直接复用；字节流（DocumentConverter 的 stream 入口）
        写入带正确后缀的临时文件，避免 caddocling 依赖扩展名时失效。
        """
        if isinstance(path_or_stream, Path):
            return path_or_stream
        suffix = Path(str(getattr(self, "file", ""))).suffix or ".dxf"
        tmp = tempfile.NamedTemporaryFile(
            prefix="docling-go-cad-", suffix=suffix, delete=False
        )
        with tmp:
            tmp.write(path_or_stream.getvalue())
        path = Path(tmp.name)
        self._temp_source = path
        return path

    def _read_head(self) -> bytes:
        """读取源文件头部字节用于真伪判定。"""
        path_or_stream = self.path_or_stream
        if isinstance(path_or_stream, Path):
            with path_or_stream.open("rb") as fh:
                return fh.read(_HEAD_SIZE)
        if isinstance(path_or_stream, BytesIO):
            path_or_stream.seek(0)
            head = path_or_stream.read(_HEAD_SIZE)
            path_or_stream.seek(0)
            return head
        return b""

    def is_valid(self) -> bool:
        """按扩展名加内容特征判定输入是否为真实 CAD 文件。

        CLI 已失败时直接判伪；否则要求扩展名与文件头特征互相印证：
        .dwg 看 AC10xx 魔数，.dxf 看 ASCII 文本特征（同时兼容二进制 DXF），
        .dxfb 看二进制哨兵串。
        """
        if self._error is not None:
            return False
        head = self._read_head()
        suffix = Path(str(self.file)).suffix.lower()
        if suffix == ".dwg":
            return is_dwg_header(head)
        if suffix == ".dxf":
            return is_dxf_ascii_header(head) or is_dxf_binary_header(head)
        if suffix == ".dxfb":
            return is_dxf_binary_header(head)
        return False

    def convert(self) -> object:
        """把缓存的 manifest 组装为 DoclingDocument。"""
        if self._manifest is None or self._outdir is None:
            raise RuntimeError(
                f"caddocling 未产出可用 manifest，无法转换 {self.file}: {self._error}"
            )
        return build_cad_document(
            self._manifest, self._outdir, name=Path(str(self.file)).stem
        )

    @classmethod
    def supports_pagination(cls) -> bool:
        """CAD 无分页概念，始终返回 False。"""
        return False

    @classmethod
    def supported_formats(cls) -> set:
        """声明支持 docling 的 CAD 输入格式（需先 register_docling）。"""
        from docling.datamodel.base_models import InputFormat

        return {InputFormat.CAD}

    def unload(self) -> None:
        """释放资源：清理 stream 入口产生的临时源文件。"""
        if self._temp_source is not None:
            self._temp_source.unlink(missing_ok=True)
            self._temp_source = None
        super().unload()


class CadFormatOption(FormatOption):
    """CAD（DWG/DXF）的 FormatOption。

    走 SimplePipeline（声明式后端直出 DoclingDocument，无需识别模型），
    后端为 :class:`CadDocumentBackend`；用它与
    ``DocumentConverter(format_options={InputFormat.CAD: CadFormatOption()})``
    完成路由注册。
    """

    pipeline_cls: Type = SimplePipeline
    backend: Type[AbstractDocumentBackend] = CadDocumentBackend
