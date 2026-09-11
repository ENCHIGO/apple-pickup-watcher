# Apple Pickup Watcher

查询 Apple 直营店的到店取货库存，有货时提醒你。提供 **桌面版、CLI 和 agent skill**。

支持 iPhone、iPad、Mac、Apple Watch；覆盖中国大陆、中国香港、中国台湾、日本、新加坡、澳大利亚和马来西亚。
查询失败会显示带原因的「未知」，与「无货」区分。

[项目主页](https://enchigo.github.io/apple-pickup-watcher/) · [English](https://enchigo.github.io/apple-pickup-watcher/en/) · [下载桌面版](https://github.com/ENCHIGO/apple-pickup-watcher/releases/latest) · [CLI 文档](docs/cli.md) · [Agent skill](skills/apple-pickup-watcher/SKILL.md)

## 安装

### 桌面版

从 [Releases](https://github.com/ENCHIGO/apple-pickup-watcher/releases) 下载对应平台的安装包：

| 平台 | 架构 |
| --- | --- |
| macOS | Apple Silicon / Intel |
| Windows | x64 |
| Linux | x86_64 |

macOS 安装包未公证。将应用放进「应用程序」后，如提示无法打开，可执行：

```bash
xattr -cr "/Applications/Apple Pickup Watcher.app"
```

Windows 安装提示、Linux 运行方式见 [桌面版指南](docs/desktop.md)。

### CLI

安装 stable Rust 后，从源码安装 `apw`：

```bash
git clone https://github.com/ENCHIGO/apple-pickup-watcher.git
cd apple-pickup-watcher
cargo install --path crates/apw-cli --locked
```

安装目录通常为 `~/.cargo/bin`，需要在 PATH 中。CLI 无需 Node、桌面环境或音频设备。
预编译包可在 [CLI 构建记录](https://github.com/ENCHIGO/apple-pickup-watcher/actions/workflows/cli.yml) 的成功运行中下载（Artifacts，需要登录 GitHub），包内包含 skill 和文档。

### Agent skill

将仓库中的 [`skills/apple-pickup-watcher`](skills/apple-pickup-watcher/SKILL.md) 目录复制到 agent 的技能目录，并确保 agent 能运行 `apw`。
Codex 的默认技能目录为 `~/.codex/skills`；配置了 `CODEX_HOME` 时使用其中的 `skills` 目录。
已有同名 skill 时先比较内容，安装后在新会话中使用 `$apple-pickup-watcher`。

## 怎么用

### 桌面版

1. 选择地区、品类、门店和型号，添加监控目标。
2. 点击「开始」，保持应用运行、电脑联网且处于唤醒状态；关闭窗口后会收进托盘继续监控。
3. 确认有货时接收系统通知、提示音和可选的 Bark 推送。可按设置打开购物袋，购买操作由你在 Apple 官网完成。

默认每 30 秒查询，最低间隔为 5 秒。有货后继续监控；持续有货不重复提醒，离开有货状态后再次有货才重新提醒。
[Bark、配置文件与通知设置](docs/desktop.md)。

### CLI / agent

```bash
# 查找地区、门店和具体 SKU
apw regions
apw stores --locale zh_CN --search 上海
apw products --locale zh_CN --category iphone --search 512GB

# 示例：指定门店与 SKU 查询一次；请换成你选中的编号
apw check --locale zh_CN --store R359 --part 'MJTF4CH/A' --timeout 60

# 等待任一目标有货，最多运行 5 分钟
apw watch --targets targets.json --until-in-stock --timeout 300
```

`check` 返回 JSON，`watch` 逐行输出 NDJSON；批量目标格式、退出码和超时行为见 [CLI 文档](docs/cli.md)。
`apw schema` 可查看机器可读的接口定义。退出码 0 表示查询成功，**不等于有货**；查询失败、缺失结果和超时不能当作无货。

安装 skill 后，可以直接对 agent 说：

> 用 $apple-pickup-watcher 查一下上海 Apple 直营店的 iPhone 512GB 库存，先列出可选机型让我选。

CLI 和 skill 负责查询与监控，命令退出后监控结束。

## 常见问题 / FAQ

- 安装、HTTP 541、通知和配置问题：[桌面版指南](docs/desktop.md)。
- 批量查询、事件流和 agent 接入：[CLI 文档](docs/cli.md)。
- 开发环境、测试和发布流程：[开发与维护](docs/development.md)。
- 报告问题：[GitHub Issues](https://github.com/ENCHIGO/apple-pickup-watcher/issues)，请附版本、地区、门店/SKU 和脱敏日志。

## 来源与许可

本项目是 [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper) 的 Rust 重写，按 **GPL-3.0-or-later** 发布。许可与来源说明见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。

本项目与 Apple Inc. 无关联。仅供个人查询，不预留库存；最终库存与取货信息以 Apple 官网为准。
