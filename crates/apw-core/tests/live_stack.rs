//! 贯穿全栈的真实链路测试：目录 → 真实零件号 → Apple 接口 → 调度引擎 → 事件。
//!
//! 与 `live.rs` 的分工：那个只验证 HTTP 客户端与响应解析；这个把 catalog 和
//! watcher 也串进来，走的是应用真正会走的那条路 —— 从内嵌目录里取出真实的门店号
//! 与零件号，交给引擎去跑，看它能不能拿到明确答复。
//!
//! 默认不参与 `cargo test`，需要显式启用：
//!
//! ```shell
//! cargo test -p apw-core --features live --test live_stack -- --nocapture
//! ```

#![cfg(feature = "live")]

use std::time::Duration;

use apw_core::apple::{AppleClient, ClientConfig};
use apw_core::catalog::Catalog;
use apw_core::model::{Availability, Target, UnknownReason, region_by_locale};
use apw_core::watcher::{Event, Watcher, WatcherConfig};

/// 从内嵌目录里挑出真实的门店与型号，跑一轮完整监控。
///
/// 断言的重点不是「有货」还是「无货」（那取决于当下库存），而是**必须拿到明确
/// 答复**。只要出现 Unknown 且原因不是「尚未查询」，就说明这条链路上有一环断了 ——
/// 而这正是上游失效时用户看到的样子：程序在跑，只是永远给不出真答案。
#[tokio::test(flavor = "multi_thread")]
async fn 全栈跑通一轮真实监控() {
    let catalog = Catalog::new();

    let stores = catalog.stores("zh_CN").expect("内嵌门店目录应当可用");
    let products = catalog.products("zh_CN").expect("内嵌商品目录应当可用");
    assert!(!stores.is_empty(), "门店目录是空的");
    assert!(!products.is_empty(), "商品目录是空的");

    // 取前两家门店、各盯两个型号：既覆盖「按门店合并请求」，也覆盖多门店并发。
    let picked_stores = &stores[..stores.len().min(2)];
    let picked_products = &products[..products.len().min(2)];

    let mut targets = Vec::new();
    for store in picked_stores {
        for product in picked_products {
            targets.push(Target {
                locale: "zh_CN".into(),
                store_number: store.number.clone(),
                store_title: store.title.clone(),
                part_number: product.part_number.clone(),
                product_name: product.title.clone(),
            });
        }
    }
    println!("准备监控 {} 项：", targets.len());
    for t in &targets {
        println!("  {} / {}", t.store_title, t.product_name);
    }

    let client = AppleClient::new(ClientConfig {
        // 别把真实接口当压测目标。
        min_interval: Duration::from_secs(2),
        ..ClientConfig::default()
    })
    .expect("构造客户端失败");

    let (watcher, mut events) = Watcher::spawn(
        client,
        WatcherConfig {
            interval: Duration::from_secs(60),
            jitter: 0.0,
            ..WatcherConfig::default()
        },
    );
    watcher.set_targets(targets.clone()).await;
    watcher.start().await;

    // 等一轮结束。
    let deadline = tokio::time::Instant::now() + Duration::from_secs(90);
    let mut troubles = Vec::new();
    let healthy = loop {
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        assert!(!remaining.is_zero(), "等一轮真实查询结束超时");
        match tokio::time::timeout(remaining, events.recv()).await {
            Ok(Some(Event::CycleComplete { healthy, .. })) => break healthy,
            Ok(Some(Event::Trouble { reason })) => troubles.push(reason),
            Ok(Some(_)) => {}
            Ok(None) => panic!("事件流意外关闭"),
            Err(_) => panic!("等一轮真实查询结束超时"),
        }
    };
    watcher.stop().await;

    let snapshot = watcher.snapshot().await;
    assert_eq!(snapshot.len(), targets.len(), "快照条数与监控目标数不符");

    println!("\n本轮结果：");
    let mut undecided = Vec::new();
    for state in &snapshot {
        println!(
            "  {:5} {} / {}",
            state.availability.label(),
            state.target.store_title,
            state.target.product_name
        );
        match &state.availability {
            Availability::InStock | Availability::OutOfStock => {}
            Availability::Unknown(UnknownReason::NotYetChecked) => {
                undecided.push(format!("{} 竟然没被查询", state.target.product_name));
            }
            Availability::Unknown(reason) => {
                undecided.push(format!(
                    "{} / {}：{}",
                    state.target.store_title,
                    state.target.product_name,
                    reason.describe()
                ));
            }
        }
    }

    if !troubles.is_empty() {
        println!("\n告警：");
        for t in &troubles {
            println!("  {t}");
        }
    }

    assert!(
        undecided.is_empty(),
        "有 {} 项没能拿到明确答复，说明链路上有一环断了：\n  {}",
        undecided.len(),
        undecided.join("\n  ")
    );
    assert!(healthy, "本轮未被判定为健康，告警：{troubles:?}");
    println!("\n全部 {} 项都拿到了明确答复。", snapshot.len());
}

/// 目录的在线刷新是否真的能从 Apple 官网抓到型号。
///
/// 这条路径决定了新机发布后用户能不能自己更新型号列表，而不必等作者发版 ——
/// 上游作者停更之后，那份手工维护的目录就永远停在了旧机型上。
#[tokio::test(flavor = "multi_thread")]
async fn 能从官网抓到最新型号() {
    let catalog = Catalog::new();
    let region = region_by_locale("zh_CN").expect("地区表里应当有中国大陆");
    let before = catalog.products("zh_CN").map(|p| p.len()).unwrap_or(0);

    let http = reqwest::Client::builder()
        .timeout(Duration::from_secs(30))
        .build()
        .expect("构造 http 客户端失败");

    let count = catalog
        .refresh_products(region, &http)
        .await
        .unwrap_or_else(|e| panic!("在线刷新失败，购买页结构可能已变：{e}"));

    assert!(count > 0, "在线刷新返回 0 个型号");
    let after = catalog.products("zh_CN").expect("刷新后目录应当可用");
    println!(
        "内嵌 {before} 个型号，从官网抓到 {count} 个，刷新后 {} 个",
        after.len()
    );
    for p in after.iter().take(5) {
        println!("  {}  {}", p.part_number, p.title);
    }
    assert!(!after.is_empty());
}
