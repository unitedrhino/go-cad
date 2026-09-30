# cad — 纯 Go 的 DWG/DXF 解析、渲染与写出库

[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8)](https://go.dev/)
[![License Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](#license)
[![No cgo](https://img.shields.io/badge/cgo-free-brightgreen)](#what-is-cad)
[![Coverage](https://img.shields.io/badge/coverage-83%25-yellowgreen)](#质量保证)

## What is cad ?

`backend/share/cad` 是一个**纯 Go** 实现的 AutoCAD 图纸处理库：对 DWG（R9～R2018 十一个版本代际）做位级解析，支持渲染 PNG、文本提取、DXF 读写、JSON 输入，以及把解析结果回写为六代 DWG 文件。无 cgo、无外部二进制依赖，可嵌入任意 Go 服务（知识库 docparse 等）直接使用。

解码正确性以 [LibreDWG](https://github.com/LibreDWG/libredwg) 官方实现为金标准做**值级对齐验证**（27 个样本 × 对象/实体双侧 100% 硬门禁），字段排列歧义以「候选布局枚举 + 合理性评分」消解，位流语义按 LibreDWG 规格逐位对齐修正扩展。

## Features

- 🗂️ **全版本 DWG 读取**：R9/R10/R11（pre-R13 独立解码路径）+ R13/R14/R2000/R2004/R2007/R2010/R2013/R2018
- 🧬 **统一文档模型**：`Document` 承载模型空间/块/图层/内部对象，供全部消费 API 共享
- ✍️ **DWG 写出**：`WriteDwg` 按版本自动分派 R2000/R2004/R2007/R2010/R2013/R2018 六代容器（回放式，读回与 LibreDWG `dwgread` 双重验证）
- 🖼️ **渲染出图**：`RenderPNG` 视口自适应渲染模型空间（线宽/颜色/文本/块递归展开），像素级金标测试防退化
- 📐 **智能拆图**（独有能力）：自动识别模型空间图框（含非标加长幅）、提取图名、逐幅出图 PNG/SVG——LibreDWG 无此能力
- 🤖 **AI 友好 SVG**：`RenderSVG` 矢量输出带语义元数据——图层分组 id（`<g id="layer:…">`）、文本类型/句柄（`data-type`/`data-h`）、图纸摘要 `<metadata>`（版本/图元数/图框名/图层清单），SVG 供 AI 直接解析语义（元数据默认开启、体积增幅 <15%，浏览器不渲染 metadata/属性，人类查看视觉零影响）
- 📝 **文本提取**：`Texts()` 提取模型空间与块内属性文本（GBK/UTF-16 码页解码，pre-R13 码页支持）
- 🔄 **DXF 双向**：`ParseDXF`（ASCII+二进制）/ `WriteDXF`（ASCII，23 类实体），与同源 DWG 及 LibreDWG `dxf2dwg` 金标准对照
- 📥 **JSON 输入**：`ParseJSON` 直接消费 LibreDWG 风格 gold JSON（41 种实体全覆盖，与 DWG 输入殊途同归）
- 🔍 **内部对象全景**：DICTIONARY/XRECORD/动态块/ASSOC 约束/渲染设置/ACSH 等数十类内部对象值级解析
- 🛡️ **工业级可靠性**：141 个官方样本全量吞吐 + 千级变异模糊测试零 panic
- 💻 **开箱 CLI**：`dwg2png` 命令行工具（支持 .dwg/.dxf/.dxfb）

### 支持矩阵

| 格式/版本 | 读取 | 写出 |
|---|---|---|
| DWG R9/R10/R11（AC1004/06/09） | ✅ | ➖（LibreDWG 亦不支持） |
| DWG R13/R14（AC1012/14） | ✅ | ✅（R2000 布局） |
| DWG R2000～R2018（AC1015~AC1032） | ✅ | ✅（六代容器） |
| DXF ASCII（R12~R2018） | ✅ 主力实体 | ✅ 23 类实体 |
| DXF 二进制 | ✅ | ➖ |
| JSON（LibreDWG gold 风格） | ✅ | ➖（DumpEntities 输出） |

## Quickstart

### 1. 安装

Go workspace 内直接引用（本包位于 `backend/share` 模块）：

```go
import "gitee.com/unitedrhino/share/cad"
```

### 2. CLI 转图

```bash
go run ./backend/cmd/dwg2png 图纸.dwg -o out.png -width 2048
# 同样支持 .dxf / .dxfb

# 图框切分逐张出图（-sheets：识别图框 → 按幅输出多文件，格式随 -o 扩展名）
go run ./backend/cmd/dwg2png 图纸.dwg -o sheet.png -sheets
go run ./backend/cmd/dwg2png 图纸.dwg -o sheet.svg -sheets   # SVG 含 AI 语义元数据
```

### 3. 库用法

```go
data, _ := os.ReadFile("drawing.dwg")

// 解析（DWG/DXF/JSON 三个入口）
doc, err := cad.Parse(data)        // 或 cad.ParseDXF / cad.ParseJSON
if err != nil { log.Fatal(err) }

// 文本提取（知识库场景）
for _, t := range doc.Texts() {
    fmt.Println(t.Handle, t.Text)
}

// 渲染 PNG
png, err := cad.RenderPNG(doc, cad.RenderOptions{Width: 2048})

// 回写 DWG（按文档版本自动分派六代容器）
var buf bytes.Buffer
err = cad.WriteDwg(doc, &buf)

// 结构导出（实体对照 JSON）
js, err := cad.DumpEntities(data)
```

## 质量保证

全部为可复现的测试门禁（`go test ./share/cad`，429+ 测试函数，覆盖率 83.5%）：

| 维度 | 门禁 | 现状 |
|---|---|---|
| 值级对齐 | 37 样本语料（34 项 strict 硬门禁）× 对象/实体双侧，对标 `dwgread -O JSON` 逐键比对 | **全 strict 行 100%（缺失 0/不符 0）**，gold 历史噪声逐条实证豁免 |
| Round-trip | 对象+实体回放重解码比对 | 五样本 fail=0 |
| 文件写出 | 六代容器回读不变量 + LibreDWG `dwgread` 交叉验证 | 全 PASS（error 行与源基线一致） |
| DXF | 同源 DWG 对照 + `dxf2dwg` 金标准 roundtrip | 58/58 全 PASS |
| JSON 输入 | gold JSON → Document → 与 DWG 输入对照 | 九样本键值差异 0 |
| 可靠性 | 147 官方样本全量吞吐（dwgread 仲裁）+ 千级变异/随机模糊 | 0 bug / 零 panic |
| API e2e | 22 个导出 API × 8 版本矩阵 + nil/空文档安全 | 全 PASS |
| 静态 | go vet / gofmt / 语句覆盖率 | 零告警 / 83.5% |

## 架构概览

| 文件 | 职责 |
|---|---|
| `bitreader.go` / `bitwriter.go` | DWG 位流原语（读写对称） |
| `container*.go` / `r21.go` / `lz77.go` / `compress.go` | 容器层（R2000 段目录 / R2004 页式压缩 / R2007 RS 交织）与压缩器 |
| `entities*.go` | 实体解码（公共头扫描框架 + 按族拆分：dimension/hatch/more/light/mleader/image/mpolygon） |
| `objects_*.go` | 内部对象解码（gfRead spec 驱动框架 + 按族拆分） |
| `r11.go` | pre-R13（R9/R10/R11）独立解码路径 |
| `encode_file.go` / `encode_entity.go` / `encode_object.go` | DWG 写出（三代容器 + 实体/对象回放） |
| `dxf_read.go` / `dxf_write.go` / `injson.go` | DXF 双向与 JSON 输入 |
| `render.go` | PNG 渲染（细分/视口/颜色） |
| `entity_audit.go` | 值级审计字段导出器（gold 键驱动） |

## 与 LibreDWG 的完成度

以 [LibreDWG](https://github.com/LibreDWG/libredwg)（官方参考实现）能力面为基准：

| 能力域 | 完成度 | 说明 |
|---|---|---|
| DWG 读取 | **~100%** | R9~R2018 全版本；spec 全部 53 实体类 + 25 对象类；31 样本值级门禁 100% |
| DWG 写出 | **100%** | R2000~R2018 六代容器全覆盖（官方支持面即 R13+） |
| DXF 读取 | **~95%** | ASCII+二进制全实体（含 ACIS SAT/IMAGE/HATCH 渐变/PFACE/MESH）；SAB 二进制转换器为硬边界 |
| DXF 写出 | **~95%** | 全实体类经官方 `dxf2dwg` 金标准 59/59 验证 |
| JSON 输入/输出 | **~95%** | 41 种实体含 ACIS 二进制段/嵌套结构；DumpEntities 键覆盖 15923/15923（豁免仅剩审计同口径排除类） |
| 渲染 PNG | 100%（超出官方） | 官方无渲染能力；01-1/01-2 值级 100% |
| 结构化正向写出 | 100%（等价官方 dxf2dwg/dwgwrite -I json 核心能力） | 任意来源（JSON/DXF/内存构造）Document 直写 R2000 DWG，dwgread 交叉验证 |

剩余边界均非能力问题：ACIS 几何内核（官方也不解析）、pre-R13 写出（官方不支持）、R2.x~R8 古董格式（官方不支持）、ARX 动态类开放集（UNKNOWN 兜底即官方口径）、LIGHT 光度分支等极少数场景（全网无真实样本，合成自证）。

## 性能

与 LibreDWG `dwgread`（C 实现，含 JSON 序列化的全链路口径）实测对比——本库纯解析吞吐为其 **2.3~6.4 倍**：

| 样本档 | 本库吞吐 | dwgread 净吞吐 |
|---|---|---|
| 117 KB | 32.9 MB/s | 5.1 MB/s |
| 147 KB | 10.8 MB/s | 3.6 MB/s |
| 8.2 MB | 9.1 MB/s | 3.4 MB/s |
| 2.2 MB | 4.9 MB/s | 2.1 MB/s |

热点优化后的基准（Xeon Gold 6226R / go1.26，完整数据见 `bench_corpus_test.go`）：

| 操作 | 典型耗时 |
|---|---|
| Parse 8.2MB 中文图纸 | ~492 ms（优化前 854 ms） |
| Parse 117KB | 3.4 ms |
| RenderPNG 2048 宽 | 92~237 ms |
| WriteDwg | 1~8 ms（回放式，毫秒级零压力） |

## Example 一键演示

```bash
cd backend/share && go run ./cad/example
```

零参数即跑全链路（解析→文本→渲染→DWG 回写→再解析校验→DXF 双向→JSON 导出），产物 PNG/DWG/DXF 落当前目录；`-f` 指定任意样本、`-o` 改输出目录、`-width` 改渲染宽度。源码见 [example/main.go](example/main.go)，6 步每步都是库公开 API 的最小用法。

## 已知限制

- **ACIS 几何内核**（3DSOLID/REGION 的 SAT/SAB 几何）：只解到句柄/元数据级，几何 blob 不重建——LibreDWG 同样不解析，无验证途径
- **pre-R13 写出**：不支持（LibreDWG 官方 encode 亦不支持）
- DXF 写出的图层/块名在 DWG 来源下为句柄合成名（模型未保留符号表名）
- 测试外部对照资源（gold JSON/dwgread）经环境变量门控，无工具链环境时相关门禁自动跳过（包内合成测试仍全量执行）

## References

- 值级对齐金标准：[LibreDWG](https://github.com/LibreDWG/libredwg)（`dwgread -O JSON` 与 `dxf2dwg`）

## License

本包遵循仓库统一许可（Apache-2.0）。
