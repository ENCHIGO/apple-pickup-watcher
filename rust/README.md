# Rust + Tauri 重写（进行中）

这个目录是 Apple Pickup Watcher 的 Rust + Tauri 重写版，对应分支 `rust-tauri`。
仓库根目录的 Go + Fyne 版本仍是当前发布的版本（`main` 分支，最新 v0.1.1），
在这里达到功能对等之前不会动它。

达到对等后的计划：把 `rust/` 提到仓库根目录、删掉 Go 代码、发 v0.2.0。
在那之前 Go 代码留着作对照 —— 里面有大量以 `文件:行号` 形式记录下来的
上游缺陷说明，那是这个项目最值钱的部分之一，不能随手扔掉。

## 为什么要重写

不是因为 Rust 更快。这个程序每 30 秒发几个 HTTP 请求，性能从来不是瓶颈。

真正的理由有两个：

**一、Fyne 的界面拿不出手。** 对一个用户在 iPhone 发售当晚盯着抢货的工具来说，
界面质量是实打实的产品问题。附带好处是发布体积从 38 MB 降到 8 MB 量级，
以及能做系统托盘常驻 —— 后者对一个需要长时间挂着的工具是实打实的体验升级。

**二、编译器能替我们守住那条唯一重要的不变量。**

> 任何「查不到 / 判不了」的情况都必须表现为 `Unknown`，绝不能变成 `OutOfStock`。

上游 hteen/apple-store-helper 正是在这条线上失守：它把请求失败折叠成「无货」，
Apple 换掉接口之后静默失效了大半年 —— 界面一切正常，只是永远显示无货。

Go 版靠三态枚举 + 一个独立的 `LastError` 字段 + 一堆测试来守这条线。问题是状态
和原因是两个可以不同步的字段，独立审查找出的两条最严重的缺陷，根子都在这种
不同步上。Rust 里可以让它们不可分割：

```rust
pub enum Availability {
    InStock,
    OutOfStock,
    Unknown(UnknownReason),   // 未知必然带着原因，构造不出「无原因的未知」
}
```

再配合「不实现 `Default`」和「`ApiError` 到状态的转换全覆盖且单向」，
**「把失败悄悄当成无货」这件事在类型层面就写不出来**。

## 当前进度

- [x] `apw-core::model` —— 三态类型、地区表、目标与键
- [x] `apw-core::apple` —— HTTP 客户端、限速、退避重试、错误四分类、响应解析
- [x] 单元测试与针对 Apple 真实接口的契约测试
- [ ] `apw-core::catalog` —— 商品与门店目录（内嵌兜底 + 在线刷新）
- [ ] `apw-core::watcher` —— 调度引擎
- [ ] `apw-core::notify` —— Bark / 提示音 / 系统通知
- [ ] `apw-core::config` —— 设置持久化（含读取 Go 版 settings.json 做迁移）
- [ ] Tauri 应用外壳与前端
- [ ] 系统托盘、自动更新

## 开发

```shell
cd rust
cargo test                    # 单元测试，不碰网络
cargo clippy --all-targets --all-features -- -D warnings
cargo fmt --all
```

针对 Apple 真实接口的契约测试默认不跑，需要显式启用：

```shell
cargo test -p apw-core --features live --test live -- --nocapture --test-threads=1
```

它会依次查询七个地区的真实门店，用来确认接口没被换掉、响应结构没变、
以及 `pickupDisplay` 没有出现我们不认识的新取值。建议按天定时跑，
不要每次提交都跑 —— 没必要给 Apple 添麻烦。

## 许可

与主项目一致，GPL-3.0。本项目是
[hteen/apple-store-helper](https://github.com/hteen/apple-store-helper)（GPL-3.0）
的修改版本，详见仓库根目录的 `LICENSE` 与 `NOTICE`。
