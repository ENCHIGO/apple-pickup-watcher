<!-- markdownlint-disable MD033 MD041 -->
<div align="center">

<img src="assets/icon.png" alt="Apple Pickup Watcher 图标" width="112" height="112">

# Apple Pickup Watcher

**苹果直营店到店取货库存监控与到货提醒**

盯住你想买的 iPhone、iPad、Mac 或 Apple Watch，选定的 Apple Store 一有货就通知你。<br>
桌面应用、命令行 `apw` 和 agent skill 三种形态，免费开源。

<sub>Free, open-source Apple Store pickup stock monitor with restock alerts for macOS, Windows and Linux.</sub>

<br>

[![最新版本](https://img.shields.io/github/v/release/ENCHIGO/apple-pickup-watcher?label=release&color=2563eb)](https://github.com/ENCHIGO/apple-pickup-watcher/releases/latest)
[![下载量](https://img.shields.io/github/downloads/ENCHIGO/apple-pickup-watcher/total?label=downloads&color=16a34a)](https://github.com/ENCHIGO/apple-pickup-watcher/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/ENCHIGO/apple-pickup-watcher/ci.yml?branch=main&event=push&label=CI)](https://github.com/ENCHIGO/apple-pickup-watcher/actions/workflows/ci.yml)
[![CLI](https://img.shields.io/github/actions/workflow/status/ENCHIGO/apple-pickup-watcher/cli.yml?branch=main&event=push&label=CLI)](https://github.com/ENCHIGO/apple-pickup-watcher/actions/workflows/cli.yml)
[![许可](https://img.shields.io/github/license/ENCHIGO/apple-pickup-watcher?color=6b7280)](LICENSE)
[![平台](https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-111827)](https://github.com/ENCHIGO/apple-pickup-watcher/releases/latest)

[项目主页](https://enchigo.github.io/apple-pickup-watcher/) · [English](README.en.md) · [下载桌面版](https://github.com/ENCHIGO/apple-pickup-watcher/releases/latest) · [CLI 文档](docs/cli.md) · [Agent skill](skills/apple-pickup-watcher/SKILL.md) · [常见问题](#常见问题)

</div>

<br>

<p align="center">
  <img src="assets/screenshot.png" alt="Apple Pickup Watcher 桌面版截图：监控列表逐行显示门店、型号、有货 / 无货 / 未知状态与最后检查时间" width="900">
</p>

## 它做什么

| 特性 | 说明 |
| --- | --- |
| **三态库存，失败不装无货** | 每个目标只会是「有货 / 无货 / 未知」之一。被拦截、被限流、网络失败或响应结构变化时，显示**带原因的「未知」**并发出「监控当前不可信」告警，绝不静默显示成无货。 |
| **多门店、多型号一起盯** | 同一张表里混合监控不同品类、不同门店的目标，型号下拉框支持按关键字搜索，加完直接开始。 |
| **关窗不退出** | 关闭窗口收进系统托盘，Rust 后台继续查询与提醒，发售前挂几个小时也不用一直开着窗口。 |
| **多渠道到货提醒** | 系统通知、提示音，可选 [Bark](https://github.com/Finb/Bark) 推送到 iPhone。持续有货不重复提醒；可按设置自动打开购物袋。 |
| **型号目录可在线刷新** | 内置离线快照，一键从 Apple 购买页刷新当前品类的型号列表；已支持购买页里的新型号发售当天就能加入监控。 |
| **CLI 与 agent skill** | `apw` 命令输出 JSON / NDJSON，无需桌面环境；配套 skill 让 Codex 等 agent 直接查库存、等到货。 |

安装包只有 3 MB 上下（Linux AppImage 除外），Rust + Tauri 2 构建，React 界面。

## 快速开始

### 桌面版

到 [Releases](https://github.com/ENCHIGO/apple-pickup-watcher/releases/latest) 下载对应平台的安装包：

| 平台 | 安装包 | 说明 |
| --- | --- | --- |
| macOS | `.dmg`（Apple Silicon / Intel 各一份，别下错） | 未经 Apple 公证，首次打开被拦下时执行下面的命令 |
| Windows | `x64-setup.exe` / `.msi` | 未做代码签名，SmartScreen 提示时点「更多信息」→「仍要运行」 |
| Linux | `.AppImage` / `.deb` / `.rpm` | AppImage 需要先 `chmod +x` |

```bash
# macOS：清除下载隔离标记，只对这一个应用生效
xattr -cr "/Applications/Apple Pickup Watcher.app"
```

安装细节、Bark 推送、配置文件位置见 [桌面版指南](docs/desktop.md)。

### CLI `apw`

```bash
git clone https://github.com/ENCHIGO/apple-pickup-watcher.git
cd apple-pickup-watcher
cargo install --path crates/apw-cli --locked   # 安装到 ~/.cargo/bin
```

只需要 stable Rust，不需要 Node、桌面环境或音频设备。预编译包可在 [CLI 构建记录](https://github.com/ENCHIGO/apple-pickup-watcher/actions/workflows/cli.yml) 的成功运行里下载（Artifacts，需登录 GitHub），包内含 skill 和文档。

### Agent skill

装好 Node.js LTS 后，用 [Skills CLI](https://github.com/vercel-labs/skills) 一条命令装进 Codex：

```bash
npx skills add ENCHIGO/apple-pickup-watcher --skill apple-pickup-watcher --agent codex --global
```

Claude Code 把 `codex` 换成 `claude-code`，去掉 `--global` 则装到当前项目。这条命令只装 skill，`apw` 仍需按上一节安装并加入 PATH，手动安装与管理见 [CLI 文档](docs/cli.md#安装-skill)。装好后在新会话里直接说：

> 用 $apple-pickup-watcher 查一下上海 Apple 直营店的 iPhone 512GB 库存，先列出可选机型让我选。

## 怎么用

### 桌面版三步

1. **添加目标**：选地区 → 品类 → 门店 → 型号，点「添加」。可以加多条，不同品类、不同门店混着加。
2. **开始监控**：点「开始」，保持应用运行、电脑联网且不休眠；关窗会收进托盘继续跑。默认每 30 秒查一轮，下限 5 秒。
3. **收到提醒去官网买**：确认有货时弹系统通知、播提示音、发 Bark，可按设置打开购物袋。添加商品、选择取货门店、结账和付款由你在 Apple 官网完成。

持续有货不会每轮重复提醒，离开有货状态后再次有货才重新提醒。看到「监控当前不可信」时，列表里的「无货」不能当准信，先按提示排查原因。

### CLI / agent

```bash
apw regions                                                    # 支持的地区
apw stores   --locale zh_CN --search 上海                       # 门店编号
apw products --locale zh_CN --category iphone --search 512GB   # SKU
apw check    --locale zh_CN --store R359 --part 'MJTF4CH/A' --timeout 60    # 查一次，输出 JSON
apw watch    --targets targets.json --until-in-stock --timeout 300          # 持续监控，逐行 NDJSON
```

`check` 的返回形状如下（示例状态不代表实时库存）：

```json
{
  "schemaVersion": 1, "command": "check", "healthy": true, "anyInStock": false,
  "snapshot": [{
    "target": { "locale": "zh_CN", "storeNumber": "R359", "storeTitle": "上海-南京东路",
                "partNumber": "MJTF4CH/A", "productName": "iPhone 18 Pro 512GB 冰川蓝色" },
    "availability": { "kind": "out_of_stock" },
    "lastCheckedMs": 1789000000000, "consecutiveFailures": 0
  }]
}
```

`availability.kind` 是 `in_stock` / `out_of_stock` / `unknown` 三者之一，`unknown` 一定带 `reason`（如 `blocked`）。退出码 0 只代表查询成功，**不等于有货**；查询失败、缺失结果和超时都不能当作无货。批量目标格式、`watch` 事件类型、退出码与 `apw schema` 见 [CLI 文档](docs/cli.md)。

## 支持范围

- **品类**：iPhone（18 Pro / 18 Pro Max、Duo、17、Air）· iPad（Pro、Air、iPad、mini）· Mac（MacBook Air / Pro / Neo、iMac、Mac mini、Mac Studio、Studio Display）· Apple Watch（Series、SE、Ultra、Hermès）
- **地区**：中国大陆 · 中国香港 · 中国台湾 · 日本 · 新加坡 · 澳大利亚 · 马来西亚
- **平台**：macOS（Apple Silicon / Intel）· Windows x64 · Linux x86_64

具体型号和门店取决于内置目录与当地 Apple 在线商店；新增购买页的机型需要更新程序。支持某地区不代表每次查询都能成功。Apple Watch 按表壳监控，查询时会自动附带同页的一条表带零件号，因为 Apple 的取货接口只把「表壳 + 表带」当作可售组合（[说明](docs/desktop.md#apple-watch-按表壳监控)）。

## 常见问题

### 为什么 apple-store-helper 一直显示「无货」，官网明明有货？

它依赖的 `/shop/fulfillment-messages` 接口已经失效，对请求返回 HTTP 541 拦截页；而它把请求失败当作无货处理，于是界面看起来一切正常，只是永远无货。本项目改用 `/shop/retail/pickup-message` 查询，并把查询失败保留为带原因的「未知」，接口再变时你会看到告警，而不是一屏假的「无货」。

### 「未知」「待查询」和「无货」有什么区别？

「待查询」是还没完成首次查询；「未知」是这次查询没有取得明确结果，原因可能是拦截、限流、网络失败或响应结构变化；「无货」是查询得到了明确的不可取货结果。只有最后一种才是真的没货。

### 程序报 HTTP 541 或「请求被拦截」，浏览器打开官网却正常？

把它当作查询失败，不要据此判断库存。v0.3.2 起客户端会先取一次购买页攒上 cookie 再查询，被拦时丢掉重攒；旧版本请先升级。升级后仍持续被拦，请附版本、地区、门店 / SKU 与脱敏日志提交 [Issue](https://github.com/ENCHIGO/apple-pickup-watcher/issues)。浏览器与程序的会话条件不同，单台机器正常不代表别的网络没问题。

### 关闭窗口后还会提醒吗？

会。关闭窗口只是收进系统托盘，后台继续查询和提醒；从托盘菜单「退出」才会停止。电脑需要联网并保持唤醒。

### 它会自动下单吗？能保证买到吗？

不会，也不能。它只查询库存并提醒，最多按设置替你打开购物袋；添加商品、选门店、结账付款都由你在 Apple 官网完成。库存可能在提醒后变化，程序不预留库存。

### 怎么在手机上收到提醒？

在 iPhone 上装 [Bark](https://github.com/Finb/Bark)，把它给你的推送地址填进设置里的「Bark 推送地址」，点「测试提醒」确认整条链路通畅。推送失败不会改变库存判定。

### 查询间隔为什么默认 30 秒？

门店库存不会在几秒内反复横跳，30 秒足够应付发售抢购，同时不至于把自己送进风控。下限 5 秒；填了更小的值会退回 30 秒。请不要用它做批量抢购牟利，那只会让这条路对所有人都失效。

更多：[桌面版指南](docs/desktop.md) · [CLI 文档](docs/cli.md) · [开发与维护](docs/development.md)

## 文档

| 文档 | 内容 |
| --- | --- |
| [桌面版指南](docs/desktop.md) | 安装、监控与提醒规则、型号目录、Bark、配置文件、常见问题 |
| [CLI 与 agent skill](docs/cli.md) | 构建安装、命令、JSON / NDJSON 格式、批量目标、退出码 |
| [开发与维护](docs/development.md) | 工具链、检查命令、代码结构、真实接口契约测试、发布流程 |
| [站点维护](docs/discoverability.md) | 项目主页的构建、部署与搜索收录约定 |
| [Agent skill](skills/apple-pickup-watcher/SKILL.md) | 给 agent 读的使用说明 |

## 来源与许可

本项目是 [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper)（GPL-3.0）的 Rust + Tauri 重写版本，按 **GPL-3.0-or-later** 发布。原始许可见 [LICENSE](LICENSE)，派生关系、原作者版权声明与修改摘要见 [NOTICE](NOTICE)。如果原项目对你仍然可用，请优先支持原作者。

本项目与 Apple Inc. 无关联，未获其授权或认可。仅供个人查询，不预留库存、不保证购得，最终库存与取货信息以 Apple 官网为准。因使用本工具造成的任何后果由使用者自行承担。

如果它帮你等到了想要的那台机器，欢迎点个 ⭐ 让更多人看到。
