//! 为浏览器扩展生成一条精确到 SKU 与门店的 Apple 官方购买地址。
//!
//! 真正的页面操作由浏览器扩展完成；桌面应用不接触 Apple 账号、密码、验证码或
//! 支付信息。当前只为已经核实过 URL 结构的 iPhone 18 Pro 系列与 iPhone Duo 生成地址，认不出
//! 的型号宁可退回购物袋，也不把用户送到错误商品。

use apw_core::model::{Target, region_by_locale};

pub(crate) fn url_for(target: &Target) -> Option<String> {
    let region = region_by_locale(&target.locale)?;
    let product = target.product_name.to_ascii_lowercase();
    let family = if product.contains("iphone 18 pro") {
        "iphone-18-pro"
    } else if product.contains("iphone duo") {
        "iphone-duo"
    } else {
        return None;
    };

    let part = target.part_number.trim().to_ascii_lowercase();
    let store = target.store_number.trim();
    if !valid_part_number(&part) || !valid_store_number(store) {
        return None;
    }

    let store_title = percent_encode_query(target.store_title.trim());
    Some(format!(
        "{}/shop/buy-iphone/{family}/{part}?apwAutoCheckout=1&apwStore={store}&apwStoreTitle={store_title}",
        region.base_url
    ))
}

fn valid_part_number(value: &str) -> bool {
    !value.is_empty()
        && value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'/' | b'-'))
}

fn valid_store_number(value: &str) -> bool {
    !value.is_empty()
        && value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-')
}

fn percent_encode_query(value: &str) -> String {
    const HEX: &[u8; 16] = b"0123456789ABCDEF";
    let mut out = String::with_capacity(value.len());
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            out.push(char::from(byte));
        } else {
            out.push('%');
            out.push(char::from(HEX[usize::from(byte >> 4)]));
            out.push(char::from(HEX[usize::from(byte & 0x0f)]));
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target(name: &str, part: &str, store: &str) -> Target {
        Target {
            locale: "zh_CN".into(),
            store_number: store.into(),
            store_title: "浙江-杭州万象城".into(),
            part_number: part.into(),
            product_name: name.into(),
        }
    }

    #[test]
    fn 生成精确型号与门店的官方购买地址() {
        let url = url_for(&target(
            "iPhone 18 Pro Max 1TB 勃艮第酒红色",
            "MJYH4CH/A",
            "R532",
        ))
        .expect("已支持的型号应有地址");
        assert_eq!(
            url,
            "https://www.apple.com.cn/shop/buy-iphone/iphone-18-pro/mjyh4ch/a?apwAutoCheckout=1&apwStore=R532&apwStoreTitle=%E6%B5%99%E6%B1%9F-%E6%9D%AD%E5%B7%9E%E4%B8%87%E8%B1%A1%E5%9F%8E"
        );
    }

    #[test]
    fn 未核实型号与不安全标识都拒绝自动导航() {
        assert!(url_for(&target("iPhone 17 Pro 512GB 黑色", "MG724CH/A", "R532")).is_none());
        assert!(url_for(&target("iPhone 18 Pro 256GB 黑色", "MJT74CH/A", "R532&x=1")).is_none());
        assert!(url_for(&target("iPhone 18 Pro 256GB 黑色", "../bad", "R532")).is_none());
    }

    #[test]
    fn 同样支持官网已经公布的iphone_duo购买页() {
        let url = url_for(&target("iPhone Duo 512GB 星光白色", "MK2P4CH/A", "R532"))
            .expect("Duo 已有正式购买页");
        assert!(url.contains("/shop/buy-iphone/iphone-duo/mk2p4ch/a?"));
    }
}
