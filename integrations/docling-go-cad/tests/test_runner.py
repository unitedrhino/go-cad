"""CadCliRunner 的单测：四级二进制定位、子进程调用与 manifest 解析校验。"""

from __future__ import annotations

import os
import shutil
from pathlib import Path

import pytest

from docling_go_cad import runner as runner_module
from docling_go_cad.runner import (
    CadCliError,
    CadCliExecutionError,
    CadCliNotFoundError,
    CadCliRunner,
    CadManifestError,
    is_dwg_header,
    is_dxf_ascii_header,
    is_dxf_binary_header,
)
from conftest import make_fake_cli


@pytest.fixture
def clean_cli_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """清空可能影响定位测试的环境变量。"""
    monkeypatch.delenv("CADCLI_BIN", raising=False)


# ---------- 四级二进制定位 ----------


def test_env_var_has_top_priority(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    input_dwg: Path,
):
    """CADCLI_BIN 指向的二进制优先于 PATH 与内置目录。"""
    primary = make_fake_cli(tmp_path / "primary", stderr="from primary")
    decoy = make_fake_cli(tmp_path / "decoy")

    monkeypatch.setenv("CADCLI_BIN", str(primary))
    monkeypatch.setenv("PATH", f"{decoy.parent}:{os.environ.get('PATH', '')}")

    result = CadCliRunner().convert(input_dwg)

    assert result.manifest["generator"] == "fake-caddocling"
    assert result.outdir.is_dir()
    # 伪造 CLI 已在 outdir 写出 manifest 与图片
    assert (result.outdir / "manifest.json").is_file()
    assert (result.outdir / "sheet-1.png").is_file()
    shutil.rmtree(result.outdir, ignore_errors=True)


def test_env_var_missing_file_is_error(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """CADCLI_BIN 指向不存在的文件时应立即报错而非静默回退。"""
    monkeypatch.setenv("CADCLI_BIN", str(tmp_path / "no-such-binary"))

    with pytest.raises(CadCliNotFoundError, match="CADCLI_BIN"):
        CadCliRunner().convert(tmp_path / "whatever.dwg")


def test_path_lookup_falls_back(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
):
    """无 CADCLI_BIN 时通过 PATH 找到 caddocling。"""
    cli = make_fake_cli(tmp_path / "bin")
    monkeypatch.setenv("PATH", f"{cli.parent}:/usr/bin:/bin")

    result = CadCliRunner().convert(input_dwg)

    assert result.manifest["dwg_version"] == "AC1015"
    shutil.rmtree(result.outdir, ignore_errors=True)


def test_builtin_lookup_falls_back(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
):
    """PATH 找不到时回退到包内置目录（测试中用 monkeypatch 指向伪造目录）。"""
    cli = make_fake_cli(tmp_path / "builtin-bin")
    monkeypatch.setattr(runner_module, "BUILTIN_BIN_DIR", cli.parent)
    monkeypatch.setenv("PATH", "/nonexistent-dir-for-test")

    result = CadCliRunner().convert(input_dwg)

    assert result.manifest["generator"] == "fake-caddocling"
    shutil.rmtree(result.outdir, ignore_errors=True)


def test_all_levels_missing_gives_install_hint(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
):
    """四级全部落空时，错误信息包含三种安装指引。"""
    monkeypatch.setattr(runner_module, "BUILTIN_BIN_DIR", tmp_path / "empty-bin")
    monkeypatch.setenv("PATH", "/nonexistent-dir-for-test")

    with pytest.raises(CadCliNotFoundError) as excinfo:
        CadCliRunner().convert(tmp_path / "whatever.dwg")

    message = str(excinfo.value)
    assert "GitHub Release" in message
    assert "go install github.com/unitedrhino/go-cad/cmd/caddocling@latest" in message
    assert "CADCLI_BIN" in message


def test_explicit_binary_argument_wins(tmp_path: Path, input_dwg: Path):
    """构造参数显式指定的二进制优先于环境变量。"""
    cli = make_fake_cli(tmp_path / "bin")
    result = CadCliRunner(binary=cli).convert(input_dwg)

    assert result.manifest["schema_version"] == 1
    shutil.rmtree(result.outdir, ignore_errors=True)


# ---------- 调用与校验 ----------


def test_nonzero_exit_raises_with_stderr(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
):
    """CLI 非零退出时抛 CadCliExecutionError 并携带 stderr。"""
    make_fake_cli(tmp_path / "bin", exit_code=3, stderr="boom: bad dwg")
    monkeypatch.setenv("CADCLI_BIN", str(tmp_path / "bin" / "caddocling"))

    with pytest.raises(CadCliExecutionError, match="boom: bad dwg") as excinfo:
        CadCliRunner().convert(input_dwg)
    assert "退出码 3" in str(excinfo.value)


def test_missing_manifest_line_raises(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
):
    """stdout 末行不是 manifest 行时报错。"""
    make_fake_cli(tmp_path / "bin", stdout_tail="all done, no manifest here")
    monkeypatch.setenv("CADCLI_BIN", str(tmp_path / "bin" / "caddocling"))

    with pytest.raises(CadManifestError, match="manifest"):
        CadCliRunner().convert(input_dwg)


def test_wrong_schema_version_raises(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
    fake_manifest: dict,
):
    """manifest schema_version 不是 1 时报错。"""
    fake_manifest["schema_version"] = 2
    make_fake_cli(tmp_path / "bin", manifest=fake_manifest)
    monkeypatch.setenv("CADCLI_BIN", str(tmp_path / "bin" / "caddocling"))

    with pytest.raises(CadManifestError, match="schema_version"):
        CadCliRunner().convert(input_dwg)


def test_referenced_file_missing_raises(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
    fake_manifest: dict,
):
    """manifest 引用的图片在产出目录缺失时报错。"""
    fake_manifest["full_image"] = "ghost.png"  # 伪造 CLI 只写它认识的文件
    make_fake_cli(tmp_path / "bin", manifest=fake_manifest)
    monkeypatch.setenv("CADCLI_BIN", str(tmp_path / "bin" / "caddocling"))

    with pytest.raises(CadManifestError, match="ghost.png"):
        CadCliRunner().convert(input_dwg)


def test_missing_input_file_raises(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """二进制就绪但输入文件不存在时直接报错，不启动子进程。"""
    cli = make_fake_cli(tmp_path / "bin")
    monkeypatch.setenv("CADCLI_BIN", str(cli))

    with pytest.raises(CadCliError, match="输入文件不存在"):
        CadCliRunner().convert(tmp_path / "no-such.dwg")


def test_default_outdir_is_tmp_and_populated(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    clean_cli_env,
    input_dwg: Path,
):
    """outdir 缺省时创建临时目录，返回值包含完整 manifest dict。"""
    cli = make_fake_cli(tmp_path / "bin")
    monkeypatch.setenv("CADCLI_BIN", str(cli))

    result = CadCliRunner().convert(input_dwg)

    assert isinstance(result.manifest, dict)
    assert result.manifest["sheets"][0]["name"] == "sheet-1"
    assert result.outdir.name.startswith("docling-go-cad-")
    assert (result.outdir / "full.png").is_file()
    shutil.rmtree(result.outdir, ignore_errors=True)


# ---------- 文件头嗅探函数 ----------


def test_header_sniffers():
    """DWG 魔数与 DXF/DXFB 特征判定的正反例。"""
    assert is_dwg_header(b"AC1015\x00...")
    assert is_dwg_header(b"AC1032xyz")
    assert not is_dwg_header(b"NOTDWG")
    assert not is_dwg_header(b"AC10")  # 长度不足
    assert not is_dwg_header(b"AC10xy")  # 版本号非数字

    dxf_head = b"  0\nSECTION\n  2\nHEADER\n  0\nENDSEC\n"
    assert is_dxf_ascii_header(dxf_head)
    assert is_dxf_ascii_header(b"999\ncomment\n  0\nSECTION\n")
    assert not is_dxf_ascii_header(b"hello world\nSECTION\n")
    assert not is_dxf_ascii_header(b"  0\nNONSECTION\n")

    assert is_dxf_binary_header(b"AutoCAD Binary DXF\r\n\x1a\x00rest")
    assert not is_dxf_binary_header(dxf_head)
