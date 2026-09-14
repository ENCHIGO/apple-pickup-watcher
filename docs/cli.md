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

`companionPart` 可选，只对 Apple Watch 有意义：Apple 的取货接口只把「表壳 + 表带」当作可售组合，单独查表壳零件号会得到空响应或假的「不支持取货」。`apw products` 返回的 Apple Watch 商品带有 `companionPart`（同购买页的一条表带零件号）；`check` / `watch` 会把它附在同一请求里，状态仍取表壳自己的结果。缺省时按 `partNumber` 回查内置目录补齐，目录里没有的零件号不会补。输出的 `target` 只在有值时包含该字段。

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
| `cycleComplete` | `healthy` 与完整 `snapshot`，用来对齐全部状态和检查时间；`nextCheckInSecs` 是距下一轮的秒数，`paced` 为 true 表示这段等待是请求预算拉长的，不是 `--interval` |
| `trouble` | `reason` 与可选 `advice`，说明监控故障 |
| `runStateChanged` | `running`，说明引擎启停 |

`--until-in-stock` 在任一目标到货时退出 0。默认监控 300 秒；期限届满退出 4，即使此前有健康查询，也不代表等待条件已满足。期间的库存行只是各自时间点的观察结果。默认间隔 30 秒、最低 5 秒，抖动和失败退避仍生效；请求预算不够时实际间隔会更长（见下文「请求预算与合并查询」）。限速、预算和去重都属于单个进程；避免启动多个重复的 watcher，也不要用循环里的 `check` 代替 `watch`——每次 `check` 都是一个新进程、两次请求，预算记不住。

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

## 请求预算与合并查询

Apple 的边缘节点按请求**次数**限制取货接口（2026-09-14 实测，见 issue #3）：不间断发到约 30 次就返回 HTTP 541，之后十几分钟内怎么发都是 541，停下来额度才慢慢恢复。计数挂在会话 / 出口 IP 上——被烧掉的会话一直被拦，同一浏览器换个不带 cookie 的请求却能过（浏览器带 cookie 541、不带 cookie 200，稳定复现）。请求头和 TLS 指纹在阈值附近两个方向都测过，都没能稳定改变结果；真正有效的是少发请求。所以从 v0.4.2 起客户端做两件事：

- **合并查询**：同一地区、同一地点的门店合并成一次按 `location` 的请求，Apple 在一次响应里返回该地点周边的所有门店（香港 6 家、上海及周边 12 家、东京周边 6 家……）。`apw products` / `apw stores` 不变；`check` / `watch` 会按门店编号从内置目录给目标补上 `pickupLocation`，目录里没有的门店按门店单独查。响应里没带上的门店当轮补查一次，之后直接按门店查。目标 JSON 里也可以显式写 `pickupLocation`，写法与官网取货查询的 `location` 参数一致（大陆是「省 市」，如「江苏 苏州」；日本用邮编）。
- **请求预算**：每个进程一个令牌桶，容量 20、每 60 秒恢复 1 次，暖场和取货都计入。桶空了不是拒绝，而是排队等恢复：`watch` 会把下一轮推迟到预算够为止，并在 `cycleComplete.paced` 里说明；`check` 单次两个请求，不会等。数字取得比实测保守，给同一出口 IP 上的浏览器和第二个实例留余量。

## 诊断 HTTP 541：`apw doctor`

被 Apple 拦截（HTTP 541）的原因已经查清（按出口 IP 计数，见上一节），`doctor` 仍保留用来在自己的网络上做请求特征的对照——例如怀疑某条网络对指纹或 cookie 另有要求时：

```bash
apw doctor --locale zh_CN --store R359 --part 'MJTF4CH/A'
apw doctor --locale zh_CN --store R359 --part 'MJTF4CH/A' --interval 30 --json
```

它对同一门店、同一零件号依次跑六个变体，每个变体用全新的客户端（独立 cookie 罐）查一次：`legacy`（v0.4.1 的行为：rustls 指纹 + Chrome/130 请求头 + `X-Requested-With`）、`rustls`（v0.4.2-beta.1 的行为：rustls 指纹 + Chrome 149 请求头）、`chrome-tls`（当前默认：Chrome 149 的 TLS / HTTP/2 指纹 + Chrome 149 请求头）、`chrome-tls-no-warm`（不带 cookie）、`chrome-tls-xrw`（加 `X-Requested-With`）、`chrome-tls-apple-extras`（加 Apple 商店前端的两个自定义头）。前三个是「旧行为 → 只换请求头 → 再换传输层指纹」的阶梯。变体之间至少间隔 10 秒（默认 15），从不重试，连续两个变体被拦就停止，其余标为未跑。

输出是一份 Markdown 报告：每次暖场与取货请求的状态码、HTTP 版本、耗时和 cookie **名字**，末尾附判读。报告不含 cookie 值、IP 或账号信息，可以直接贴进 issue。`--json` 改为每个变体一行 NDJSON，`report.records[]` 是原始请求记录。退出码 0 表示至少一个变体拿到明确答复，3 表示没有。跑之前请先暂停桌面版监控，以免两边互相影响。

### 传输层指纹

从 v0.4.2-beta.2 起，取货查询默认走 `chrome-tls` 传输：用 BoringSSL 复刻 Chrome 149 的 TLS 握手与 HTTP/2 设置（wreq），请求头由同一版本的档案控制。rustls 的握手指纹与任何浏览器都不一样，是请求头之外浏览器与我们之间唯一剩下的差别。构建机上没有 cmake 时可以用 `--no-default-features --features notifications` 去掉它，退回 rustls。

### 被拦后的冷却

`check` / `watch` 和桌面版共用同一套客户端：遇到 541 或 403 不再秒级重试，而是把该地区标为冷却，首次 5 分钟，冷却结束后只放一次探测，只有探测再被拦才依次延长到 10、20、30 分钟；期间该地区的查询直接返回 `unknown / blocked`（`detail` 里写明剩余时间），不发请求。同一轮里并发在飞的其他请求跟着被拦算同一次事件，不升档（v0.4.2-beta 曾把它们各算一次，一轮就抬到 30 分钟）；只有探测成功才算解封。实测被拦后大约 10 到 15 分钟恢复，期间继续发请求不会更快恢复。

## 验证与打包

```bash
cargo test --locked -p apw-cli
cargo test --locked -p apw-core --no-default-features
cargo clippy --locked -p apw-cli --all-targets -- -D warnings
python3 scripts/package-cli.py --binary target/release/apw --target aarch64-apple-darwin
```

日常测试保持离线。CLI 测试用真实核心引擎和模拟 Fetcher 验证库存错误、部分结果、去重及取消，并用真实子进程验证接口输出与 stdin 超时。`.github/workflows/cli.yml` 独立验证四个平台、构建二进制并打包 CLI、skill、文档和许可，上传 Actions artifacts；不依赖桌面构建，也不自动发布公开 Release。
