//! 跨 IPC 边界的线上格式契约。
//!
//! 这些用例钉住的是「Rust 序列化出来长什么样」。前端的 TypeScript 类型是照着
//! 这个形状手写的，一旦有人调整了 serde 标注，这里会立刻变红 —— 而不是等到
//! 界面上莫名其妙少了一列、或者状态永远显示不出来才发现。
//!
//! 尤其是 `Availability`：它是内部标签枚举套内部标签枚举，会扁平成两层判别字段
//! （`kind` + `reason`）。这个形状正好能映射成 TypeScript 的可辨识联合，配合
//! `switch` 的穷尽性检查，Rust 这边加一个状态、前端漏处理就编译不过。这是刻意
//! 设计的，不是巧合，所以必须钉住。

use apw_core::model::{Availability, Category, Product, Store, Target, UnknownReason};
use serde_json::json;

fn to_value<T: serde::Serialize>(v: &T) -> serde_json::Value {
    serde_json::to_value(v).expect("序列化失败")
}

#[test]
fn 可用状态的线上格式() {
    assert_eq!(
        to_value(&Availability::InStock),
        json!({"kind": "in_stock"})
    );
    assert_eq!(
        to_value(&Availability::OutOfStock),
        json!({"kind": "out_of_stock"})
    );
}

#[test]
fn 未知状态会扁平成两层判别字段() {
    let cases: Vec<(UnknownReason, serde_json::Value)> = vec![
        (
            UnknownReason::NotYetChecked,
            json!({"kind": "unknown", "reason": "not_yet_checked"}),
        ),
        (
            UnknownReason::Blocked {
                detail: "HTTP 541".into(),
            },
            json!({"kind": "unknown", "reason": "blocked", "detail": "HTTP 541"}),
        ),
        (
            UnknownReason::RateLimited,
            json!({"kind": "unknown", "reason": "rate_limited"}),
        ),
        (
            UnknownReason::SchemaDrift {
                field: "pickupDisplay".into(),
                raw: "weird".into(),
            },
            json!({"kind": "unknown", "reason": "schema_drift", "field": "pickupDisplay", "raw": "weird"}),
        ),
        (
            UnknownReason::AppleError {
                message: "boom".into(),
            },
            json!({"kind": "unknown", "reason": "apple_error", "message": "boom"}),
        ),
        (
            UnknownReason::Transport {
                detail: "timeout".into(),
            },
            json!({"kind": "unknown", "reason": "transport", "detail": "timeout"}),
        ),
    ];

    for (reason, want) in cases {
        assert_eq!(
            to_value(&Availability::Unknown(reason.clone())),
            want,
            "{reason:?} 的线上格式变了，前端的 TypeScript 类型需要同步更新"
        );
    }
}

#[test]
fn 跨边界的结构统一用小驼峰() {
    // 混用 snake_case 和 camelCase 会让前端每个类型都要单独记住用哪种，
    // 迟早写错。统一成小驼峰，符合 TypeScript 的习惯。
    let target = Target {
        locale: "zh_CN".into(),
        store_number: "R683".into(),
        store_title: "上海-环球港".into(),
        part_number: "MG724CH/A".into(),
        product_name: "iPhone 17 512GB 黑色".into(),
    };
    assert_eq!(
        to_value(&target),
        json!({
            "locale": "zh_CN",
            "storeNumber": "R683",
            "storeTitle": "上海-环球港",
            "partNumber": "MG724CH/A",
            "productName": "iPhone 17 512GB 黑色"
        })
    );

    let product = Product {
        part_number: "MG724CH/A".into(),
        category: Category::Iphone,
        family: "iphone17".into(),
        capacity: "512GB".into(),
        color: "黑色".into(),
        title: "iPhone 17 512GB 黑色".into(),
    };
    let v = to_value(&product);
    assert!(v.get("partNumber").is_some(), "Product 应当用小驼峰：{v}");
    // 品类是个纯字符串标签，前端照着它筛下拉框。写成对象或者数字，
    // TypeScript 那边的联合类型就对不上了。
    assert_eq!(v.get("category"), Some(&json!("iphone")));

    let store = Store {
        number: "R683".into(),
        name: "环球港".into(),
        title: "上海-环球港".into(),
    };
    assert!(to_value(&store).get("number").is_some());
}

#[test]
fn 品类的线上格式是小写标识符() {
    // 前端的 Category 联合类型逐字写着这四个串。谁改了 Rust 的 serde 标注，
    // 这里会先红，而不是等界面上品类下拉框选完什么都筛不出来才发现。
    for (category, want) in [
        (Category::Iphone, "iphone"),
        (Category::Ipad, "ipad"),
        (Category::Mac, "mac"),
        (Category::Watch, "watch"),
    ] {
        assert_eq!(to_value(&category), json!(want));
        let back: Category = serde_json::from_value(json!(want)).expect("前端传回来的品类必须认得");
        assert_eq!(back, category);
    }
}

#[test]
fn 监控目标能原样往返() {
    // 前端会把 Target 发回来（set_targets），所以它必须是可往返的。
    let target = Target {
        locale: "ja_JP".into(),
        store_number: "R119".into(),
        store_title: "東京-渋谷".into(),
        part_number: "MG6A4J/A".into(),
        product_name: "iPhone 17 256GB ラベンダー".into(),
    };
    let json = serde_json::to_string(&target).unwrap();
    let back: Target = serde_json::from_str(&json).expect("反序列化失败");
    assert_eq!(target, back);
}
