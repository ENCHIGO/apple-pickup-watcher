# APW CLI 与 agent skill

`apw` 是独立的 Rust 命令行程序，复用桌面版的 `apw-core`，无需运行 Tauri、浏览器或音频设备。支持 macOS、Windows、Linux；发布包与桌面安装包分开。库存判定、按门店合并请求、失败退避和到货去重使用同一套核心代码。

## 构建和安装

在**源码仓库**根目录使用 stable Rust（最低版本见根目录 `Cargo.toml`）：

```bash
cargo build --release --locked -p apw-cli
./target/release/apw --version
./target/release/apw regions
```

Windows 的可执行文件是 `target/release/apw.exe`。仅构建 CLI 不需要 Node、pnpm、WebKit、GTK 或 ALSA。

在源码仓库中安装到 Cargo 的 bin 目录（通常是 `~/.cargo/bin`）：

```bash
cargo install --path crates/apw-cli --locked
```

预编译包解压后可直接运行其中的 `apw` / `apw.exe`，也可把它放到 PATH 中。包名带版本和 Rust target triple；Linux GNU 包在 Ubuntu 24.04 构建，运行环境需要兼容的 glibc。选择与操作系统及架构匹配的包。

## 安装 skill

配套 skill 可直接从本仓库安装，无需把项目另行发布到 npm。安装 Node.js LTS 后，使用 [Skills CLI](https://github.com/vercel-labs/skills)：

```bash
# 先查看仓库中可安装的 skill
npx skills add ENCHIGO/apple-pickup-watcher --list

# 全局安装到 Codex
npx skills add ENCHIGO/apple-pickup-watcher --skill apple-pickup-watcher --agent codex --global

# 或全局安装到 Claude Code
npx skills add ENCHIGO/apple-pickup-watcher --skill apple-pickup-watcher --agent claude-code --global
```

去掉 `--global` 即安装到当前项目；省略 `--agent` 由工具检测或选择 agent。需要非交互安装时追加 `--yes`，但已有同名 skill 时应先比较本地定制。Skills CLI 当前要求 Node.js 22.20.0 或更新版本；CLI 程序 `apw` 本身不依赖 Node。

安装后可查看和更新这个 skill：

```bash
npx skills list --agent codex --global
npx skills update apple-pickup-watcher --global
```

也可手动将仓库或预编译包中的 `skills/apple-pickup-watcher/` 整个目录复制到 agent 的技能目录。Codex 的默认位置是 `${CODEX_HOME:-$HOME/.codex}/skills/apple-pickup-watcher/`。已有同名 skill 时先比较内容；其他支持 `SKILL.md` 的 agent 可导入同一目录。

上述方式只安装 skill，**不会安装 `apw` 程序**。agent 还需要 Shell 工具以及 PATH 中可访问的 `apw`，可运行 `apw --version` 确认。安装后在新会话中通过 `$apple-pickup-watcher` 使用。

## 命令

所有业务命令默认输出 JSON；`watch` 逐行输出 NDJSON。无需 `--json`。`--help` 和 `--version` 是文本输出。

```bash
apw regions
apw stores --locale zh_CN --search 上海
apw products --locale zh_CN --category iphone --search '512GB'
apw products --locale zh_CN --category iphone --refresh --timeout 120
apw schema
```

地区标识来自 `regions[].locale`；门店编号来自 `stores[].number`；SKU 来自 `products[].partNumber`。品类为 `iphone`、`ipad`、`mac`、`watch`。`--search` 不区分大小写，也支持中文。目录默认使用内置快照；目录非空不代表商品有货。

`--refresh` 必须指定品类，仅在本次调用内刷新该品类。失败页保留内置数据，并以退出码 3、`refreshComplete: false` 和 `warning` 明示刷新不完整。超时以退出码 4 和 stderr 报错，不返回一个看似完整的目录。新 SKU 可以直接传入查询，不要求它存在于内置快照中。

### 单次查询

```bash
apw check --locale zh_CN --store R359 --part 'MWUC3CH/A' --timeout 60
```

门店及 SKU 仅为示例，按实际需求从目录选择。返回形状如下（示例状态不代表实时库存）：

```json
{
  "schemaVersion": 1,
  "command": "check",
  "healthy": true,
  "anyInStock": false,
  "snapshot": [{
    "target": {
      "locale": "zh_CN", "storeNumber": "R359", "storeTitle": "上海-南京东路",
      "partNumber": "MWUC3CH/A", "productName": "示例商品"
    },
    "availability": {"kind": "out_of_stock"},
    "lastCheckedMs": 1789000000000,
    "consecutiveFailures": 0
  }]
}
```

`check` 执行一轮查询后退出。`healthy` 表示所有目标都取得明确结果；`anyInStock` 表示其中至少一项确认有货。HTTP 541 等失败返回 `{"kind":"unknown","reason":"blocked","detail":"…"}`，不会转成无货；一部分目标未知时退出 3，仍保留其他目标的已知结果。

### 批量输入

重复 `--store` 与 `--part` 可查询它们的所有组合。需要跨地区或只查指定组合时，使用目标数组：

```json
[
  {"locale":"zh_CN","storeNumber":"R359","partNumber":"MWUC3CH/A"},
  {"locale":"zh_CN","storeNumber":"R683","partNumber":"MWUD3CH/A"}
]
```

```bash
apw check --targets targets.json
apw check --targets - < targets.json
```

最多 256 个输入目标、1 MiB；同一 `locale / storeNumber / partNumber` 去重。`storeTitle` 与 `productName` 可选，缺省时由目录补齐，目录找不到则显示编号。未知字段和错误的编号格式会报错。`--targets` 与 `--locale / --store / --part` 互斥。CLI 不读取或修改桌面版设置，不执行配置迁移。

### 持续监控

```bash
apw watch --targets targets.json --interval 30 --timeout 300
apw watch --targets targets.json --until-in-stock --timeout 300
```

事件封装为 `{"schemaVersion":1,"command":"watch","event":{...}}`。事件类型在 `event.type`：

| 类型 | 含义 |
| --- | --- |
| `stateChanged` | `state` 中的目标状态变化；`not_yet_checked` 表示尚未查询 |
| `inStock` | `state` 中的目标确认到货；持续有货不重复提醒 |
| `cycleComplete` | `healthy` 与完整 `snapshot`，用来对齐全部状态和检查时间 |
| `trouble` | `reason` 与可选 `advice`，说明监控故障 |
| `runStateChanged` | `running`，说明引擎启停 |

`--until-in-stock` 在任一目标到货时退出 0。默认监控 300 秒；期限届满退出 4，即使此前有健康查询，也不代表等待条件已满足。期间的库存行只是各自时间点的观察结果。默认间隔 30 秒、最低 5 秒，抖动和失败退避仍生效。限速和去重属于单个进程；避免启动多个重复的 watcher。

明确需要长期运行时可用 `--timeout 0`，由终端或进程管理器保持进程。Ctrl-C / SIGTERM 会取消在途查询。消费者关闭输出管道时退出 141。命令退出后监控结束；CLI 本身不弹窗、播放声音、发推送、打开网页或购买商品，agent 可根据用户请求处理 `inStock` 事件。

### 退出码与错误

| 退出码 | 含义 |
| --- | --- |
| 0 | 查询/目录成功，或等待条件满足；查询成功也可能是明确无货 |
| 1 | 内部错误或 I/O 失败 |
| 2 | 参数、目标文件或编号错误 |
| 3 | 查询存在未知结果，或目录刷新不完整；仍需读取 stdout |
| 4 | 整体超时，包括输入读取、请求和输出等待 |
| 130 / 143 | Ctrl-C / SIGTERM 中断 |
| 141 | 输出管道关闭 |

不可继续的错误输出到 stderr：

```json
{"schemaVersion":1,"error":{"kind":"timeout","message":"Deadline of 60 seconds exceeded","exitCode":4}}
```

超时或中断可能没有完整结果，不能推断为无货。`watch` 的最后一个事件可能不是 `runStateChanged`；以进程退出码判断命令如何结束。不要依赖中文错误文案做逻辑判断，使用状态、`reason`、`error.kind` 和退出码。

`apw schema` 提供运行版本、实际命令参数、退出码及 JSON Schema `$defs`（`targets`、`availability`、`targetState`、`check`、`watch`、`error`）。扩展字段可在同一 schemaVersion 内增加；破坏字段语义的改动必须升级 schemaVersion。

## 验证与打包

```bash
cargo test --locked -p apw-cli
cargo test --locked -p apw-core --no-default-features
cargo clippy --locked -p apw-cli --all-targets -- -D warnings
python3 scripts/package-cli.py --binary target/release/apw --target aarch64-apple-darwin
```

日常测试保持离线。CLI 测试用真实核心引擎和模拟 Fetcher 验证库存错误、部分结果、去重及取消，并用真实子进程验证接口输出与 stdin 超时。`.github/workflows/cli.yml` 独立验证四个平台、构建二进制并打包 CLI、skill、文档和许可，上传 Actions artifacts；不依赖桌面构建，也不自动发布公开 Release。
