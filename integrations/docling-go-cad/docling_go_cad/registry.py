"""register_docling：把 CAD（DWG/DXF/DXFB）格式幂等注入 docling 的格式注册表。

docling 通过 ``InputFormat`` 枚举与 ``FormatToExtensions`` /
``FormatToMimeType`` / ``MimeTypeToFormat`` 三张表驱动格式路由；
docling 原生不含 CAD，本模块在运行期动态扩展上述结构，
使 ``DocumentConverter`` 能把 .dwg/.dxf/.dxfb 路由到
``CadFormatOption`` 声明的后端。

docling >= 2.131 的格式识别以 ``filetype`` 库内容嗅探为主，
因此这里同时向 filetype 注册 DWG/DXF 匹配器，保证嗅探返回
``image/vnd.dwg`` / ``image/vnd.dxf`` 后能命中 CAD 表项。
"""

from __future__ import annotations

from typing import Any

__all__ = ["register_docling", "CAD_EXTENSIONS", "CAD_MIME_TYPES"]

# CAD 支持的扩展名与 MIME，均与 manifest 合同保持一致
CAD_EXTENSIONS = ["dwg", "dxf", "dxfb"]
CAD_MIME_TYPES = ["image/vnd.dwg", "image/vnd.dxf"]

# 注入到 InputFormat 的成员名与取值
_CAD_MEMBER_NAME = "CAD"
_CAD_VALUE = "cad"

_DOCILING_INSTALL_HINT = (
    "register_docling() 需要完整 docling（仅装 docling-core 不够）。"
    "请先安装：pip install 'docling-go-cad[docling]' 或 pip install 'docling>=2'"
)


def _extend_enum(enum_cls: type, name: str, value: Any) -> None:
    """给 stdlib ``Enum``（含 ``str`` mixin 枚举）动态追加成员，幂等。

    标准库不提供运行期加成员的公开 API，这里复刻枚举成员创建的
    内部流程（``_member_type_.__new__`` + ``_name_``/``_value_``），
    再补齐三张成员表 ``_member_map_`` / ``_member_names_`` /
    ``_value2member_map_``；同时必须把成员绑定为真正的类属性——
    Python 3.12 的 ``EnumType`` 已不提供 ``__getattr__`` 回退，
    ``cls.NAME`` 只认创建期 ``setattr`` 进去的类属性。

    :param enum_cls: 目标枚举类。
    :param name: 新成员名；已存在同名成员时直接复用，不重复注入。
    :param value: 新成员取值；取值已被其他成员占用时按别名语义挂到现有成员上。
    """
    if name in enum_cls._member_map_:
        return
    existing = enum_cls._value2member_map_.get(value)
    if existing is not None:
        # 取值冲突：注册为别名（与 stdlib 别名一致，不进入 _member_names_）
        setattr(enum_cls, name, existing)
        enum_cls._member_map_[name] = existing
        return

    # 复刻 stdlib 成员创建：mixin 类型（如 str/int）负责携带值构造实例，
    # 纯 Enum 的 object.__new__ 不接受值参数
    mixin = getattr(enum_cls, "_member_type_", object)
    if mixin is object:
        member = mixin.__new__(enum_cls)
    else:
        member = mixin.__new__(enum_cls, value)
    member._name_ = name
    member._value_ = value
    member.__objclass__ = enum_cls
    # _sort_order_ 仅 3.11+ 使用，低版本多设一个属性无副作用
    member._sort_order_ = len(enum_cls._member_names_)

    # 类属性绑定必须在 _member_map_ 注入之前：EnumType.__setattr__
    # 会把"名字已在 _member_map_ 中"的 setattr 判为重赋成员而拒绝
    setattr(enum_cls, name, member)
    enum_cls._member_names_.append(name)
    enum_cls._member_map_[name] = member
    enum_cls._value2member_map_[value] = member


def _register_filetype_matchers() -> None:
    """向 filetype 库注册 DWG/DXF/DXFB 内容匹配器（存在才注册，幂等）。

    docling >= 2.131 依赖 filetype 做格式嗅探，未注册时 .dwg/.dxf
    会被兜底判为 text/plain，导致 CAD 路由永远无法命中；filetype
    是 docling 的依赖，这里缺失时静默跳过（老版 docling 走扩展名路由，
    用不到嗅探）。
    """
    try:
        import filetype
    except ImportError:
        return

    # 幂等标记挂在 filetype 包对象上，避免重复 insert 匹配器
    if getattr(filetype, "_docling_go_cad_registered", False):
        return

    class DwgFileType(filetype.Type):
        """DWG 魔数匹配器：前 6 字节为 AC10xx 版本串（与 runner.is_dwg_header 同规则）。"""

        MIME = "image/vnd.dwg"
        EXTENSION = "dwg"

        def __init__(self) -> None:
            super().__init__(self.MIME, self.EXTENSION)

        def match(self, buf: bytes) -> bool:
            from docling_go_cad.runner import is_dwg_header

            return is_dwg_header(buf)

    class DxfFileType(filetype.Type):
        """ASCII DXF 文本特征匹配器（与 runner.is_dxf_ascii_header 同规则）。"""

        MIME = "image/vnd.dxf"
        EXTENSION = "dxf"

        def __init__(self) -> None:
            super().__init__(self.MIME, self.EXTENSION)

        def match(self, buf: bytes) -> bool:
            from docling_go_cad.runner import is_dxf_ascii_header

            return is_dxf_ascii_header(buf)

    class DxfBinaryFileType(filetype.Type):
        """二进制 DXF（.dxfb）哨兵串匹配器。"""

        MIME = "image/vnd.dxf"
        EXTENSION = "dxfb"

        def __init__(self) -> None:
            super().__init__(self.MIME, self.EXTENSION)

        def match(self, buf: bytes) -> bool:
            from docling_go_cad.runner import is_dxf_binary_header

            return is_dxf_binary_header(buf)

    # add_type 会插到匹配列表头部，优先于内置匹配器
    filetype.add_type(DxfBinaryFileType())
    filetype.add_type(DxfFileType())
    filetype.add_type(DwgFileType())
    filetype._docling_go_cad_registered = True


def register_docling() -> None:
    """向 docling 注册 CAD 输入格式（幂等，可重复调用）。

    完成四件事：
      1. 给 ``docling.datamodel.base_models.InputFormat`` 动态追加成员
         ``CAD = "cad"``；
      2. 注入 ``FormatToExtensions[CAD]`` / ``FormatToMimeType[CAD]``；
      3. 对 ``MimeTypeToFormat`` 中的 ``image/vnd.dwg`` /
         ``image/vnd.dxf`` 追加 CAD（该表是组合结构，追加而非覆盖，
         避免丢掉同 MIME 下的其他格式）；
      4. 向 filetype 注册内容匹配器，保证 docling>=2.131 的嗅探路由生效。

    :raises ImportError: docling 未安装时抛出，并附安装指引。
    """
    try:
        from docling.datamodel.base_models import (
            FormatToExtensions,
            FormatToMimeType,
            InputFormat,
            MimeTypeToFormat,
        )
    except ImportError as exc:
        raise ImportError(_DOCILING_INSTALL_HINT) from exc

    _extend_enum(InputFormat, _CAD_MEMBER_NAME, _CAD_VALUE)
    cad = InputFormat.CAD

    # 三张表均为模块级可变 dict，直接写入即可被路由逻辑读到
    FormatToExtensions[cad] = list(CAD_EXTENSIONS)
    FormatToMimeType[cad] = list(CAD_MIME_TYPES)
    for mime in CAD_MIME_TYPES:
        holders = MimeTypeToFormat.setdefault(mime, [])
        if cad not in holders:
            holders.append(cad)

    _register_filetype_matchers()
