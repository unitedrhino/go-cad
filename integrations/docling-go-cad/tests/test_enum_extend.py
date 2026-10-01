"""_extend_enum 通用枚举扩展的单测：str-mixin 枚举与纯 Enum 均可加成员、幂等。"""

from __future__ import annotations

from enum import Enum

from docling_go_cad.registry import _extend_enum


def test_extend_str_enum_adds_member():
    """给 str-mixin 枚举追加成员后，按名/按值/遍历全部生效。"""

    class Color(str, Enum):
        RED = "red"

    _extend_enum(Color, "BLUE", "blue")

    assert Color.BLUE.value == "blue"
    assert Color.BLUE.name == "BLUE"
    assert Color("blue") is Color.BLUE
    assert Color["BLUE"] is Color.BLUE
    assert Color.BLUE == "blue"  # str mixin 语义
    assert [member.name for member in Color] == ["RED", "BLUE"]
    assert "blue" in [member.value for member in Color]


def test_extend_str_enum_idempotent():
    """同名成员重复注入不报错且不产生重复。"""

    class Color(str, Enum):
        RED = "red"

    _extend_enum(Color, "BLUE", "blue")
    first = Color.BLUE
    _extend_enum(Color, "BLUE", "blue")

    assert Color.BLUE is first
    assert len(Color.__members__) == 2
    assert len(list(Color)) == 2


def test_extend_pure_enum():
    """非 str 的纯 Enum 同样可以扩展。"""

    class Level(Enum):
        LOW = 1

    _extend_enum(Level, "HIGH", 2)

    assert Level.HIGH.value == 2
    assert Level(2) is Level.HIGH
    assert Level.HIGH in list(Level)


def test_extend_value_conflict_registers_alias():
    """取值已被占用时按 stdlib 别名语义挂到现有成员。"""

    class Color(str, Enum):
        RED = "red"

    _extend_enum(Color, "CRIMSON", "red")

    assert Color.CRIMSON is Color.RED
    # 别名不应进入 canonical 成员序列
    assert [member.name for member in Color] == ["RED"]
    assert set(Color.__members__) == {"RED", "CRIMSON"}
