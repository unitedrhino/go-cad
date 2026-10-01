# docling-go-cad

让 [docling](https://github.com/docling-project/docling) 支持 CAD（DWG/DXF）文档解析的 Python 集成包。它把 [go-cad](https://github.com/unitedrhino/go-cad) 提供的 `caddocling` CLI 封装为 docling 的 `DocumentConverter` 后端：一次调用即可完成图框智能拆图、渲染出图与文字实体提取，并组装成 docling 标准的 `DoclingDocument`。

## 简介

- 以外部 CLI `caddocling` 为解析引擎，Python 侧零 CGO 依赖；
- 注册后 `.dwg` / `.dxf` / `.dxfb` 成为 docling 的一等输入格式，与 PDF、DOCX 走同一套 `DocumentConverter` 入口；
- 每个 CAD 图框输出一个"节"：图框名作节标题、渲染图作图片项、图框内文字实体逐条转为文本项；
- 仅依赖轻量的 `docling-core`；完整 docling 作为可选依赖，按需安装。

## 安装

### 1. 安装 Python 包

```bash
# PyPI 安装（含 docling-core）
pip install docling-go-cad

# 需要 DocumentConverter 全路由时，连同完整 docling 一起安装
pip install "docling-go-cad[docling]"

# 或从源码安装
git clone https://github.com/unitedrhino/go-cad
cd go-cad/integrations/docling-go-cad
pip install -e '.[dev]'
```

### 2. 获取 caddocling CLI

任选其一：

```bash
# 方式 A：go install（需要 Go 1.25+）
go install github.com/unitedrhino/go-cad/cmd/caddocling@latest

# 方式 B：从 GitHub Release 下载预编译二进制
#   https://github.com/unitedrhino/go-cad/releases
```

二进制定位按以下顺序回退：

1. 环境变量 `CADCLI_BIN`（指向可执行文件的完整路径）；
2. `PATH` 中的 `caddocling`；
3. 包内置的 `docling_go_cad/bin/caddocling`（发布 wheel 时可预置）；
4. 均未命中时抛出 `CadCliNotFoundError`，错误信息附上述安装指引。

## 快速开始

### 方式一：DocumentConverter 全路由（需要完整 docling）

```python
from docling.document_converter import DocumentConverter
from docling.datamodel.base_models import InputFormat

from docling_go_cad import CadFormatOption, register_docling

# 1. 幂等注入：InputFormat.CAD + 扩展名/MIME 路由表 + filetype 内容嗅探
register_docling()

# 2. 注册 CAD 格式选项，之后与其他格式统一转换
converter = DocumentConverter(
    format_options={InputFormat.CAD: CadFormatOption()}
)
result = converter.convert("drawing.dwg")
print(result.status)                      # ConversionStatus.SUCCESS
print(result.document.export_to_markdown())
```

### 方式二：convert_cad 一步转换（只需 docling-core）

```python
from docling_go_cad import convert_cad

doc = convert_cad("drawing.dwg", register=False)
print(doc.export_to_markdown())

# 需要保留中间产物（拆图 PNG、manifest.json）时指定产出目录
doc = convert_cad("drawing.dwg", outdir="/tmp/cad-out")
```

### 直接使用 CLI 调用器

```python
from docling_go_cad import CadCliRunner

conversion = CadCliRunner().convert("drawing.dwg", outdir="/tmp/cad-out")
print(conversion.manifest["sheets"])   # 图框列表
print(conversion.outdir)               # 图片与 manifest.json 所在目录
```

## 工作原理

```
drawing.dwg
    │  subprocess
    ▼
caddocling CLI ──智能拆图/渲染──▶ outdir/
    │                            ├─ sheet-1.png …（各图框图）
    │                            ├─ full.png     （整图）
    │                            └─ manifest.json
    ▼
docling-go-cad 解析并校验 manifest（schema_version=1、引用文件存在）
    ▼
DoclingDocument
    ├─ sheet=0 的文字实体（图框外文本）
    ├─ 每个图框：SECTION_HEADER(图名) → PICTURE(caption=图名) → TEXT(图框内文字)
    └─ 整图 PICTURE（无图框时降级为单节兜底）
```

manifest 合同（schema_version=1）关键字段：`source`、`dwg_version`、`sheets[{index,name,image,width,pass_rate,rerenders,text_count}]`、`full_image`、`texts[{text,x,y,height,rotation,layer,sheet}]`；`sheet` 为 1-based 图框序号，`0` 表示不在任何图框内。

## 能力边界

- CAD 矢量图形以**渲染位图 + 文字实体提取**方式进入文档：图形内容在图片项中，仅 `.dwg` 内的显式文字实体（TEXT/MTEXT 等）会成为文本项；
- **没有 OCR、没有表格识别**：图片内视觉呈现的表格、标注符号不会被还原为结构化表格；
- 图框拆分继承 go-cad 的智能拆图能力（图框识别、重绘率 `pass_rate` 等见 manifest 字段），Python 侧不做二次拆分；
- DWG 版本识别以 `dwg_version` 原样透出，不保证覆盖全部历史版本。

## 降级路径说明

- **只装 docling-core**（默认依赖）：包可正常导入，`convert_cad(..., register=False)` 与 `CadCliRunner` 完全可用；
- **装完整 docling**：`register_docling()` 生效，`DocumentConverter` 可把 CAD 与其他格式统一路由；
- **找不到 caddocling**：`CadCliNotFoundError` 给出三种安装指引（GitHub Release / `go install` / `CADCLI_BIN`）；
- **CLI 失败或 manifest 不合规**：`CadCliExecutionError` / `CadManifestError` 携带 stderr 与具体校验失败项；在 `DocumentConverter` 路由下表现为该文档被拒绝（BACKEND_FAILURE），不中断批处理；
- **docling >= 2.131**：格式识别依赖 filetype 内容嗅探，注册时自动向 filetype 注入 DWG/DXF 匹配器，保证 `.dwg`/`.dxf`/`.dxfb` 被嗅探为 `image/vnd.dwg` / `image/vnd.dxf` 后命中 CAD 路由。

## 测试

```bash
# 轻量单测（仅 docling-core，无需完整 docling）
pip install -e '.[dev]' docling-core
pytest

# heavy 集成测试（需完整 docling + go 工具链，自动编译真实 caddocling）
pip install "docling-go-cad[docling]"
pytest -m heavy
```

## License

Apache-2.0
