# go-cad

<p align="center">
  <a href="README.md">简体中文</a> | <a href="README_en.md">English</a>
</p>

**纯 Go 的 DWG/DXF 解析、渲染与写出库——R9～R2018 全版本、零 cgo、与 LibreDWG 逐键对齐。**

## go-cad 是什么

`go-cad` 在位级解析 Autodesk DWG 图纸（R9～R2018），支持渲染 PNG/SVG、文本提取，并能按六代容器把解析结果回写为 DWG 文件。同时支持 DXF 双向转换与 LibreDWG 风格 JSON 输入。无 cgo、无外部二进制依赖，作为独立模块可嵌入任意 Go 服务。

解码正确性以 [LibreDWG](https://github.com/LibreDWG/libredwg)（参考实现）为金标准做**逐键值级验证**：37 个语料图纸、其中 34 个 strict 硬门禁，全部 100% 对齐。实现为独立纯 Go 表达，按 LibreDWG 规格逐位对齐修正。

## 特色

- 🗂️ **全版本 DWG 读取**：R9/R10/R11（pre-R13 独立路径）+ R13/R14/R2000/R2004/R2007/R2010/R2013/R2018
- ✍️ **DWG 写出**：六代容器（R2000→R2018），回读与 LibreDWG `dwgread` 双重验证
- 🖼️ **PNG 渲染**：真实 CJK 字形引擎（旋转/镜像/对齐/宽度因子），分辨率自动保证文字清晰
- 📐 **SVG 输出**：真矢量 + AI 友好元数据（图层分组/实体类型/句柄/图纸摘要）
- 🔪 **智能拆图**（独有能力）：自动识别模型空间图框（含非标加长幅）、提取图号与图名、逐幅出图；设计说明/图例表等无框内容区自动兜底切分；整图全览固定首张
- 📝 **文本提取**：单张 4MB 图纸提取 1.4 万条文本，GBK/UTF-16 码页解码，pre-R13 码页支持
- 🔄 **DXF 双向**：ASCII + 二进制读取、23+ 实体写出，经 LibreDWG `dxf2dwg` 金标准 59/59 验证
- 📥 **JSON 输入**：直接消费 LibreDWG 风格 gold JSON（41 种实体）
- 🧱 **结构化正向写出**：JSON/DXF/内存构造的 Document 均可直接写出 DWG（等价官方 dxf2dwg / dwgwrite -I json 核心能力）
- 🛡️ **工业级可靠性**：147 个官方语料全量吞吐零 bug、千级变异模糊零 panic、出图自动清晰度保障（不达标自动提升分辨率重渲）
- 💻 **CLI 内置**：`dwg2png` 支持 DWG/DXF → PNG/SVG，含智能拆图模式；`caddocling` 输出结构化 manifest（图框+文本+图片清单），供 AI 管线与联犀 docling 消费
- 🤝 **Docling 集成**（联犀文档解析库原生支持 CAD 输入）：[unitedrhino/docling](https://github.com/unitedrhino/docling) 原生集成 go-cad，`.dwg`/`.dxf`/`.dxfb` 与 PDF/Word 等走同一条解析管线

## 多语言文本支持

工程图纸的中文支持是一等公民，不是事后补丁：

- TEXT/MTEXT/ATTRIB/ATTDEF 全实体类型的 **GBK/UTF-16 码页解码**（R13+）与 **pre-R13 码页**（R9～R11 头变量码页）
- 大字体（bigfont）场景与 `\U+XXXX` 转义处理
- 渲染使用 CJK 字形引擎，中文标注在 PNG/SVG 中清晰可读
- 真实验证：4 张中文工程图纸（含 505 个中文 TEXT 的 R2000 图纸）值级对齐 100%

## 渲染效果

以下均为 `dwg2png` 的真实渲染输出（样例取自公开语料中的中文工程图纸，未经任何人工修饰）：

<p align="center">
  <img src="docs/images/showcase-v-filters.png" width="820" alt="V 型滤池泵房工艺图渲染输出" />
</p>

V 型滤池泵房工艺图（R2018 容器）：剖面对象、彩色工艺管线、图例表格与标题栏一次成形。

<p align="center">
  <img src="docs/images/showcase-cjk.png" width="820" alt="中文文本渲染特写" />
</p>

中文标注特写（机械零件图）：技术要求逐字清晰可读，标题栏中文与表格完整还原——GBK/UTF-16 码页解码 + 内置 CJK 字形引擎，不依赖系统字体。

## 与 LibreDWG 的对比

| 能力 | LibreDWG | go-cad |
|---|---|---|
| DWG 读取 | R13~R2018 + 部分 pre-R13 | **R9~R2018 全版本**（pre-R13 逐类型计数验证）|
| 值级正确性 | ——（它自己就是基准）| **37 个语料图纸 100%**（62 行 strict 门禁）|
| DWG 写出 | R13~R2018 | **R2000~R2018** 六代容器（`dwgread` 交叉验证）|
| DXF 读写 | ✅ | ✅（金标准 59/59）|
| JSON 输入输出 | ✅ | ✅ 输入（41 种实体）/ 结构化导出 |
| 渲染 | ⚠️ 仅调试级 dwg2SVG | ✅ PNG 字形渲染 + AI 友好 SVG + 智能拆图 |
| 中文图纸 | 部分支持 | ✅ 一等公民（码页/大字体/CJK 渲染全链路）|
| 语言与许可 | C（GPL）| **Go（Apache-2.0）**——可嵌入，无许可证传染 |

### 做不到的（诚实边界）

- **ACIS 几何内核**（3DSOLID/REGION 的 SAT/SAB 重建）——LibreDWG 也只存 blob，无参考可验证
- **pre-R13 写出**——LibreDWG 的 encode 同样不支持
- **R2.x～R8 古董格式**——超出 LibreDWG 支持范围
- 少数 spec 分支经两轮全网搜索**无真实样本**（光度 LIGHT、pre-R13 中文）——已按 spec 实现+合成自证
- 约 5% 的 DumpEntities 长尾表记录键（进行中，已验证键 15923/15923）

## 支持矩阵

| 格式 | 读取 | 写出 |
|---|---|---|
| DWG R9/R10/R11（AC1004/06/09） | ✅ | ➖（LibreDWG 亦不支持）|
| DWG R13/R14（AC1012/14） | ✅ | ✅（R2000 布局）|
| DWG R2000～R2018（AC1015~AC1032） | ✅ | ✅ 六代容器 |
| DXF ASCII / 二进制（R12~R2018） | ✅ | ✅ |
| JSON（LibreDWG gold 风格） | ✅ | ✅ 实体导出 |

## 快速开始

### 1. 安装

```bash
go get github.com/unitedrhino/go-cad
```

### 2. 解析、渲染、写出

```go
package main

import (
    "bytes"
    "os"

    "github.com/unitedrhino/go-cad"
)

func main() {
    data, _ := os.ReadFile("drawing.dwg")

    doc, err := cad.Parse(data) // 也有 ParseDXF、ParseJSON
    if err != nil { panic(err) }

    texts := doc.Texts() // 4MB 图纸提取 1.4 万条文本

    png, err := cad.RenderPNG(doc, cad.RenderOptions{Width: 4096})

    var buf bytes.Buffer
    err = cad.WriteDwg(doc, &buf) // 六代容器写出，按版本自动分派

    svg, err := cad.RenderSVG(doc, cad.RenderOptions{Width: 4096})
    _ = png; _ = svg
}
```

### 3. CLI

```bash
go run ./cmd/dwg2png drawing.dwg -o out.png            # PNG（2048）
go run ./cmd/dwg2png drawing.dwg -o out.svg            # SVG（矢量，无限缩放）
go run ./cmd/dwg2png drawing.dwg -sheets -o s.png      # 智能拆图：
                                                       # 按图框逐张出图（宽度自适应），
                                                       # 无框内容区（设计说明/图例表）兜底切分，
                                                       # 首张固定整图全览
go run ./cmd/caddocling drawing.dwg -o out             # 结构化 manifest（图框+文本+图片清单），
                                                       # 供 AI 管线与联犀 docling 消费
```

## Example 一键演示

```bash
go run ./example
```

零参数即跑全链路（解析→文本→渲染→DWG 回写→再解析校验→DXF 双向→JSON 导出），产物 PNG/DWG/DXF 落当前目录；`-f` 指定任意样本、`-o` 改输出目录、`-width` 改渲染宽度。源码见 [example/main.go](example/main.go)，6 步每步都是库公开 API 的最小用法。

## Docling 集成（联犀文档解析）

同组织的纯 Go 文档解析库 [unitedrhino/docling](https://github.com/unitedrhino/docling)（PDF/Word/PPT/Excel/HTML/Markdown/EML/图片/CAD 图纸等 13 种格式统一解析为 Docling 协议，联犀物联网平台知识库同款引擎）已原生集成 go-cad：`.dwg`/`.dxf`/`.dxfb` 可直接作为输入格式——**每张图框一页**（图名标题 + 渲染图 + 图纸文本），与 PDF/Word 等文档一起进入统一的 Markdown/JSON/content_list 输出与切片管线。

```go
data, _ := os.ReadFile("图纸.dwg")

doc, err := docling.ParseByExt("图纸.dwg", data) // 按扩展名自动分派，CAD 每图框一页
if err != nil { panic(err) }

md := doc.ToMarkdown() // 图名标题 + 图纸文本，与 PDF/Word 同一协议
```

完整能力（其他输入格式、content_list、RAG 分块、大模型增强钩子）见 [unitedrhino/docling](https://github.com/unitedrhino/docling)。

## 真实案例验证

每个版本都在真实生产图纸上验证，而非仅合成数据：

- **4.3MB 智能化深化设计图**（108,850 对象）：250 万实体键值与 LibreDWG gold 对照 **100%**；8192px 渲染中文标注清晰可读；回写后无损再解析
- **消防/弱电施工图**：9+9 张图框智能拆分，图号与图名（如 `RD(XF)-05-地下一层消防平面图`）从标题栏自动提取；彩色线型图例表 16 行完整还原
- **室外总平面图**：离群实体修复，渲染内容完整可见
- **147 个 LibreDWG 官方语料**：全消费链路（解析→渲染→写出→再解析），以 `dwgread` 仲裁——0 bug
- **模糊测试**：1000+ 变异体 + 随机输入，零 panic（开发期发现并修复 4 个）

## 质量保证

| 维度 | 门禁 | 现状 |
|---|---|---|
| 值级对齐 | 37 图纸 × 对象/实体双侧，对标 `dwgread -O JSON` 逐键比对 | **全部 strict 行 100%** |
| Round-trip | 对象+实体回放重解码 | fail=0 |
| DWG 写出 | 六代容器回读不变量 + `dwgread` 交叉验证 | 全 PASS |
| DXF | 同源对照 + `dxf2dwg` 金标准 | 59/59 |
| JSON 输入 | gold → Document → 与 DWG 来源对照 | 9 图纸 0 差异 |
| 可靠性 | 147 语料全量 + 变异模糊 | 0 bug / 0 panic |
| 覆盖率 | 语句覆盖 | 83.5% |

外部对照资源（LibreDWG gold JSON）经环境变量门控；内置 testdata 套件在任何环境完整运行。

## 性能

纯解析吞吐为 LibreDWG `dwgread`（C 实现，全链路口径）的 **2.3～6.4 倍**：

| 样本 | go-cad | dwgread（净）|
|---|---|---|
| 117 KB | 32.9 MB/s | 5.1 MB/s |
| 8.2 MB | 9.1 MB/s | 3.4 MB/s |

4.3MB / 10.8 万对象图纸：解析 691ms、PNG@2048 4.1s、DWG 回写 785ms、26 张 SVG 拆图数秒。

## 包结构

| 路径 | 职责 |
|---|---|
| `cad.go` | 公开门面：类型别名 + API 转发（Parse/Document/渲染/写出），外部使用方式零变化 |
| `internal/bitstream/` | DWG 位流原语（读写对称，最底层，无内部依赖）|
| `internal/container/` | 容器层（R2000 段目录 / R2004 页式 / R2007 RS 交织）+ LZ77/R21 压缩器 + 原始素材捕获（回放写出用）|
| `internal/objrec/` | 对象记录层：对象图/对象记录/类型码命名（实体与对象解码共用的最底层记录原语）|
| `internal/entity/` | 实体解码（公共头扫描框架 + 按族拆分）+ 图元离散/调色板等实体侧共用件 |
| `internal/object/` | 内部对象解码（spec 驱动 gfRead 框架）|
| `internal/drawing/` | 文档模型与解码编排（Document/Parse）+ pre-R13 路径 + DXF 双向 + JSON 输入 + 图元离散（tessellate）与包围盒 |
| `internal/render/` | 渲染（PNG 字形 / SVG 矢量 / 图纸拆分）|
| `internal/writer/` | DWG 写出（位级回放 + 结构化正向）|
| `internal/testsupport/` | 测试支撑包：测试位流构造器 / gold 路径定位 / 比较助手（无业务依赖，供各子包测试导入）|
| `example/` / `cmd/dwg2png/` | 一键演示与 CLI（仅依赖门面）|
| `cmd/caddocling/` | 结构化 manifest CLI：manifest.json（图框/文本/图片清单）+ 拆图出图，供联犀 docling 等 AI 管线消费 |
| `testdata/` | 官方语料样本（测试内置，CI 完整运行）|

依赖方向单向：`bitstream → objrec → container → entity/object → drawing → render/writer`，
全部实现位于 `internal/`，外部仅经 `cad.go` 门面消费。

## 已知限制

见[与 LibreDWG 的对比](#与-libredwg-的对比)——简版：ACIS 几何内核（仅存 blob，同官方）、pre-R13 写出（官方同样不支持）、R2.x～R8 古董格式、极少数无真实样本的 spec 分支（合成自证）、约 5% 长尾表记录键。

## 参考

- 值级对齐金标准：[LibreDWG](https://github.com/LibreDWG/libredwg) `dwgread -O JSON` 与 `dxf2dwg`

## 参与

PR 欢迎提交，`go test ./...` 必须通过——套件内置值级对齐门禁，保证解码始终诚实。

## 许可

Apache-2.0，详见 [LICENSE](LICENSE)。
