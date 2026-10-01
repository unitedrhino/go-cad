"""docling-go-cad：让 docling 通过 caddocling CLI 支持 CAD（DWG/DXF）解析。

顶层导出：
  - :class:`CadCliRunner`：caddocling CLI 调用器（无 docling 依赖）；
  - :func:`register_docling`：向 docling 幂等注入 CAD 格式注册；
  - :func:`convert_cad`：一步转换的便捷 API（仅依赖 docling-core）；
  - :class:`CadDocumentBackend` / :class:`CadFormatOption`：docling
    后端与格式选项（依赖完整 docling，按需导入）。

仅安装 docling-core 时本包可正常导入，后两个符号在访问时才触发
docling 导入。
"""

from docling_go_cad.api import build_cad_document, convert_cad
from docling_go_cad.registry import register_docling
from docling_go_cad.runner import (
    CadCliError,
    CadCliExecutionError,
    CadCliNotFoundError,
    CadCliRunner,
    CadConversion,
    CadManifestError,
    find_binary,
)

__version__ = "0.1.0"

__all__ = [
    "CadCliError",
    "CadCliExecutionError",
    "CadCliNotFoundError",
    "CadCliRunner",
    "CadConversion",
    "CadManifestError",
    "CadDocumentBackend",
    "CadFormatOption",
    "build_cad_document",
    "convert_cad",
    "find_binary",
    "register_docling",
    "__version__",
]

# 依赖完整 docling 的符号延迟导入（PEP 562），保证仅装 docling-core 时包可导入
_DOCLING_ONLY_ATTRS = ("CadDocumentBackend", "CadFormatOption")


def __getattr__(name: str):
    if name in _DOCLING_ONLY_ATTRS:
        from docling_go_cad import backend as _backend

        return getattr(_backend, name)
    raise AttributeError(f"module 'docling_go_cad' has no attribute {name!r}")


def __dir__() -> list:
    return sorted(list(globals()) + list(_DOCLING_ONLY_ATTRS))
