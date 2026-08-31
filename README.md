# Apple Pickup Watcher

盯着 Apple 直营店的「到店取货」库存，某个型号在你选的门店可取货时立刻提醒你。
支持 **iPhone、iPad、Mac、Apple Watch** 四个品类，七个地区：中国大陆、中国香港、
中国台湾、日本、Singapore、Australia、Malaysia。

跨平台桌面应用，macOS / Windows / Linux。Rust + Tauri，v0.3.0。

**English** — Apple Pickup Watcher monitors in-store pickup availability at Apple Retail
Stores and alerts you the moment a specific model becomes available at the store you
picked. It covers iPhone, iPad, Mac and Apple Watch. It is a cross-platform desktop app
(macOS / Windows / Linux) built with Rust and Tauri, covering seven regions: China
mainland, Hong Kong, Taiwan, Japan, Singapore, Australia and Malaysia.

It is a rewrite of [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper)
(GPL-3.0, unmaintained). That project's stock endpoint now returns **HTTP 541** for every
request, and because it reported failed lookups as “out of stock”, it kept looking healthy
while showing no stock forever. See [常见问题 / FAQ](#常见问题--faq) for the diagnosis and
the endpoint that still works.

---

## 来源与许可

本项目是 [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper)（GPL-3.0）
的重写版本。原项目已停止维护。

本项目同样以 **GPL-3.0** 发布：

- 原始许可全文在 [`LICENSE`](LICENSE)，未作任何删改。
- 派生关系、原作者版权声明与修改摘要在 [`NOTICE`](NOTICE)。

内嵌的门店与商品数据、以及整体的产品设计都来自原项目，因此 GPL 的义务继续适用 ——
你可以自由使用、修改和再分发本项目，但衍生作品必须同样以 GPL-3.0 开源。

**如果原项目对你仍然可用，请优先去支持原作者。** 这个项目存在的唯一理由是原项目已经
不工作了，不是为了取代谁。

---

## 为什么会有这个项目

原项目依赖 Apple 的 `/shop/fulfillment-messages` 接口。这个接口现在对**任意**请求都恒定
返回 HTTP 541 加一个 128002 字节的拦截页 —— 中国大陆站与美国站的响应完全一致，同一时刻
`apple.com.cn` 首页正常返回 200，可以排除是 IP 被封。也就是说，接口对所有人都已经不通了。

真正致命的不是接口失效，而是失效的**表现方式**：原项目把请求失败当作「无货」处理。于是
程序看上去一切正常 —— 界面在刷新、时间戳在跳、日志在滚 —— 只是永远显示无货。它就这样
静默失效了大半年。一个直接报错的程序你会立刻去修；一个永远说「无货」的程序，你只会以为
今天没货，然后错过购买时机。

本项目做了两件事：

1. **换接口。** 改用 `/shop/retail/pickup-message`，重写了全部的请求构造与响应解析。
2. **把「未知」提升为一等状态。** 库存状态是「有货 / 无货 / 未知」三态，`未知` 必须携带
   一个具体原因（被拦截、被限流、响应结构不符、Apple 返回业务错误、网络失败、尚未查询），
   在界面上和「无货」分开展示。

   > 猜错成「无货」会让你错过机会，猜错成「未知」只是让你多看一眼。
   > 这两种错误的代价完全不对等，所以代码在所有拿不准的地方一律倒向「未知」。

这条不变量是用类型系统守的，不是靠自觉：`Unknown` 构造时必须给出原因，`Availability`
刻意不实现 `Default`（有了默认值，迟早有人在解析失败时 `unwrap_or_default()`），
而所有 API 错误到状态的转换是单向且全覆盖的 —— **没有任何一条错误路径能通向「无货」**。

---

## 相对 Go 版 v0.1.x 的变化

v0.1.x 是 Go + Fyne 实现。v0.2.0 换成了 Rust + Tauri，界面重写为 React + TypeScript。

| | v0.1.x（Go + Fyne） | 现在（Rust + Tauri） |
| --- | --- | --- |
| 发布体积 | 约 38 MB | 8 MB 量级 |
| 关闭窗口 | 退出程序 | 收进系统托盘继续跑 |
| 覆盖品类 | 只有 iPhone | iPhone / iPad / Mac / Apple Watch |
| 型号列表 | 硬编码，等作者发版 | 可从 Apple 官网在线更新 |
| 更新 | 手动看 Release | 应用内检查更新 |
| 设置文件 | `settings.json` | `settings.v2.json` |

几点说明：

- **系统托盘。** 这个工具的正常用法是发售前挂上几个小时，所以关窗口收进托盘而不是退出；
  真要退出走托盘菜单里的「退出」。到货提醒发在 Rust 侧而不是前端，因为托盘模式下窗口是
  隐藏的，WebView 可能被系统节流甚至挂起 —— 把「及时提醒」挂在一个会被挂起的执行环境上
  是不能接受的。
- **型号列表可在线更新。** 界面上有「从 Apple 官网更新型号列表」的按钮，新机发售当天就能
  盯，不必等这个程序发新版。按钮只抓**当前品类**的那几页 —— 四个品类加起来二十页购买页，
  想看新出的 Mac 没有理由等 iPhone、iPad、Watch 一起抓完。抓不到时自动退回随程序内嵌的
  离线快照，不会因此变得不可用。
- **应用内更新检查只提示，不静默安装。** 发现新版本会在界面上显示一条提示，装不装由你点。
- **配置可以共存、可以回退。** 设置文件换成了 `settings.v2.json`（两版的 JSON 字段命名不同，
  共用一个文件名会让两个版本互相把对方的配置读成缺省值再覆盖掉）。首次启动时，如果新版
  还没有任何监控目标，会自动把 Go 版 `settings.json` 里的配置迁移过来 —— 而那份旧文件
  **原封不动地留着**，你想退回 v0.1.x 随时可以，配置一条都不会丢。

体积下降只是换框架的附带结果，不是重写的理由。重写的理由写在上一节。

---

## 安装

到 [Releases](https://github.com/ENCHIGO/apple-pickup-watcher/releases) 下载对应平台的安装包。
macOS 分 Apple Silicon 与 Intel 两份，别下错。

### macOS：安装后必须先解除隔离标记

安装包**没有经过 Apple 公证**（公证需要每年 99 美元的开发者账号）。直接双击会被 Gatekeeper
拦下，提示「已损坏，无法打开」或「无法验证开发者」。把 `.app` 拖进「应用程序」之后执行：

```shell
xattr -cr "/Applications/Apple Pickup Watcher.app"
```

然后正常打开即可。这条命令做的事是清掉下载文件被打上的 `com.apple.quarantine` 扩展属性 ——
只对你自己刚下载的这一个应用生效，不会关掉系统的任何安全机制。

> 顺带一提：原项目的 issue #111「M4 Pro 上跑不起来」，**很可能**就是这件事。那类报告的表现
> （新机器、下载后完全打不开、没有任何错误日志）与未公证应用被 Gatekeeper 拦截完全吻合，
> 而不是芯片架构不兼容。这是根据现象做的推断，我们没有那台机器可以复现确认。

### Windows

安装包未做代码签名，SmartScreen 会提示「Windows 已保护你的电脑」。点「更多信息」→
「仍要运行」。

### Linux

按 Release 页面提供的包格式安装。AppImage 需要自己加可执行权限：`chmod +x`。

---

## 怎么用

1. 选**地区**。地区决定了查哪个 Apple 在线商店，换地区后门店和型号会一起重选。
2. 选**品类**（iPhone / iPad / Mac / Apple Watch）。它只是型号下拉框的筛选器，
   已经加进列表的监控目标不受影响 —— 四个品类是混在一张表里盯的。
3. 选**门店**和**型号**，点「添加」。可以加多条，不同品类、不同门店混着加都行。
4. 点「开始」。表格里每一行会显示状态：有货 / 无货 / 未知 / 待查询，以及最后检查时间。
5. 某一行从非有货变成有货时，会同时：弹系统通知、播提示音、发 Bark 推送（如已配置），
   并按设置打开该地区的购物袋页面。

看到「监控当前不可信」的告警，说明有目标处于未知状态 —— 展开能看到具体原因（被拦截、
被限流、接口结构变了……）。**这时候表格里的「无货」不代表真的没货**，告警消失前不要拿它
当准信。

### 查询间隔

默认 **30 秒**，下限 **5 秒**。

原项目写死 500 毫秒一轮，即每个门店每秒两次请求。这个频率对一个公开的商品查询接口来说
过高，是触发风控的直接原因，也是它的 issue 里 503 / 541 反复出现的背景。30 秒足够应付
发售抢购，同时不至于把自己送进黑名单。

填了小于 5 秒的值会被退回默认的 30 秒，而不是夹到 5 秒 —— 手抖填了 1 秒的人想要的是快，
但 5 秒同样会被风控盯上，退回 30 秒才是安全的那一侧。

### 配置文件位置

| 平台 | 路径 |
| --- | --- |
| macOS | `~/Library/Application Support/apple-pickup-watcher/settings.v2.json` |
| Windows | `%APPDATA%\apple-pickup-watcher\settings.v2.json` |
| Linux | `$XDG_CONFIG_HOME/apple-pickup-watcher/settings.v2.json`（默认 `~/.config/...`） |

配置写在系统约定的用户配置目录里，不是程序旁边 —— 打包成 macOS `.app` 之后，进程的工作
目录取决于应用被如何启动，可能是 `/` 这种不可写的位置，写在那里会静默丢失。

如果这个文件读不出来（磁盘损坏、被别的程序写坏了、手工编辑成了非法 JSON），程序**不会**
拿默认配置继续跑然后把它覆盖掉 —— 那等于用一份空配置抹掉你攒了很久的监控列表。它会把
原文件改名留档，然后在界面上告诉你档案存在哪。

### Bark 推送

[Bark](https://github.com/Finb/Bark) 是一个开源的 iOS 推送 App。装好后它会给你一个形如
`https://api.day.app/你的Key` 的地址，把整段填进设置里的「Bark 推送地址」即可，留空则不推送。

地址后面可以带查询参数，会原样保留，例如：

```
https://api.day.app/你的Key?group=库存&sound=alarm&level=critical
```

点「测试提醒」可以立刻走一遍完整的提醒链路（系统通知 + 提示音 + Bark），确认配置有效 ——
不要等到发售当晚才发现推送没配对。

推送失败**不会**影响库存判定。渠道自己的问题就是渠道自己的问题，不能反过来改写已经确认
的「有货」。

---

## 从源码构建

### 工具链

| 工具 | 版本 |
| --- | --- |
| Rust | 1.94（edition 2024；`Cargo.toml` 里声明的最低版本是 1.90） |
| Node.js | 当前 LTS |
| pnpm | 9 |

### 系统依赖

**macOS**：Xcode Command Line Tools（`xcode-select --install`）。

**Windows**：Microsoft C++ 生成工具 与 WebView2 Runtime（Windows 11 已自带）。

**Linux**（Debian / Ubuntu）：

```shell
sudo apt update
sudo apt install -y \
  libwebkit2gtk-4.1-dev build-essential curl wget file \
  libxdo-dev libssl-dev libayatana-appindicator3-dev librsvg2-dev \
  libasound2-dev
```

前面那一组是 Tauri v2 官方前置依赖清单；最后的 `libasound2-dev` 是本项目额外需要的 ——
提示音走 rodio，在 Linux 上最终落到 ALSA，缺了它会在**编译期**报找不到 `alsa.h`，而不是
在运行时才安静地没声音。

其他发行版的包名不同（Fedora 是 `webkit2gtk4.1-devel` 一套，Arch 是 `webkit2gtk-4.1` 一套），
以 [Tauri v2 的前置依赖文档](https://v2.tauri.app/start/prerequisites/) 为准。

### 命令

```shell
pnpm install                  # 首次
pnpm tauri dev                # 起开发版应用
pnpm tauri build              # 打安装包，产物在 target/release/bundle/
```

提交前跑这几条，和 CI 一致：

```shell
cargo test                                                    # 全部离线测试，不碰网络
cargo clippy --all-targets --all-features -- -D warnings
cargo fmt --all --check
python3 crates/apw-core/data/generate.py --self-test           # 快照生成脚本自检，不联网
pnpm exec tsc --noEmit
pnpm exec vite build
```

### 代码结构

```
crates/apw-core/   核心逻辑，不依赖任何界面框架，可脱离 GUI 单独测试
                     model          三态库存类型、地区表、品类与购买页、监控目标
                     apple          Apple 接口客户端：限速、退避重试、错误分类、响应解析
                     apple_catalog  从购买页抠出商品数据（两种页面排布都认）
                     catalog        商品与门店目录（内嵌快照 + 在线刷新）
                     watcher        调度引擎（actor 模型）
                     notify         Bark 推送与提示音
                     config         设置持久化与旧版迁移
                   data/            内嵌离线快照，由 data/generate.py 重新生成
src-tauri/         Tauri 应用外壳（crate 名 apw-app）：装配、IPC 转译、托盘、系统通知
src/               React 19 + TypeScript 前端
```

有条边界是硬的：**所有业务判断都在 `apw-core` 里**，外壳层和前端不许出现任何「什么算有货」
之类的逻辑。一旦让界面层参与判断，那条核心不变量就多了一处可以被绕开的地方。

调度引擎用 actor 模型（一个任务独占全部状态，外界只能发消息）而不是「共享状态 + 锁」。
原因写在 `crates/apw-core/src/watcher.rs` 的模块文档里 —— 简单说，共享状态那套写法在原项目
里制造了一整类竞态（放锁去等待的窗口、第二条循环、`close(nil)` 崩进程、循环挂了之后叫不醒
的僵尸引擎），而这类问题在 actor 结构下是**不存在**的，不是被堵住的。

---

## 给维护者

### main 受保护，改动一律走 PR

`main` 上开了分支保护，**对仓库管理员同样生效**：

| 规则 | 状态 |
| --- | --- |
| 合并前必须通过 `检查（Linux）`、`检查（macOS）` | 是 |
| 禁止 force push | 是 |
| 禁止删除分支 | 是 |
| 对管理员同样生效（`enforce_admins`） | 是 |
| 要求 PR review | 否 —— 单人仓库要求 approval 等于把自己锁在外面 |

**这条会在发版时绊到你**：改版本号那一次提交也得走 PR，直接 `git push origin main` 会被
拒绝，报 `2 of 2 required status checks are expected`。别以为是仓库坏了。

要临时绕过（比如线上出事要立刻修），只能去 Settings → Branches 把规则关掉再打开 ——
刻意没留后门，单人仓库最大的风险恰恰是自己手滑。

`force push` 那条连管理员也挡，且**不受 `enforce_admins` 影响** —— 把 `enforce_admins`
关掉也照样推不上去，这两个开关管的不是同一件事。所以想删掉一个已经推上 main 的提交，
唯一的办法是去 Settings → Branches 临时勾上 Allow force pushes，推完立刻取消勾选。

### 发布需要两个 GitHub Secret

Tauri 的更新包需要签名，签名用两个仓库 Secret：

| Secret | 说明 |
| --- | --- |
| `TAURI_SIGNING_PRIVATE_KEY` | minisign 私钥内容 |
| `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | 私钥密码（本项目生成时用的是**空密码**，仍需建这个 Secret，值留空） |

对应的公钥已经写死在 `src-tauri/tauri.conf.json` 的 `plugins.updater.pubkey` 里。

**两个 Secret 没设置时，流水线照常产出未签名的安装包，不会失败。** 这是刻意的 —— 仓库主人
可能还没来得及配，不该因此连能装的包都拿不到。代价是这次发布的产物没有更新签名，老版本的
应用内更新检查认不了它，用户得手动下载。

> **私钥丢了就再也签不出能被老版本接受的更新包。**
>
> 私钥在本机 `.tauri/` 目录下，该目录已被 `.gitignore` 排除（`.gitignore` 里同时排除了
> `*.key`）。公钥编译进了已经发出去的每一个版本，换一把新密钥意味着所有存量用户的
> 应用内更新永久失效，只能靠公告让他们手动重装。**请离线备份 `.tauri/` 下的私钥。**

### 每天跑一次的真实接口契约测试

```shell
cargo test -p apw-core --features live --test live -- --nocapture --test-threads=1
```

它会依次查询七个地区的真实门店，检查四件事：接口还在不在、响应结构有没有变、
`pickupDisplay` 有没有出现我们不认识的新取值、以及**我们是不是真的在用 HTTP/2**。

最后那条是补上去的，因为它对应一类特别阴的缺陷：`reqwest` 的 HTTP/2 支持挂在 `http2`
feature 上，而这个项目用的是 `default-features = false`。那个 feature 曾经漏了整整一个
版本 —— 客户端静默退回 HTTP/1.1，查询照常成功、离线测试全绿，功能上一点征兆都没有。
但我们的 UA 自称 Chrome 130，而真实的 Chrome 早就不用 HTTP/1.1 跟 apple.com 说话了；
这个矛盾是最一眼可辨的机器人特征，在受风控审查的网络上直接换来一屏 HTTP 541
（issue #3）。**配置写漏了、功能却没坏**的问题，只有真的去连一次才发现得了。

**这个测试就是为了不重蹈上游覆辙而存在的。** 上游正是因为 Apple 悄悄换掉了接口而彻底失效，
却因为没有任何真实接口测试，半年时间里没人发现程序返回的「无货」其实是被拦截。用假响应
写的单元测试验证的是解析逻辑，拦不住这种事 —— 只有真的打一次 Apple 才行。

它失败时要看的是「Apple 是不是又改了接口」，而不是「测试是不是又不稳定了」。

两条纪律：

- **常规 CI 绝对不要跑它。** 它会真的向 Apple 发请求。每次 push 都跑等于替所有贡献者
  一起去打扰 Apple 的服务器。
- **按天定时跑。** 每天一次足够及时发现接口变更，也足够克制。

测试里那张零件号表（`crates/apw-core/tests/live.rs` 的 `CASES`）会随机型更新而失效。届时
应当**更新这张表，而不是删掉测试**。

### 重新生成内嵌的离线快照

```shell
python3 crates/apw-core/data/generate.py
```

它会把七个地区、四个品类共二十页购买页各抓一遍，把用得到的字段裁出来写进
`crates/apw-core/data/products_<locale>.json`。抓不全时**非零退出**且不掩盖失败 ——
静默写出一份缺页的快照，等于把「兜底目录」变成「兜底目录里刚好没有你要的那台机器」。

平时不需要跑它：程序运行时会自己从购买页现抓，快照只是断网或被拦截时的兜底。
真正需要跑的时候是两种：Apple 换了新一代机型（`model.rs` 的 `DEFAULT_FAMILIES` 里
加了新 slug），或者购买页的数据结构变了。上游是把这份数据手工从浏览器开发者工具里
复制进仓库，于是每发一代新机都得等作者更新并发版。

### 打包平台

macOS 的 Intel 构建用 `macos-15-intel` runner，arm64 用 `macos-latest`。

上一代的 `macos-13` 标签已于 2025-12-04 退役，写它会让作业在初始化阶段就直接失败 ——
连日志都没有，只有一行调度错误。这个坑本项目踩过一次，整条发布流水线因此空转。
改这里之前请先确认新标签当前有效。

---

## 常见问题 / FAQ

这一节回答的是几个真实被反复问到的问题（上游 issue #127 #126 #124 #122 #118 都是同一件事）。

### 为什么 apple-store-helper 一直显示「无货」，但官网明明有货？

因为它使用的库存接口已经失效了，而失效的表现不是报错，是「永远无货」。

`/shop/fulfillment-messages` 现在对任意请求恒定返回 **HTTP 541**，响应体是一个 128002 字节
的「Page Not Found」拦截页。可复现：

```shell
curl -sS -o /dev/null -w 'HTTP %{http_code}  size=%{size_download}\n' \
  'https://www.apple.com.cn/shop/fulfillment-messages?little=true&mt=regular&parts.0=MG724CH%2FA&store=R683'
# HTTP 541  size=128002
```

中国大陆站与美国站返回的字节数完全相同，同一时刻 apple.com.cn 首页正常返回 200 ——
可以排除偶发故障和 IP 被封。换 UA、补请求头、先取 cookie 再请求，都仍然 541。

真正让它「看起来正常」的是另一半：那个项目在请求失败时返回空结果，上层把查不到的型号
一律标成无货（`services/listen.go:226-230` 与 `:147`）。所以接口没了之后，界面一切正常，
只是永远显示无货 —— 这比直接报错更容易让人错过购买时机。

### HTTP 541 是什么意思？

541 不是标准 HTTP 状态码，是 Apple 边缘节点自定义的拦截响应。看到它基本可以确定请求被
挡下了，而不是「没有库存」。降低查询频率、更换 User-Agent、加重试都无法绕过 —— 问题不在
频率或伪装，在于那个接口本身已经不在了。

### 现在还能用的接口是哪个？

`/shop/retail/pickup-message`：

```shell
curl -sS 'https://www.apple.com.cn/shop/retail/pickup-message?pl=true&mts.0=regular&parts.0=MG724CH%2FA&store=R683'
# HTTP 200，返回 JSON
```

响应结构与旧接口不同：门店在 `body.stores[]`（不再是 `body.content.pickupMessage.stores`），
状态在 `partsAvailability.<零件号>.pickupDisplay`（取值 `available` / `unavailable` /
`ineligible`），`messageTypes` 下只有 `regular` 而没有 `compact`。一次请求可以带多个零件号，
所以每个门店每轮只需发一次请求。七个地区都实测可用。

### Why does apple-store-helper always show “out of stock”?

Its stock endpoint `/shop/fulfillment-messages` now returns **HTTP 541** with a 128002-byte
“Page Not Found” interception page for every request, regardless of part number or store.
Worse, that project treated a failed lookup as “out of stock”, so the UI kept looking healthy
while never actually querying anything. Lowering the polling interval or changing the
User-Agent does not help — the endpoint is simply gone.

The endpoint that still works is `/shop/retail/pickup-message`, with a different response
shape (`body.stores[]`, `partsAvailability.<part>.pickupDisplay`, and only `regular` under
`messageTypes`). All seven regions verified.

### 这个项目和上游、以及其他 fork 有什么不同？

最主要的一条不是换了接口，而是**「查不到」和「无货」被当成两件事**。状态有三种：有货、
无货、未知；未知必须携带原因（被拦截 / 限流 / 接口结构变了 / 网络失败），界面上用完全
不同的配色显示，并把「查不到多少项」单独标出来。接口哪天再变，你会立刻看到告警，而不是
对着一屏看起来正常的「无货」空等。

另外有一个每天自动跑的契约测试，直接请求 Apple 的真实接口。上游正是因为没有这道防线，
接口失效后半年多没人发现。

---

## 免责声明

本工具仅供**个人查询**使用，与 Apple Inc. 没有任何关系，不是 Apple 的产品，也未获得
Apple 的授权或认可。

请合理设置查询频率。默认 30 秒、下限 5 秒是经过考虑的取值，不是随手填的。把它调到贴着
下限跑，对你自己的收益接近于零（门店库存不会在 5 秒内反复横跳），对 Apple 的服务器却是
实打实的负担，还会让你更快被风控盯上。

**请不要用它做批量抢购牟利。** 这个工具是为了让想买手机的人别错过补货，不是为了给黄牛
提效。真那么用了，最先被封的是这条路本身，然后所有人都用不了。

因使用本工具造成的任何后果由使用者自行承担。

---

## 许可

GPL-3.0-or-later。详见 [`LICENSE`](LICENSE) 与 [`NOTICE`](NOTICE)。
