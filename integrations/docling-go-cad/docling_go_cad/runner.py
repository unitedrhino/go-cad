"""caddocling CLI 调用层：二进制定位、子进程调用、manifest 解析与校验。

本模块是整个包的底座，不依赖 docling / docling-core，可在仅安装
docling-core 的环境下独立使用。
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path
from typing import NamedTuple, Optional, Union

__all__ = [
    "CadCliError",
    "CadCliNotFoundError",
    "CadCliExecutionError",
    "CadManifestError",
    "CadConversion",
    "CadCliRunner",
    "find_binary",
    "is_dwg_header",
    "is_dxf_ascii_header",
    "is_dxf_binary_header",
    "BUILTIN_BIN_DIR",
]

# CLI 可执行文件名，也是 PATH 查找与包内置目录下的文件名
CLI_NAME = "caddocling"
# 包内置二进制目录：发布 wheel 时可将预编译的 caddocling 放到这里
BUILTIN_BIN_DIR = Path(__file__).parent / "bin"
# manifest 合同版本：caddocling 输出的 schema_version 必须等于该值
MANIFEST_SCHEMA_VERSION = 1
# 子进程默认超时（秒），防止 CLI 异常挂起拖死调用方
DEFAULT_TIMEOUT_SECONDS = 600


class CadCliError(Exception):
    """caddocling 调用相关错误的基类。"""


class CadCliNotFoundError(CadCliError):
    """四级回退后仍找不到 caddocling 可执行文件。"""


class CadCliExecutionError(CadCliError):
    """caddocling 以非零退出码结束或进程异常终止。"""


class CadManifestError(CadCliError):
    """stdout 中缺少 manifest 行，或 manifest 内容/文件校验失败。"""


class CadConversion(NamedTuple):
    """一次成功转换的结果：manifest 字典 + 产出目录。

    manifest 中记录的图片文件名均相对 outdir。
    """

    manifest: dict
    outdir: Path


def _install_hint() -> str:
    """未找到 caddocling 时给出的安装指引文案。"""
    return (
        f"未找到 {CLI_NAME} 可执行文件。请按以下任一方式安装：\n"
        f"  1. 从 GitHub Release 下载预编译二进制（unitedrhino/go-cad）；\n"
        f"  2. go install github.com/unitedrhino/go-cad/cmd/{CLI_NAME}@latest；\n"
        f"  3. 设置环境变量 CADCLI_BIN 指向已下载的 {CLI_NAME} 二进制。\n"
        f"安装后确保其在 PATH 中，或通过 CADCLI_BIN 指定完整路径。"
    )


def is_dwg_header(head: bytes) -> bool:
    """判断文件头字节是否为 DWG 魔数。

    DWG 文件前 6 字节是版本串（如 AC1015/AC1018/AC1032 等），
    即 ``AC10`` 前缀加两位数字版本号；本判定不覆盖 AC1.50 等
    前史前版本，如需支持请扩展此处规则（registry 中 filetype
    匹配器共用同一规则，修改时需保持同步）。
    """
    head = bytes(head)
    return len(head) >= 6 and head[:4] == b"AC10" and head[4:6].isdigit()


def is_dxf_ascii_header(head: bytes) -> bool:
    """判断文件头是否为 ASCII DXF 文本特征。

    ASCII DXF 以组码行开头（通常是 ``0`` 或注释组码 ``999``），
    且头部很快出现独立成行的 ``SECTION`` 关键字；必须按整行匹配，
    否则 ``NONSECTION`` 之类包含该词的其他文本会被误判。
    """
    head = bytes(head)
    stripped = head.lstrip()
    if not stripped:
        return False
    first_line = stripped.split(b"\n", 1)[0].strip()
    if first_line not in (b"0", b"999"):
        return False
    lines = {line.strip().upper() for line in head[:4096].split(b"\n")}
    return b"SECTION" in lines


def is_dxf_binary_header(head: bytes) -> bool:
    """判断文件头是否为二进制 DXF（DXFB）哨兵串。"""
    return bytes(head).startswith(b"AutoCAD Binary DXF")


def find_binary() -> Path:
    """按四级回退定位 caddocling 二进制。

    优先级：环境变量 ``CADCLI_BIN`` > PATH 中的 ``caddocling`` >
    包内置 ``docling_go_cad/bin/caddocling`` > 抛出
    :class:`CadCliNotFoundError`。
    """
    env_bin = os.environ.get("CADCLI_BIN")
    if env_bin:
        # 用户显式指定却无效时应立即报错，静默回退会掩盖配置错误
        path = Path(env_bin).expanduser()
        if not path.is_file():
            raise CadCliNotFoundError(
                f"环境变量 CADCLI_BIN 指向的文件不存在: {env_bin}\n{_install_hint()}"
            )
        return path

    which_hit = shutil.which(CLI_NAME)
    if which_hit:
        return Path(which_hit)

    builtin = BUILTIN_BIN_DIR / CLI_NAME
    if builtin.is_file():
        return builtin

    raise CadCliNotFoundError(_install_hint())


class CadCliRunner:
    """caddocling CLI 的调用器：负责执行转换并解析校验 manifest。"""

    def __init__(
        self,
        binary: Optional[Union[str, Path]] = None,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
    ) -> None:
        """初始化调用器。

        :param binary: 显式指定二进制路径；缺省时走 :func:`find_binary` 四级回退。
        :param timeout: 单次子进程执行超时秒数。
        """
        self._explicit_binary = None if binary is None else Path(binary)
        self._timeout = timeout

    def convert(
        self,
        path: Union[str, Path],
        outdir: Optional[Union[str, Path]] = None,
    ) -> CadConversion:
        """对输入 DWG/DXF/DXFB 执行 caddocling，返回解析校验后的 manifest。

        :param path: 输入 CAD 文件路径。
        :param outdir: 产出目录；缺省时创建一次性临时目录（由调用方负责清理）。
        :return: :class:`CadConversion`，manifest 中图片文件名相对 outdir。
        :raises CadCliNotFoundError: 找不到可执行文件。
        :raises CadCliExecutionError: CLI 非零退出、超时或被信号终止。
        :raises CadManifestError: stdout 无 manifest 行、schema 不符或文件缺失。
        """
        source = Path(path)

        # 先定位二进制：CADCLI_BIN 等显式配置错误比输入缺失更应先暴露
        binary = self._explicit_binary or find_binary()

        if not source.is_file():
            raise CadCliError(f"输入文件不存在: {source}")

        if outdir is None:
            out_dir = Path(tempfile.mkdtemp(prefix="docling-go-cad-"))
        else:
            out_dir = Path(outdir)
        out_dir.mkdir(parents=True, exist_ok=True)

        command = [str(binary), str(source), "-o", str(out_dir)]
        try:
            proc = subprocess.run(
                command,
                capture_output=True,
                text=True,
                timeout=self._timeout,
            )
        except subprocess.TimeoutExpired as exc:
            raise CadCliExecutionError(
                f"caddocling 执行超时（>{self._timeout}s）: {source}"
            ) from exc
        except OSError as exc:
            raise CadCliExecutionError(f"caddocling 无法执行: {binary}") from exc

        if proc.returncode != 0:
            raise CadCliExecutionError(
                f"caddocling 退出码 {proc.returncode}: {source}\n"
                f"stderr: {proc.stderr.strip()}"
            )

        manifest = self._parse_stdout(proc.stdout, out_dir)
        self._validate(manifest, out_dir)
        return CadConversion(manifest=manifest, outdir=out_dir)

    @staticmethod
    def _parse_stdout(stdout: str, out_dir: Path) -> dict:
        """从 stdout 末行解析 ``manifest: <绝对路径>`` 并加载 JSON。"""
        lines = [line.strip() for line in stdout.splitlines() if line.strip()]
        if not lines:
            raise CadManifestError("caddocling stdout 为空，未找到 manifest 行")
        last = lines[-1]
        prefix = "manifest:"
        if not last.startswith(prefix):
            raise CadManifestError(
                f"caddocling stdout 末行不是 manifest 行: {last!r}"
            )
        manifest_path = Path(last[len(prefix) :].strip())
        if not manifest_path.is_absolute():
            # 契约要求绝对路径；宽容处理相对路径（相对当前目录）
            manifest_path = Path.cwd() / manifest_path
        try:
            with manifest_path.open("r", encoding="utf-8") as fh:
                manifest = json.load(fh)
        except FileNotFoundError as exc:
            raise CadManifestError(f"manifest 文件不存在: {manifest_path}") from exc
        except json.JSONDecodeError as exc:
            raise CadManifestError(f"manifest 不是合法 JSON: {manifest_path}") from exc
        if not isinstance(manifest, dict):
            raise CadManifestError(f"manifest 顶层必须是 JSON 对象: {manifest_path}")
        return manifest

    @staticmethod
    def _validate(manifest: dict, out_dir: Path) -> None:
        """校验 manifest 合同：schema 版本与图片文件真实存在。"""
        version = manifest.get("schema_version")
        if version != MANIFEST_SCHEMA_VERSION:
            raise CadManifestError(
                f"manifest schema_version 不受支持: {version!r}（期望 {MANIFEST_SCHEMA_VERSION}），"
                f"请升级 caddocling 或 docling-go-cad"
            )

        sheets = manifest.get("sheets") or []
        if not isinstance(sheets, list):
            raise CadManifestError("manifest.sheets 必须是数组")

        referenced: list[str] = []
        for sheet in sheets:
            image = sheet.get("image")
            if image:
                referenced.append(image)
        full_image = manifest.get("full_image")
        if full_image:
            referenced.append(full_image)

        missing = [name for name in referenced if not (out_dir / name).is_file()]
        if missing:
            raise CadManifestError(
                f"manifest 引用的文件在产出目录中缺失: {missing}（outdir={out_dir}）"
            )
