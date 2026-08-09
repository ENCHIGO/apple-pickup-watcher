//! Apple 直营店到店取货库存监控的核心逻辑。
//!
//! 这个 crate 不依赖 Tauri、也不认识任何界面框架，因此可以脱离应用单独测试 ——
//! 包括针对 Apple 真实接口的契约测试（`--features live`）。
//!
//! 本项目是 [hteen/apple-store-helper](https://github.com/hteen/apple-store-helper)
//! （GPL-3.0，已停止维护）的重写版本，同样以 GPL-3.0 发布。
//!
//! # 唯一真正重要的不变量
//!
//! **任何「查不到 / 判不了」的情况都必须表现为 [`model::Availability::Unknown`]，
//! 绝不能变成 [`model::Availability::OutOfStock`]。**
//!
//! 上游正是因为把请求失败折叠成「无货」，在 Apple 换掉接口之后静默失效了大半年：
//! 界面一切正常，只是永远显示无货 —— 这比直接报错更容易让人错过购买时机。
//!
//! 用 Rust 重写的首要理由就是把这条线交给编译器：`Unknown` 必须携带原因、
//! `Availability` 没有 `Default`、[`apple::ApiError`] 到状态的转换是全覆盖且单向的，
//! 「把失败悄悄当成无货」在类型层面就写不出来。

pub mod apple;
pub mod apple_catalog;
pub mod catalog;
pub mod config;
pub mod model;
pub mod notify;
pub mod watcher;
