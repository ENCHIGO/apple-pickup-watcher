# 开发与维护

[返回 README](../README.md) · [CLI 构建与接口](cli.md)

## 工具链与系统依赖

使用 stable Rust，最低版本见根目录 [Cargo.toml](../Cargo.toml)。桌面前端使用 Node.js LTS 和 pnpm 9。

桌面版的系统依赖以 [CI](../.github/workflows/ci.yml) 和 [Release 工作流](../.github/workflows/release.yml) 为准：macOS 需要 Xcode Command Line Tools；Windows 需要 Microsoft C++ 生成工具和 WebView2；Linux 需要 WebKitGTK、托盘和 ALSA 等开发库。Ubuntu 开发环境可使用 [scripts/codex-setup.sh](../scripts/codex-setup.sh)。

CLI 单独构建不启用通知 feature，不需要桌面或音频依赖。

## 开发与检查

```bash
pnpm install --frozen-lockfile
pnpm tauri dev
pnpm tauri build
```

按改动范围执行检查；完整日常检查与 CI 一致：

```bash
cargo fmt --all --check
python3 crates/apw-core/data/generate.py --self-test
python3 -m unittest discover -s scripts -p 'test_*.py'
pnpm exec tsc --noEmit
pnpm test
pnpm exec vite build
cargo clippy --all-targets --all-features -- -D warnings
cargo test
```

普通测试保持离线，不为 `cargo test` 添加 `--all-features` 或 `--features live`。Clippy 的 `--all-features` 只编译，不执行真实请求。

## 代码结构

| 目录 | 职责 |
| --- | --- |
| `crates/apw-core/` | 库存类型、Apple 客户端、目录、监控引擎、通知与配置 |
| `crates/apw-cli/` | `apw` 命令、JSON/NDJSON 输出、目标输入与进程生命周期 |
| `src-tauri/` | 桌面装配、IPC、托盘、系统通知与购物袋打开 |
| `src/`、`tests/` | React 界面、前端状态及交互测试 |
| `skills/apple-pickup-watcher/` | 配套 agent skill |
| `site/` | 中英文项目介绍页，见 [站点维护说明](discoverability.md) |

库存判定留在 `apw-core`，失败必须保留为带原因的 `Unknown`。通知失败不得改写库存；配置迁移不得修改旧文件，损坏配置不得被默认值覆盖。完整约定见 [AGENTS.md](../AGENTS.md)。

## 真实接口与目录快照

[CI](../.github/workflows/ci.yml) 的单独任务按天运行真实接口契约测试。需要手动验证时：

```bash
cargo test -p apw-core --features live --test live -- --nocapture --test-threads=1
```

该命令会访问 Apple，检查真实响应与 HTTP 协商。型号过期时更新测试目标，测试结果仅代表对应时间和网络下的观察。

离线快照生成器不带 `--self-test` 时会联网并改写数据：

```bash
python3 crates/apw-core/data/generate.py
```

新增购买页或适配页面结构后再按需更新快照，检查是否存在抓取失败和缺页。

## 提交与发布

`main` 按仓库分支保护规则通过 PR 更新；当前要求 Linux 和 macOS 检查通过。规则以 GitHub 设置为准，不为普通发布关闭保护。

桌面版本更新需同步 `Cargo.toml`、工作区 crate 的 `Cargo.lock` 版本、`package.json` 和 `src-tauri/tauri.conf.json`。`v*` 标签触发桌面打包并创建 Release 草稿，手动触发仅构建。所有平台产物就绪并核对后再发布草稿。

桌面更新签名使用 `TAURI_SIGNING_PRIVATE_KEY` 和 `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` 两个仓库 Secret。缺少密钥时工作流按配置输出未签名安装包；应用内更新需要与内嵌公钥匹配的签名。私钥应离线备份，不提交到仓库。

[CLI 工作流](../.github/workflows/cli.yml) 独立构建 macOS Apple Silicon / Intel、Windows x64 和 Linux x86_64，上传包含 CLI、skill、文档、许可及校验和的 Actions artifacts。它不自动创建公开 Release。

发布后的版本分支由 [Release branches 工作流](../.github/workflows/release-branches.yml) 维护。站点部署与桌面发布彼此独立。
