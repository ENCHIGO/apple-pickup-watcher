//! 商品与门店目录。
//!
//! 目录有两个来源：随二进制内嵌的离线快照，以及运行时从 Apple 购买页抓来的
//! 最新数据。内嵌快照保证程序在断网或被拦截时仍然可用，在线抓取保证新机发布
//! 当天就能盯，不必等一个新版本。
//!
//! 内嵌用的是 `include_str!`：数据在编译期就进了二进制。上游是运行时按相对
//! 路径去读工作目录下的 `config/*.json`，打包成 .app 之后工作目录未必是仓库
//! 目录，读不到文件就在 `services/store.go:22` 直接 panic 崩掉整个程序。
//!
//! # 这里也有一条「查不到不等于没有」
//!
//! 目录本身不判定库存，但它离那条不变量只有一步：[`Catalog::product_by_part`]
//! 与 [`Catalog::store_by_number`] 找不到时返回 `None`，调用方**必须**把它当成
//! 「这一项暂时说不清」，而不是悄悄删掉监控项或把它显示成无货。上游在这两个
//! 位置（`services/product.go:19`、`services/store.go:22`）用的是
//! `funk.Find(...).(model.Product)` 这样的硬类型断言：找不到时对 nil 接口做
//! 断言，直接 panic 掉整个程序 —— 用户选完商品后正好赶上一次目录刷新、旧型号
//! 不复存在，就足以触发。

use std::collections::{HashMap, HashSet};
use std::sync::{PoisonError, RwLock, RwLockReadGuard, RwLockWriteGuard};

use serde::Deserialize;

use crate::apple::ApiError;
use crate::model::{Product, Region, Store};

/// 目录相关的失败。
///
/// 刻意分成「内置数据坏了」「地区不存在」「线上抓取失败」几类而不是糊成一个
/// 字符串：界面对它们的处置完全不同 —— 前两者用户重试多少次都没用，最后一种
/// 稍后再试就好。
#[derive(Debug, thiserror::Error)]
pub enum CatalogError {
    /// 内置数据里没有这个地区。
    #[error("内置数据里没有地区 {locale} 的{kind}目录")]
    UnknownLocale { locale: String, kind: &'static str },

    /// 内置数据解析失败。只可能是数据文件本身写坏了，与运行环境无关。
    #[error("内置数据 {file} 解析失败：{detail}")]
    Corrupt { file: String, detail: String },

    /// 抓取购买页失败，已按 [`ApiError`] 的口径分类。
    #[error("抓取购买页失败：{0}")]
    Fetch(#[from] ApiError),

    /// 页面拿到了，但里面的商品数据结构与预期不符。
    #[error("购买页结构与预期不符：{detail}")]
    PageSchema { detail: String },

    /// 该地区没有配置任何可抓取的机型。
    #[error("地区 {locale} 没有配置任何机型")]
    NoFamilies { locale: String },

    /// 刷新时有机型失败。
    ///
    /// `fetched` 是成功抓到并**已经写进缓存**的型号数：为 0 表示这次刷新毫无
    /// 产出，缓存保持原样。返回错误的同时缓存已被更新，是刻意的 —— iPhone Air
    /// 抓不到不该导致连 iPhone 17 都选不了。
    #[error("地区 {locale} 有 {} 个机型刷新失败（已抓到 {fetched} 个型号）：{}",
            .failures.len(), .failures.join("；"))]
    RefreshFailed {
        locale: String,
        fetched: usize,
        failures: Vec<String>,
    },
}

/// 内嵌的离线商品快照。
///
/// 用 `include_str!` 而不是运行时读文件：打包后那些相对路径根本不存在。
/// 表要手写是因为 `include_str!` 的路径必须是字面量；漏了哪个地区由测试兜住。
const EMBEDDED_PRODUCTS: &[(&str, &str)] = &[
    ("zh_CN", include_str!("../data/products_zh_CN.json")),
    ("zh_HK", include_str!("../data/products_zh_HK.json")),
    ("zh_TW", include_str!("../data/products_zh_TW.json")),
    ("ja_JP", include_str!("../data/products_ja_JP.json")),
    ("en_SG", include_str!("../data/products_en_SG.json")),
    ("en_AU", include_str!("../data/products_en_AU.json")),
    ("en_MY", include_str!("../data/products_en_MY.json")),
];

/// 内嵌的离线门店快照。所有地区都在这一个文件里。
const EMBEDDED_STORES: &str = include_str!("../data/stores.json");

const STORES_FILE: &str = "stores.json";

fn products_file(locale: &str) -> String {
    format!("products_{locale}.json")
}

/// 商品与门店目录，可以直接放进 Tauri 的共享状态。
///
/// 全部方法收 `&self`：读路径（界面每次打开下拉框）与写路径（后台刷新）会同时
/// 发生。上游是无锁共享 map（`services/store.go:56` 在后台写 `s.stores`，界面
/// 同时在读），在 Go 里会触发 `fatal error: concurrent map read and map write`，
/// 连 recover 都捕获不到。
///
/// 只有在线刷新结果需要加锁：离线快照在 [`Catalog::new`] 里一次解析完就再也不
/// 变，读它完全不需要同步。
#[derive(Debug)]
pub struct Catalog {
    /// 内嵌快照的解析结果，按地区。
    ///
    /// 解析失败的地区留下错误说明而不是直接丢掉，否则用户只会看到「没有这个
    /// 地区」，永远不知道是数据坏了。存 `String` 而不是 `CatalogError` 是因为
    /// 它要被反复返回，而 `CatalogError` 携带了不可克隆的 [`ApiError`]。
    offline_products: HashMap<&'static str, Result<Vec<Product>, String>>,
    /// 门店快照。门店变动远慢于商品，没有在线刷新这条路。
    offline_stores: Result<HashMap<String, Vec<Store>>, String>,
    /// 在线刷新结果，按地区覆盖离线快照。
    online_products: RwLock<HashMap<String, Vec<Product>>>,
}

impl Default for Catalog {
    fn default() -> Self {
        Self::new()
    }
}

impl Catalog {
    /// 载入内嵌数据。
    ///
    /// 全部地区在这里一次解析完（合计一百多万字节 JSON，几毫秒的事），而不是
    /// 惰性解析：一次性算完之后，离线部分就是一份彻底不可变的数据，读路径无锁、
    /// 也不存在「首次访问时卡在界面线程上」和惰性初始化的重复解析问题。
    ///
    /// 数据坏了也不会失败：那是「这个地区暂时选不了」，不是「整个程序应当立刻
    /// 死掉」，错误留到真正去读那个地区时再报。
    pub fn new() -> Self {
        let offline_products = EMBEDDED_PRODUCTS
            .iter()
            .map(|(locale, text)| {
                (
                    *locale,
                    load_products(locale, text).map_err(|e| e.to_string()),
                )
            })
            .collect();

        Self {
            offline_products,
            offline_stores: load_stores(EMBEDDED_STORES).map_err(|e| e.to_string()),
            online_products: RwLock::new(HashMap::new()),
        }
    }

    /// 某地区的全部商品。
    ///
    /// 有在线刷新结果时优先返回它，否则回退到内嵌快照。
    pub fn products(&self, locale: &str) -> Result<Vec<Product>, CatalogError> {
        if let Some(fresh) = self.online_products(locale) {
            return Ok(fresh);
        }
        match self.offline_products.get(locale) {
            Some(Ok(products)) => Ok(products.clone()),
            Some(Err(detail)) => Err(CatalogError::Corrupt {
                file: products_file(locale),
                detail: detail.clone(),
            }),
            None => Err(CatalogError::UnknownLocale {
                locale: locale.to_string(),
                kind: "商品",
            }),
        }
    }

    /// 某地区的全部直营店。
    pub fn stores(&self, locale: &str) -> Result<Vec<Store>, CatalogError> {
        let by_locale = self
            .offline_stores
            .as_ref()
            .map_err(|detail| CatalogError::Corrupt {
                file: STORES_FILE.to_string(),
                detail: detail.clone(),
            })?;
        by_locale
            .get(locale)
            .cloned()
            .ok_or_else(|| CatalogError::UnknownLocale {
                locale: locale.to_string(),
                kind: "门店",
            })
    }

    /// 按零件号查商品。
    ///
    /// 返回 `Option` 而不是 panic —— 见模块文档里上游那两处硬类型断言。
    /// **找不到不代表这个型号没货**，只代表本地目录里没有它：调用方该提示用户
    /// 目录可能已经过期，而不是把这一项当成无货或者直接删掉。
    pub fn product_by_part(&self, locale: &str, part: &str) -> Option<Product> {
        self.products(locale)
            .ok()?
            .into_iter()
            .find(|p| p.part_number == part)
    }

    /// 按门店编号查门店，语义同 [`Catalog::product_by_part`]。
    pub fn store_by_number(&self, locale: &str, number: &str) -> Option<Store> {
        self.stores(locale)
            .ok()?
            .into_iter()
            .find(|s| s.number == number)
    }

    /// 从 Apple 官网购买页抓最新型号，覆盖该地区的内存副本。返回抓到的型号数。
    ///
    /// 逐个机型抓取再合并。某个机型失败时保留其余机型的结果并返回
    /// [`CatalogError::RefreshFailed`] —— iPhone Air 抓不到不该导致连 iPhone 17
    /// 都选不了，但也不能装作一切正常，用户得知道自己看到的目录是不全的。
    ///
    /// `http` 必须是宿主长期持有的那一个客户端。上游 `services/listen.go:221`
    /// 每次请求都新建客户端，连接池无法复用，内存能涨到十几 GB。
    pub async fn refresh_products(
        &self,
        region: &'static Region,
        http: &reqwest::Client,
    ) -> Result<usize, CatalogError> {
        if region.families.is_empty() {
            return Err(CatalogError::NoFamilies {
                locale: region.locale.to_string(),
            });
        }

        let mut merged: Vec<Product> = Vec::new();
        let mut seen: HashSet<String> = HashSet::new();
        let mut failures: Vec<String> = Vec::new();

        for family in region.families {
            match crate::apple_catalog::fetch_products(http, region, family).await {
                Ok(items) => {
                    for product in items {
                        // 不同机型页之间也可能返回同一零件号（Pro 与 Pro Max 共页）。
                        if seen.insert(product.part_number.clone()) {
                            merged.push(product);
                        }
                    }
                }
                Err(err) => failures.push(format!("{family}：{err}")),
            }
        }

        let fetched = merged.len();
        self.install_products(region.locale, merged);

        if !failures.is_empty() {
            return Err(CatalogError::RefreshFailed {
                locale: region.locale.to_string(),
                fetched,
                failures,
            });
        }
        Ok(fetched)
    }

    /// 用一批新抓到的商品覆盖某地区的在线副本。
    ///
    /// 空列表直接忽略：把目录清空会让用户已选的监控项在界面上集体消失，而这
    /// 通常只意味着这一次抓取全军覆没 —— 那种时候旧目录（哪怕是内嵌兜底）
    /// 仍然比空目录有用。
    fn install_products(&self, locale: &str, mut products: Vec<Product>) {
        if products.is_empty() {
            return;
        }
        sort_products(&mut products);
        write_lock(&self.online_products).insert(locale.to_string(), products);
    }

    /// 某地区的在线刷新结果，没有或为空时返回 `None`。
    fn online_products(&self, locale: &str) -> Option<Vec<Product>> {
        read_lock(&self.online_products)
            .get(locale)
            .filter(|products| !products.is_empty())
            .cloned()
    }
}

/// 取读锁，锁中毒时照样把数据交出来。
///
/// 中毒只说明某个线程在持锁期间 panic 了，而这里的临界区只是整体替换一份缓存，
/// 不存在改了一半的中间态。把中毒当成致命错误反而更糟：目录会就此永久不可用，
/// 用户连门店列表都打不开。
fn read_lock<T>(lock: &RwLock<T>) -> RwLockReadGuard<'_, T> {
    lock.read().unwrap_or_else(PoisonError::into_inner)
}

fn write_lock<T>(lock: &RwLock<T>) -> RwLockWriteGuard<'_, T> {
    lock.write().unwrap_or_else(PoisonError::into_inner)
}

/// 让商品排列稳定且符合直觉：先按机型，再按容量从小到大，最后按展示名。
///
/// 原始数据里的顺序是乱的（同一机型下 512GB 可能排在 256GB 前面），直接丢进
/// 下拉框很难找。按机型标识排而不是按数据里的出现顺序，是为了让离线快照与在线
/// 抓取（抓取顺序取决于 `Region::families` 的写法）得到同一份排列。
fn sort_products(products: &mut [Product]) {
    products.sort_by(|a, b| {
        a.family.cmp(&b.family).then_with(|| {
            crate::apple_catalog::capacity_rank(&a.capacity)
                .cmp(&crate::apple_catalog::capacity_rank(&b.capacity))
                .then_with(|| a.title.cmp(&b.title))
        })
    });
}

/// 解析某地区的内嵌商品快照。
///
/// 文件顶层是数组，每个元素都是一份 `productSelectionData`，与在线抓取时从购买页
/// 里截出来的对象结构完全一致，因此直接复用同一套解析，不维护两份。
fn load_products(locale: &str, text: &str) -> Result<Vec<Product>, CatalogError> {
    let groups: Vec<crate::apple_catalog::ProductSelection> =
        serde_json::from_str(text).map_err(|e| CatalogError::Corrupt {
            file: products_file(locale),
            detail: e.to_string(),
        })?;

    let mut products = Vec::new();
    let mut seen = HashSet::new();
    for group in &groups {
        for product in group.to_products() {
            // 组之间也要去重：同一零件号可能同时出现在几段数据里。
            if seen.insert(product.part_number.clone()) {
                products.push(product);
            }
        }
    }

    if products.is_empty() {
        return Err(CatalogError::Corrupt {
            file: products_file(locale),
            detail: "没有解析出任何商品".to_string(),
        });
    }

    sort_products(&mut products);
    Ok(products)
}

/// `stores.json` 顶层数组的元素。
///
/// 各地区的结构不统一：`hasStates` 为 true 时门店挂在 `state[].store[]` 下，
/// 否则直接挂在 `store[]` 下。两种都要认。
///
/// 这里的字段写得比购买页那边硬（不额外容忍 null）：这份数据是内嵌的、随二进制
/// 固定，解析结果由测试钉死；购买页的数据来自线上，随时可能变形，所以那边宽容。
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RawStoreRegion {
    #[serde(default)]
    locale: String,
    #[serde(default)]
    has_states: bool,
    #[serde(default, rename = "state")]
    states: Vec<RawStoreState>,
    #[serde(default, rename = "store")]
    stores: Vec<RawStore>,
}

#[derive(Debug, Deserialize)]
struct RawStoreState {
    #[serde(default)]
    name: String,
    #[serde(default, rename = "store")]
    stores: Vec<RawStore>,
}

#[derive(Debug, Deserialize)]
struct RawStore {
    #[serde(default)]
    id: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    address: RawAddress,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RawAddress {
    #[serde(default)]
    city: String,
    #[serde(default)]
    state_name: String,
}

/// 解析内嵌门店快照，返回按地区分组的门店表。
fn load_stores(text: &str) -> Result<HashMap<String, Vec<Store>>, CatalogError> {
    let regions: Vec<RawStoreRegion> =
        serde_json::from_str(text).map_err(|e| CatalogError::Corrupt {
            file: STORES_FILE.to_string(),
            detail: e.to_string(),
        })?;

    let mut result: HashMap<String, Vec<Store>> = HashMap::with_capacity(regions.len());

    for region in &regions {
        if region.locale.is_empty() {
            continue;
        }

        let mut stores = Vec::new();
        // 按门店编号去重，保留第一次出现的那条，顺序沿用数据源里的顺序 ——
        // 那本来就是按省 / 州分好组的，正合下拉框里找店的直觉。
        let mut seen = HashSet::new();

        for state in &region.states {
            for raw in &state.stores {
                push_store(&mut stores, &mut seen, raw, region.has_states, &state.name);
            }
        }
        for raw in &region.stores {
            push_store(&mut stores, &mut seen, raw, region.has_states, "");
        }

        result.insert(region.locale.clone(), stores);
    }

    if result.is_empty() {
        return Err(CatalogError::Corrupt {
            file: STORES_FILE.to_string(),
            detail: "没有解析出任何地区".to_string(),
        });
    }
    Ok(result)
}

fn push_store(
    out: &mut Vec<Store>,
    seen: &mut HashSet<String>,
    raw: &RawStore,
    has_states: bool,
    state_name: &str,
) {
    let number = raw.id.trim();
    if number.is_empty() {
        return;
    }
    if !seen.insert(number.to_string()) {
        return;
    }

    let name = raw.name.trim();
    // 有 state 层级的地区（中国大陆、日本、澳大利亚等）用 stateName，否则用
    // city。香港、新加坡这类城市站根本没有 stateName 字段。日本站两者都有且
    // 不同（stateName=Tokyo、city=Chiyoda-ku），必须挑前者，否则界面上会冒出
    // 一堆没人认得的区名。
    let mut city = raw.address.city.trim();
    if has_states {
        let from_store = raw.address.state_name.trim();
        if !from_store.is_empty() {
            city = from_store;
        } else if !state_name.trim().is_empty() {
            city = state_name.trim();
        }
    }

    out.push(Store {
        number: number.to_string(),
        name: name.to_string(),
        title: if city.is_empty() {
            name.to_string()
        } else {
            format!("{city}-{name}")
        },
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    fn product(part: &str, title: &str) -> Product {
        Product {
            part_number: part.to_string(),
            family: "iphone17".to_string(),
            capacity: "256GB".to_string(),
            color: "黑色".to_string(),
            title: title.to_string(),
        }
    }

    #[test]
    fn 在线刷新结果会覆盖内嵌副本() {
        let catalog = Catalog::new();
        let offline = catalog.products("zh_CN").expect("内嵌数据应当可用");
        assert!(offline.iter().any(|p| p.part_number == "MG724CH/A"));

        catalog.install_products("zh_CN", vec![product("XXXX/A", "iPhone 18 256GB 黑色")]);

        let fresh = catalog.products("zh_CN").expect("刷新后仍然可读");
        assert_eq!(fresh.len(), 1);
        assert_eq!(fresh[0].part_number, "XXXX/A");
        assert_eq!(
            catalog.product_by_part("zh_CN", "XXXX/A").map(|p| p.title),
            Some("iPhone 18 256GB 黑色".to_string())
        );
        // 旧型号不在新目录里就该查不到 —— 但只是查不到，不是崩溃。
        assert!(catalog.product_by_part("zh_CN", "MG724CH/A").is_none());
        // 只覆盖被刷新的地区，别的地区不受影响。
        assert!(
            !catalog
                .products("ja_JP")
                .expect("日本站应当可用")
                .is_empty()
        );
    }

    #[test]
    fn 空的刷新结果不会把目录清空() {
        let catalog = Catalog::new();
        let before = catalog.products("zh_CN").expect("内嵌数据应当可用");

        catalog.install_products("zh_CN", Vec::new());

        // 一次抓取全军覆没时，旧目录仍然比空目录有用：清空会让用户已选的
        // 监控项在界面上集体消失。
        assert_eq!(catalog.products("zh_CN").expect("仍然可读"), before);
    }

    #[test]
    fn 商品按机型与容量排序() {
        let catalog = Catalog::new();
        let products = catalog.products("zh_CN").expect("内嵌数据应当可用");

        for pair in products.windows(2) {
            let (a, b) = (&pair[0], &pair[1]);
            if a.family != b.family {
                assert!(a.family < b.family, "机型排序不对：{a:?} 在 {b:?} 之前");
                continue;
            }
            let (ra, rb) = (
                crate::apple_catalog::capacity_rank(&a.capacity),
                crate::apple_catalog::capacity_rank(&b.capacity),
            );
            assert!(ra <= rb, "容量排序不对：{} 在 {} 之前", a.title, b.title);
            if ra == rb {
                assert!(a.title <= b.title, "同容量下应当按展示名排");
            }
        }
    }
}
