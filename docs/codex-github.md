# Codex GitHub integration

目标仓库：`ENCHIGO/apple-pickup-watcher`。使用 Codex 官方 GitHub 集成。
`AGENTS.md` 提供项目规则；合并这些文件不会自动安装 GitHub App 或开启云端审查。

## 仓库授权与环境

1. 登录 [Codex](https://chatgpt.com/codex)，连接 GitHub，授权这个仓库。
2. 在[环境设置](https://chatgpt.com/codex/settings/environments)为此仓库创建环境，选择默认 universal 镜像和受支持的 Node.js LTS 版本。
3. 在环境的 Setup script 中填写：

   ```sh
   bash scripts/codex-setup.sh
   ```

4. 可选的 Maintenance script 用于恢复缓存后同步锁文件依赖：

   ```sh
   pnpm install --frozen-lockfile
   cargo fetch --locked
   ```

Setup script 需要先合并到默认分支，再运行环境初始化。脚本安装 CI 使用的 Linux
系统依赖、stable Rust 和 pnpm 9，并预取依赖。无需配置 Apple、Bark 或发布签名凭据。
保留 agent 阶段默认的关闭联网设置即可进行离线开发与测试；依赖安装发生在联网的 setup 阶段。

## PR 审查

在[代码审查设置](https://chatgpt.com/codex/settings/code-review)为这个仓库开启
Code review 和 Automatic reviews。`AGENTS.md` 的 `Code Review Rules` 定义审查重点。

手动审查时，在 PR 评论里输入：

```text
@codex review
```

需要修复 CI 时可以输入：

```text
@codex fix the CI failures
```

## 验证

- 环境初始化成功，`pnpm exec tsc --noEmit`、`pnpm exec vite build` 和 `cargo test` 可以执行；不添加 `--features live`。
- 新建非草稿 PR 后收到 Codex 审查，或手动触发后收到机器人响应和审查结果。
- 检查机器人审查对应当前 PR 的提交；GitHub 分支保护和现有 CI 继续生效。

参考：[GitHub 集成](https://learn.chatgpt.com/docs/third-party/github)、[云端环境](https://learn.chatgpt.com/docs/environments/cloud-environment)。
