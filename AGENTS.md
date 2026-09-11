# Repository guidance

Apple Pickup Watcher 是 Rust + Tauri v2 + React/TypeScript 桌面应用。
项目行为见 `README.md`，系统依赖和检查流程以 `.github/workflows/ci.yml` 为准。

## Structure

- `crates/apw-core/`：库存类型、Apple 客户端、商品目录、监控调度、通知与配置持久化。
- `crates/apw-cli/`：独立 `apw` CLI，JSON/NDJSON 接口；`skills/apple-pickup-watcher/`：配套 agent skill。
- `src-tauri/`：桌面装配、IPC、托盘、系统通知和购物袋打开。
- `src/`：React 界面与前端状态；`tests/`：前端交互及异步状态回归。
- `crates/apw-core/data/`：离线快照及其生成脚本。

## Development and validation

使用 stable Rust（最低版本见 `Cargo.toml`）、Node.js LTS 和 pnpm 9。
安装前端依赖：`pnpm install --frozen-lockfile`。
启动桌面应用：`pnpm tauri dev`；打包：`pnpm tauri build`。
Codex 云端 Ubuntu 环境可用 `bash scripts/codex-setup.sh` 安装开发依赖。

按改动范围执行相关检查，完整日常 CI 检查为：

```sh
cargo fmt --all --check
python3 crates/apw-core/data/generate.py --self-test
pnpm exec tsc --noEmit
pnpm test
pnpm exec vite build
cargo clippy --all-targets --all-features -- -D warnings
cargo test
```

常规测试保持离线：`cargo test` 不加 `--all-features` 或 `--features live`。
`live` 和 `live_stack` 测试会访问 Apple；日常 PR 检查不执行它们。
Clippy 的 `--all-features` 只编译，可以保留。
`generate.py` 不带 `--self-test` 时会联网并改写离线快照。

## Code Review Rules

- 库存判定留在 `apw-core`。请求失败、缺失或无法识别的结果必须是带原因的 `Unknown`，不能变成 `OutOfStock`；界面须区分未知、待查询和无货。
- 到货监控和通知在 Rust 后台执行，窗口隐藏时仍须工作。持续有货不重复提醒，离开有货后再次有货才重新提醒；通知渠道失败不得改写库存状态。
- 保留配置保护：`settings.v2.json` 与旧 `settings.json` 分离，迁移不修改旧文件；损坏配置应留档并显示原因，不能被默认配置覆盖。写入保持原子性。

审查聚焦可证实的行为缺陷，并说明触发条件和影响；格式、类型与 lint 检查交给 CI。
