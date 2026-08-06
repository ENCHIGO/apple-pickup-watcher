# Apple Pickup Watcher

轮询 Apple 直营店的「到店取货」库存，指定门店的指定型号一有货就立刻提醒你。

支持中国大陆、中国香港、中国台湾、日本、新加坡、澳大利亚、马来西亚七个地区。

---

## 来源与许可

**本项目是 [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper) 的硬分叉重写版本。**

原项目由 [@Hteen](https://github.com/hteen) 及其贡献者以 **GPL-3.0** 发布，目前已停止维护。
本项目在其基础上重写，**同样以 GPL-3.0 发布**，原始许可全文完整保留在
[`LICENSE`](LICENSE) 中，修改内容、修改日期与完整的版权声明见 [`NOTICE`](NOTICE)。

如果原项目仍然可用，请优先支持原作者。本项目存在的唯一理由是：原项目所依赖的
Apple 接口已经失效，且失效的表现形式是「界面一切正常、只是永远显示无货」，
这比直接报错更容易让人错过购买时机。

---

## 与上游的差异

上游最后一次提交是 2025 年 9 月。之后 Apple 关掉了它使用的接口，程序从此静默失效。
除了换接口，下面这些问题也一并修掉了：

| | 上游 apple-store-helper | 本项目 |
| --- | --- | --- |
| **库存接口** | `/shop/fulfillment-messages`，现已对任意请求恒定返回 HTTP 541 拦截页 | `/shop/retail/pickup-message`，七个地区均已实测返回 200 |
| **查询失败时** | 一律当成「无货」显示 | 有货 / 无货 / **未知** 三态，查不到就明说查不到，并附上原因 |
| **轮询频率** | 固定 500 毫秒一轮，即每秒两次请求 | 默认 30 秒，带随机抖动，连续失败自动指数退避 |
| **HTTP 客户端** | 每次查询新建一个，连接池完全无法复用，内存可涨到 17 GB | 全进程复用同一个客户端与连接池 |
| **后台出错** | 后台 goroutine 里直接 `panic`，整个进程被杀掉 | 全部错误改为返回值，代码中没有任何 `panic`，并留有兜底恢复 |
| **并发安全** | 界面线程与轮询线程无锁共享 map，触发 Go 运行时 fatal error 直接终止进程 | 所有共享状态由读写锁保护，CI 强制开启 `-race` |
| **设置存放** | 写在进程工作目录，打包成 `.app` 后会静默丢失 | 写入系统用户配置目录，且是「先写临时文件再原子改名」 |
| **机型清单** | 需要每年手动从浏览器开发者工具里复制粘贴 | 可直接从 Apple 官网购买页自动抓取，内置数据作为离线兜底 |

下面两点值得展开说，因为它们直接决定这个工具还能不能用。

### 接口失效

上游查询库存用的是 `/shop/fulfillment-messages`。这个接口现在对任意请求都返回
HTTP 541 加一个 12 万字节的「Page Not Found」HTML 拦截页 —— 中国大陆站和美国站的
响应完全一致，且同一时刻 apple.com.cn 首页正常返回 200，可以排除是 IP 被封。
也就是说这不是偶发故障，而是接口已经被彻底关掉了。

本项目改用 `/shop/retail/pickup-message`。这是 Apple 购买页当前实际在用的接口，
一次请求可以带上同一门店的多个零件号，因此每个门店每轮只需要发一次请求。

### 「无货」和「查不到」不是一回事

上游在请求出错时返回一个空结果，然后把所有查不到的型号都标成「无货」。
接口被关掉之后，用户看到的就是一屏永远不会变的「无货」，程序看上去在正常工作，
实际上早已失去意义。

本项目把「未知」提升为和「有货」「无货」平级的一等状态：只有 Apple 明确作答时
才会显示有货或无货，任何查询失败、响应结构不符、型号已下架的情况一律显示「未知」
并给出具体原因。**猜错成「无货」会让你错过机会，猜错成「未知」只是让你多看一眼。**

---

## 安装

### 下载现成的版本

前往 [Releases](https://github.com/ENCHIGO/apple-pickup-watcher/releases) 页面下载对应平台的压缩包。

**macOS 用户请注意：** 发布产物没有经过 Apple 公证（这需要付费的开发者账号），
从浏览器下载后会被系统打上隔离属性，双击时 Gatekeeper 会拦下来，
提示「已损坏，无法打开」或「无法验证开发者」。**应用本身没有损坏**，
在终端里清掉隔离属性即可：

```shell
xattr -cr "/Applications/Apple Pickup Watcher.app"
```

（上游 issue #111「M4 Pro 上跑不起来」很可能就是这个原因 —— 系统给出的提示词
指向的方向和真实原因完全不同，很容易被误认为是芯片架构不兼容。）

### 从源码构建

需要 Go 1.26.2 或更高版本。Fyne 依赖 CGO，所以还需要本地的 C 工具链：

```shell
# macOS：安装 Xcode Command Line Tools
xcode-select --install

# Debian / Ubuntu
# Wayland 那几个包不能省：Fyne v2.8 带的 GLFW 3.4 会同时编译 X11 和 Wayland
# 两个后端，只装 xorg-dev 会报 wayland-client-core.h: No such file or directory。
sudo apt-get install -y gcc pkg-config libgl1-mesa-dev xorg-dev libxkbcommon-dev \
  libwayland-dev wayland-protocols libwayland-bin libdecor-0-dev libasound2-dev

# Windows：安装 MinGW-w64，确保 gcc 在 PATH 中
```

然后：

```shell
make build        # 编译到 dist/apple-pickup-watcher
make run          # 直接运行
make test         # 带竞态检测跑测试
make vet          # 静态检查
make fmt          # 格式化代码
make clean        # 清理构建产物
make help         # 列出全部目标
```

不想用 make 也可以直接：

```shell
go build -o apple-pickup-watcher ./cmd/apple-pickup-watcher
go run ./cmd/apple-pickup-watcher
```

### 打包成 macOS 应用

```shell
go install fyne.io/tools/cmd/fyne@latest
make package-mac
```

产物是 `dist/Apple Pickup Watcher.app`，已经做过 ad-hoc 签名，本机可直接运行。
图标默认取仓库里的 `assets/icon.png`，想换一张用 `make package-mac ICON=<路径>` 指定。

> 由于 Fyne 依赖 CGO，**各平台必须在对应系统上原生构建**，
> 在 Linux 上设 `GOOS=darwin` 直接交叉编译是编不过的。

---

## 使用方法

1. **提前在 Apple 官网登录账号，并把想买的型号加进购物袋。**
   这一步必须自己做 —— 本工具只负责盯库存和提醒，不会替你下单。
2. 启动应用，选择地区。
3. 选择门店和型号，点「添加」把它们加进监控列表；可以同时盯多个门店和多个型号。
4. 点「开始」。列表里每一行会实时显示当前状态：有货 / 无货 / 未知。
5. 检测到有货时，应用会按你的设置发出提醒：播放提示音、推送到 iOS 设备、
   自动打开购物袋页面。**打开购物袋后仍需你手动选择门店并完成下单。**

### 推送到 iPhone

想在离开电脑时也能收到提醒，可以配合 [Bark](https://bark.day.app/)：

1. 在 App Store 安装「Bark」，允许其发送通知。
2. 打开 Bark，复制代表你设备的推送地址（形如 `https://api.day.app/xxxxxxxx`）。
3. 把这个地址填进应用的 Bark 设置项，保存后即可生效。

---

## 配置文件位置

设置保存在系统约定的用户配置目录下，卸载或移动应用都不会影响它：

| 系统 | 路径 |
| --- | --- |
| macOS | `~/Library/Application Support/apple-pickup-watcher/settings.json` |
| Linux | `~/.config/apple-pickup-watcher/settings.json`（受 `XDG_CONFIG_HOME` 影响） |
| Windows | `%AppData%\apple-pickup-watcher\settings.json` |

里面记录了地区、监控列表、查询间隔、Bark 地址、提示音与自动打开购物袋的开关。
想彻底重置，直接删掉它。

文件损坏或读不出来时，应用会回退到默认设置继续运行，不会因此起不来；
同时会把原文件改名成 `settings.json.corrupt-<时间戳>` 留档，并且**本次运行
不再写盘**。这一条是刻意的：读不到旧配置不等于用户没有配置，若照常写盘，
启动后几毫秒内就会用一份空的默认配置覆盖掉唯一的那份，监控列表再也找不回来。
遇到这种情况，界面日志里会说明原因和备份位置。

---

## 注意事项

- **这不是外挂，不能全自动下单。** 它做的事只有一件：告诉你现在有货了。
  抢购的关键几秒仍然取决于你自己的手速和提前准备。
- **提前登录、提前加购物袋。** 有货提醒弹出来时再去登录已经来不及了。
- **不要把查询间隔调到最低。** 默认 30 秒对抢购场景完全够用。
  程序允许的下限是 5 秒，但请理解：上游那种每秒两次的频率正是被风控盯上的原因，
  接口被关掉之后所有人都用不了。
- **只覆盖直营店的到店取货。** 官网快递发货、授权经销商、运营商渠道都不在范围内。
- **代理生效方式。** 客户端遵循标准的 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`
  环境变量，需要走代理时按常规方式设置即可。
- **状态显示「未知」时先看原因。** 如果提示被拦截或接口结构不符，
  说明 Apple 又调整了接口，此时继续盯着界面没有意义，欢迎提 issue。

---

## 免责声明

本项目仅供个人查询商品库存使用，与 Apple Inc. 无任何关联，也未获得其授权或认可。

请合理设置查询频率、尊重 Apple 的服务与其他用户 —— 高频轮询不但会让你自己被限流，
也会让这个公开接口对所有人都变得更难用。请勿将本项目用于批量抢购、代购牟利或任何
违反 Apple 服务条款、当地法律法规的用途。使用本项目产生的一切后果由使用者自行承担。

本程序不附带任何担保；详见 GPL-3.0 中关于免责的条款。

---

## 许可

本项目以 [GNU General Public License v3.0](LICENSE) 发布。

它是 [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper)（GPL-3.0）的修改版本，
修改日期为 2026-08-06，详细的修改摘要与版权声明见 [`NOTICE`](NOTICE)。

任何基于本项目的再分发，同样必须以 GPL-3.0 授权，并保留上述声明。
