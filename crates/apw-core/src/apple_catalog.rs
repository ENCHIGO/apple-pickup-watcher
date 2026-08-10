//! 从 Apple 官网购买页在线抓取商品目录。
//!
//! Go 版之前（以及上游）的做法是把 `productSelectionData` 手工从浏览器开发者
//! 工具里复制成 `products_<locale>.json` 提交进仓库，于是每次 Apple 发新机都得
//! 等作者手动更新并发版；上游作者停更之后，那份目录就永远停在了旧机型上。
//! 这里改成直接从购买页现抓，内嵌 JSON 只作为离线兜底。
//!
//! 分工：本模块只负责「把商品数据从购买页里抠出来」，缓存与离线兜底在
//! [`crate::catalog`]，库存查询在 [`crate::apple`]。
//!
//! 没有直接复用 `apple.rs` 的取页函数，是因为那边的限速与重试都是私有的，
//! 而且请求头是按 JSON 接口配的（`Accept: application/json`），拿来抓 HTML
//! 页面并不合适。这里按最小需要另写一份，但**共用调用方传进来的
//! `reqwest::Client`**，连接池仍然是同一个 —— 上游 `services/listen.go:221`
//! 每次请求都 `gorequest.New()` 造一个新客户端，连接池完全无法复用，空闲连接
//! 持续堆积，配合它 500ms 一轮的轮询，几小时就能涨到十几 GB 内存。

use std::collections::{BTreeMap, HashSet};
use std::time::Duration;

use serde::Deserialize;

use crate::apple::ApiError;
use crate::catalog::CatalogError;
use crate::model::{Product, Region};

/// 购买页里承载商品数据的全局变量名。
///
/// 页面中的原文形如：
///
/// ```text
/// window.PRODUCT_SELECTION_BOOTSTRAP = {
///         productSelectionData: {...}
/// ```
///
/// 先定位到这个变量，是为了避开页面里别处的同名文本；万一 Apple 改了变量名，
/// 找不到就退化成全页搜索键名，还能多撑一阵子。
const BOOTSTRAP_MARKER: &[u8] = b"PRODUCT_SELECTION_BOOTSTRAP";

/// 商品数据的键名。
///
/// 注意它是**不带引号的 JS 标识符**，整段不是 JSON，不能直接反序列化，只能
/// 定位到冒号后的第一个 `{` 再做花括号配对，把那个「本身是合法 JSON」的子对象
/// 截出来。不过「不带引号」只是当前打包器的选择，不是契约，带引号的写法也要认。
const SELECTION_KEY_STR: &str = "productSelectionData";
const SELECTION_KEY: &[u8] = SELECTION_KEY_STR.as_bytes();

/// 购买页 HTML 的读取上限。购买页本身在 2 MB 量级，留一倍余量。
///
/// 设上限的目的和 `apple.rs` 一样：万一被换成了别的巨大页面（比如拦截页），
/// 不至于把它整个读进内存。
const MAX_PAGE_BYTES: usize = 8 << 20;

/// 单次取页的超时。
///
/// 挂在请求上而不是客户端上：客户端是调用方给的，这里没有资格改它的全局配置，
/// 但也不能因为对方忘了设超时就把刷新任务永远挂住。
const PAGE_TIMEOUT: Duration = Duration::from_secs(20);

/// 单次抓取内部的最大重试次数（不含首次请求）。
const MAX_RETRIES: u32 = 2;

/// 与 `apple.rs` 保持一致的 UA。
///
/// 那边的常量是私有的，引用不到，只能抄一份。上游写死的是 Chrome/94（2021 年），
/// 这种年代久远的 UA 本身就是明显的机器人特征。
const PAGE_USER_AGENT: &str = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) \
     AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";

/// 抓取 `region` 站点上 `family` 机型的全部可购买配置。
///
/// `family` 是购买页 slug（如 `iphone-17`），取值来自 [`Region::families`]。
///
/// `http` 必须是调用方长期持有的那一个客户端，绝不能在这里现造 —— 见模块文档。
pub async fn fetch_products(
    http: &reqwest::Client,
    region: &Region,
    family: &str,
) -> Result<Vec<Product>, CatalogError> {
    if family.trim().is_empty() {
        return Err(CatalogError::Fetch(ApiError::Transport(
            "机型 slug 为空".into(),
        )));
    }

    let page = fetch_page(http, region, family).await?;
    let products = parse_buy_page(&page)?;
    if products.is_empty() {
        // 页面拿到了、JSON 也截出来了，却一个商品都没有，说明字段名变了。
        // 这必须报错：静默返回空列表会让上层用一份空目录覆盖掉可用的兜底数据。
        return Err(CatalogError::PageSchema {
            detail: format!("{family} 购买页没有解析出任何商品"),
        });
    }
    Ok(products)
}

/// 从一段购买页 HTML 里解析出商品列表。
///
/// 单独暴露出来是为了能用本地 HTML 字符串完整测掉解析链路，不必请求真实网络。
pub fn parse_buy_page(page: &[u8]) -> Result<Vec<Product>, CatalogError> {
    let raw = extract_product_selection_data(page)?;
    parse_product_selection(raw)
}

/// 取一个 HTML 页面，失败按 [`ApiError`] 的口径分类。
///
/// 不复用 `apple.rs` 的 `get`：那里写死了 `Accept: application/json` 和
/// `X-Requested-With: XMLHttpRequest`，那是查询接口的请求特征，拿它去取一个
/// 普通网页反而更像脚本。底层客户端和 UA 仍然共用同一份。
async fn fetch_page(
    http: &reqwest::Client,
    region: &Region,
    family: &str,
) -> Result<Vec<u8>, ApiError> {
    let url = region.buy_page_url(family);
    let mut last_err = None;

    for attempt in 0..=MAX_RETRIES {
        if attempt > 0 {
            // 指数退避。被拦截或限流时继续猛冲只会让情况更糟。
            tokio::time::sleep(Duration::from_secs(1u64 << (attempt - 1))).await;
        }

        match fetch_page_once(http, &url, region).await {
            Ok(body) => return Ok(body),
            Err(err) if err.is_retryable() => last_err = Some(err),
            Err(err) => return Err(err),
        }
    }

    Err(last_err.unwrap_or_else(|| ApiError::Transport("重试次数耗尽".into())))
}

async fn fetch_page_once(
    http: &reqwest::Client,
    url: &str,
    region: &Region,
) -> Result<Vec<u8>, ApiError> {
    let mut resp = http
        .get(url)
        .timeout(PAGE_TIMEOUT)
        .header(reqwest::header::USER_AGENT, PAGE_USER_AGENT)
        .header(
            reqwest::header::ACCEPT,
            "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        )
        .header(reqwest::header::ACCEPT_LANGUAGE, region.accept_language())
        .header(reqwest::header::REFERER, format!("{}/", region.base_url))
        // 刻意不设置 Accept-Encoding：交给 reqwest 的 gzip 特性自动协商并透明
        // 解压，手动指定反而会拿到一坨没人解压的压缩字节。
        .send()
        .await
        .map_err(|e| ApiError::Transport(e.to_string()))?;

    let status = resp.status().as_u16();
    // 边读边截。购买页有两三 MB，若 Apple 哪天返回一个异常巨大的响应（或压缩炸弹），
    // 先整读再切等于没有上限。
    let mut body: Vec<u8> = Vec::new();
    while body.len() < MAX_PAGE_BYTES {
        let chunk = resp
            .chunk()
            .await
            .map_err(|e| ApiError::Transport(format!("读取响应失败：{e}")))?;
        let Some(chunk) = chunk else { break };
        let n = chunk.len().min(MAX_PAGE_BYTES - body.len());
        body.extend_from_slice(&chunk[..n]);
    }

    match status {
        200 => {
            if body.iter().all(u8::is_ascii_whitespace) {
                return Err(ApiError::Blocked("HTTP 200 但响应体为空".into()));
            }
            // 这里不校验内容是不是真的商品页：拦截页同样是 HTML，靠 Content-Type
            // 分不出来。真假交给后面的解析步骤判定。
            Ok(body)
        }
        // 541 是 Apple 自定义的拦截状态码，不是标准 HTTP 状态码。
        541 => Err(ApiError::Blocked("HTTP 541".into())),
        403 => Err(ApiError::Blocked("HTTP 403".into())),
        429 => Err(ApiError::RateLimited("HTTP 429".into())),
        code if code >= 500 => Err(ApiError::RateLimited(format!("HTTP {code}"))),
        code => Err(ApiError::Transport(format!("HTTP {code}"))),
    }
}

/// 从购买页 HTML 中截出 `productSelectionData` 的值。
///
/// 返回的是页面里的原始切片，本身保证是合法 JSON 对象。
pub fn extract_product_selection_data(page: &[u8]) -> Result<&[u8], CatalogError> {
    let (search, offset) = match find(page, BOOTSTRAP_MARKER) {
        Some(i) => (&page[i..], i),
        None => (page, 0),
    };

    // 必须把所有同名位置都试一遍，而不是只认第一个。Go 版为此返工过一次：
    // 页面里只要先出现一段含有 productSelectionData 文本的字符串字面量
    // （埋点参数、提示文案里出现这种字面量再正常不过），定位就停在那里，
    // 发现后面不是冒号便直接判整页失败，真正的属性再也没机会被看到。表现是
    // 购买页明明带着完整数据，程序却一口咬定「结构不符」，用户对着空目录发呆。
    let mut last_err = None;
    let mut from = 0usize;

    while from < search.len() {
        let Some(rel) = find(&search[from..], SELECTION_KEY) else {
            break;
        };
        let at = from + rel;
        from = at + SELECTION_KEY.len();

        match selection_value_at(search, at, offset) {
            Ok(raw) => return Ok(raw),
            // 单个候选不成立只说明这里不是那个属性，接着找下一个。
            Err(err) => last_err = Some(err),
        }
    }

    // 全部候选都不成立时，报最后一个候选的具体原因；一个候选都没有才是「找不到」。
    Err(last_err.unwrap_or_else(|| CatalogError::PageSchema {
        detail: format!("页面里找不到 {SELECTION_KEY_STR}"),
    }))
}

/// 判断 `search[at..]` 处的键名是不是一个真的属性名，是则把它的值原样截出来。
///
/// `offset` 只用于把出错位置换算回整页偏移，方便排查。
fn selection_value_at(search: &[u8], at: usize, offset: usize) -> Result<&[u8], CatalogError> {
    let not_an_identifier = || CatalogError::PageSchema {
        detail: format!(
            "偏移 {} 处的 {SELECTION_KEY_STR} 只是更长标识符的一部分",
            offset + at
        ),
    };

    // 前后都得是标识符边界。否则 legacy_productSelectionData、
    // productSelectionDataV2 这类把目标名包在里面的更长标识符也会算命中，
    // 进而把一份旧数据当成商品目录 —— 那比报错还糟：界面上会摆出一批早已下架
    // 的型号，用户守着永远不会有货的零件号。
    if at > 0 && search.get(at - 1).copied().is_some_and(is_ident_byte) {
        return Err(not_an_identifier());
    }
    let mut pos = at + SELECTION_KEY.len();
    if search.get(pos).copied().is_some_and(is_ident_byte) {
        return Err(not_an_identifier());
    }

    pos = skip_space(search, pos);
    // 键带不带引号都要认：JS 对象字面量里 productSelectionData: 和
    // "productSelectionData": 一样合法。只认不带引号那种的话，Apple 哪天顺手
    // 加上引号（比如换个打包器），整页数据就会栽在「之后不是冒号」上。
    if matches!(search.get(pos), Some(b'"' | b'\'')) {
        pos = skip_space(search, pos + 1);
    }
    if search.get(pos) != Some(&b':') {
        return Err(CatalogError::PageSchema {
            detail: format!("偏移 {} 处的 {SELECTION_KEY_STR} 之后不是冒号", offset + at),
        });
    }
    pos = skip_space(search, pos + 1);
    if search.get(pos) != Some(&b'{') {
        return Err(CatalogError::PageSchema {
            detail: format!("偏移 {} 处的 {SELECTION_KEY_STR} 的值不是对象", offset + at),
        });
    }

    let end = match_object(search, pos).map_err(|detail| CatalogError::PageSchema {
        detail: format!(
            "截取 {SELECTION_KEY_STR} 失败（起始偏移 {}）：{detail}",
            offset + pos
        ),
    })?;
    let raw = search.get(pos..end).unwrap_or_default();

    // 花括号配平不等于内容合法。补这一道校验，才能把「命中的其实是某段文案里的
    // 同名文本」这类假阳性挡在门外 —— 挡掉之后循环还能继续去找真正的属性，
    // 而不是抱着一段垃圾往下走，最后以「一个商品都解析不出来」收场。
    serde_json::from_slice::<serde::de::IgnoredAny>(raw).map_err(|e| CatalogError::PageSchema {
        detail: format!("{SELECTION_KEY_STR} 的值不是合法 JSON：{e}"),
    })?;

    Ok(raw)
}

/// 从 `start` 处的 `{` 开始做花括号配对，返回配对的 `}` 之后一位的下标。
///
/// 必须跳过字符串字面量，否则商品数据里那些含有 `{` 的 HTML 片段（颜色的
/// `image` 字段就是整段 HTML）会把配对算错。按字节扫描对 UTF-8 是安全的：
/// 多字节序列的每个字节最高位都是 1，永远不会等于这里关心的 ASCII 定界符 ——
/// 这也是整个提取过程都在字节上做、而不是在 `&str` 上切片的原因，后者一旦切在
/// 字符中间就是 panic。
fn match_object(b: &[u8], start: usize) -> Result<usize, &'static str> {
    let mut depth = 0usize;
    let mut in_string = false;
    let mut quote = 0u8;
    let mut escaped = false;

    for (i, &ch) in b.iter().enumerate().skip(start) {
        if in_string {
            if escaped {
                escaped = false;
            } else if ch == b'\\' {
                escaped = true;
            } else if ch == quote {
                in_string = false;
            }
            continue;
        }
        match ch {
            b'"' | b'\'' => {
                in_string = true;
                quote = ch;
            }
            b'{' => depth += 1,
            b'}' => {
                // 调用方保证 start 处是 `{`，走到这里 depth 至少为 1；
                // 仍然用 saturating_sub，免得将来有人换了调用方式就地下溢。
                depth = depth.saturating_sub(1);
                if depth == 0 {
                    return Ok(i + 1);
                }
            }
            _ => {}
        }
    }
    Err("花括号未闭合")
}

/// 一个字节能否出现在 JS 标识符中间。
fn is_ident_byte(b: u8) -> bool {
    b == b'_' || b == b'$' || b.is_ascii_alphanumeric()
}

fn skip_space(b: &[u8], mut i: usize) -> usize {
    while matches!(b.get(i), Some(b' ' | b'\t' | b'\r' | b'\n')) {
        i += 1;
    }
    i
}

fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack.windows(needle.len()).position(|w| w == needle)
}

/// 购买页商品数据中用得到的那部分字段。
///
/// 这份数据来自线上，Apple 随时可能改动，所以每个字段都写成「缺了也认」：
/// 少一个字段只该让那一条记录退化，不该让整页目录报废。内嵌快照的元素是同一种
/// 结构，因此 [`crate::catalog`] 直接复用它解析离线数据，没有理由维护两套。
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct ProductSelection {
    /// 用 `Option` 而不是 `#[serde(default)]` 的 `Vec`：字段给成 JSON `null`
    /// 时后者会直接失败，而这两种情况该有同样的处置。
    #[serde(default)]
    products: Option<Vec<RawProduct>>,
    #[serde(default)]
    display_values: Option<DisplayValues>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct DisplayValues {
    /// 用 `Value` 承接是因为这张表里混着非颜色的条目：`title` 是
    /// `{"singleVariantDisplayTitle": ...}`，`variantOrder` 干脆是个数组。
    /// 映射成具体结构体会让整个对象反序列化失败，颜色名就全丢了。
    #[serde(default)]
    dimension_color: BTreeMap<String, serde_json::Value>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RawProduct {
    #[serde(default)]
    part_number: Option<String>,
    #[serde(default)]
    family_type: Option<String>,
    #[serde(default)]
    dimension_capacity: Option<String>,
    #[serde(default)]
    dimension_color: Option<String>,
}

impl ProductSelection {
    /// 把一段商品数据转成商品列表，按零件号去重。
    pub(crate) fn to_products(&self) -> Vec<Product> {
        let colors = self.color_names();
        let raw = self.products.as_deref().unwrap_or_default();

        let mut products = Vec::with_capacity(raw.len());
        let mut seen = HashSet::with_capacity(raw.len());

        for item in raw {
            let part_number = trimmed(&item.part_number);
            if part_number.is_empty() {
                continue;
            }
            // 同一零件号会重复出现：日本站把同一台机器按运营商
            // （SOFTBANK_IPHONE17）和无锁版（UNLOCKED_JP）各列了一遍，零件号
            // 完全相同。查库存只认零件号，留一条就够，否则界面上会出现一模一样
            // 的两行，用户不知道该点哪个。
            if !seen.insert(part_number) {
                continue;
            }

            let family = trimmed(&item.family_type);
            let capacity = normalize_capacity(trimmed(&item.dimension_capacity));
            let color_key = trimmed(&item.dimension_color);
            // 取不到本地化名称就退回色值本身（如 `cosmicorange`）：难看，但比
            // 留空强 —— 留空会让同机型同容量的几个颜色在界面上长得一模一样。
            let color = colors
                .get(color_key)
                .copied()
                .unwrap_or(color_key)
                .to_string();

            products.push(Product {
                title: join_non_empty(&[
                    family_display_name(family),
                    capacity.clone(),
                    color.clone(),
                ]),
                part_number: part_number.to_string(),
                family: family.to_string(),
                capacity,
                color,
            });
        }

        products
    }

    /// 色值到本地化名称的映射。
    fn color_names(&self) -> BTreeMap<&str, &str> {
        let Some(values) = self.display_values.as_ref() else {
            return BTreeMap::new();
        };
        values
            .dimension_color
            .iter()
            .filter_map(|(key, value)| {
                // 取不出 value 的条目就是上面说的 title / variantOrder，跳过即可。
                // `Value::get` 对数组用字符串键取值会得到 None，不会 panic。
                let name = value.get("value")?.as_str()?.trim();
                (!name.is_empty()).then_some((key.as_str(), name))
            })
            .collect()
    }
}

/// 把一个 `productSelectionData` 对象解析成商品列表。
pub fn parse_product_selection(raw: &[u8]) -> Result<Vec<Product>, CatalogError> {
    let data: ProductSelection =
        serde_json::from_slice(raw).map_err(|e| CatalogError::PageSchema {
            detail: format!("{SELECTION_KEY_STR} 结构与预期不符：{e}"),
        })?;
    Ok(data.to_products())
}

fn trimmed(value: &Option<String>) -> &str {
    value.as_deref().unwrap_or_default().trim()
}

fn join_non_empty(parts: &[String]) -> String {
    parts
        .iter()
        .filter(|p| !p.is_empty())
        .cloned()
        .collect::<Vec<_>>()
        .join(" ")
}

/// 把 Apple 的容量标识规范成展示形式，如 `512gb` → `512GB`。
pub fn normalize_capacity(raw: &str) -> String {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    let lower = trimmed.to_ascii_lowercase();
    for unit in ["tb", "gb", "mb"] {
        if let Some(number) = lower.strip_suffix(unit) {
            return format!("{}{}", number.trim(), unit.to_ascii_uppercase());
        }
    }
    // 认不出单位就原样返回。拼一个自以为是的名字比留着原文更容易误导人。
    trimmed.to_string()
}

/// 把容量标识换算成可比较的数值（单位 GB），用于排序。
///
/// 直接按字符串排会把 `1TB` 排在 `256GB` 前面。
pub(crate) fn capacity_rank(capacity: &str) -> u32 {
    let lower = capacity.trim().to_ascii_lowercase();
    let (number, multiplier) = if let Some(n) = lower.strip_suffix("tb") {
        (n, 1024)
    } else if let Some(n) = lower.strip_suffix("gb") {
        (n, 1)
    } else {
        return 0;
    };
    number
        .trim()
        .parse::<u32>()
        .map(|n| n.saturating_mul(multiplier))
        .unwrap_or(0)
}

/// 机型标识里可识别的词，按顺序尝试匹配。
///
/// 拆成单词而不是穷举全部机型，是为了新机发布时不必改代码：`iphone17promax`
/// 会被拆成 pro + max，未来的 `iphone18pro` 同样成立。
const FAMILY_WORDS: &[(&str, &str)] = &[
    ("pro", "Pro"),
    ("max", "Max"),
    ("plus", "Plus"),
    ("mini", "mini"),
    ("air", "Air"),
    ("se", "SE"),
    ("e", "e"),
];

/// 把 `familyType` 转成人类可读的机型名，如 `iphone17promax` → `iPhone 17 Pro Max`。
pub fn family_display_name(family_type: &str) -> String {
    let trimmed = family_type.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    let lower = trimmed.to_lowercase();
    let Some(rest) = lower.strip_prefix("iphone") else {
        // 不认识的前缀原样返回，总比拼出个错名字强。
        return trimmed.to_string();
    };

    // 逐字符处理而不是按字节切片：familyType 理论上可能带非 ASCII，按字节切
    // `&str` 一旦切在字符中间就是 panic，而库代码不允许有 panic。
    let chars: Vec<char> = rest.chars().collect();
    let mut tokens: Vec<String> = vec!["iPhone".to_string()];
    let mut i = 0usize;

    while i < chars.len() {
        if chars[i].is_ascii_digit() {
            let start = i;
            while i < chars.len() && chars[i].is_ascii_digit() {
                i += 1;
            }
            tokens.push(chars[start..i].iter().collect());
            continue;
        }

        if let Some((token, display)) = FAMILY_WORDS
            .iter()
            .find(|(token, _)| starts_with_at(&chars, i, token))
        {
            // 「16e」这类后缀紧贴数字，中间不加空格。
            let glue = *token == "e"
                && tokens
                    .last()
                    .and_then(|t| t.chars().next())
                    .is_some_and(|c| c.is_ascii_digit());
            if glue {
                if let Some(last) = tokens.last_mut() {
                    last.push_str(display);
                }
            } else {
                tokens.push((*display).to_string());
            }
            i += token.chars().count();
            continue;
        }

        // 遇到没登记的词，整段保留并首字母大写，至少不丢信息。
        let start = i;
        while i < chars.len() && !chars[i].is_ascii_digit() {
            i += 1;
        }
        let word: String = chars[start..i].iter().collect();
        tokens.push(capitalize(&word));
    }

    tokens.join(" ")
}

fn starts_with_at(chars: &[char], i: usize, word: &str) -> bool {
    let len = word.chars().count();
    chars
        .get(i..i + len)
        .is_some_and(|slice| slice.iter().copied().eq(word.chars()))
}

fn capitalize(word: &str) -> String {
    let mut it = word.chars();
    match it.next() {
        None => String::new(),
        Some(first) => first.to_uppercase().collect::<String>() + it.as_str(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 容量被规范成展示形式() {
        assert_eq!(normalize_capacity("512gb"), "512GB");
        assert_eq!(normalize_capacity(" 1tb "), "1TB");
        assert_eq!(normalize_capacity("256GB"), "256GB");
        assert_eq!(normalize_capacity(""), "");
        // 认不出的单位原样保留，不瞎猜。
        assert_eq!(normalize_capacity("大杯"), "大杯");
    }

    #[test]
    fn 容量按真实大小排序而不是按字符串() {
        assert!(capacity_rank("256GB") < capacity_rank("512GB"));
        // 按字符串排会把 1TB 排在 256GB 前面。
        assert!(capacity_rank("512GB") < capacity_rank("1TB"));
        assert!(capacity_rank("1TB") < capacity_rank("2TB"));
        assert_eq!(capacity_rank("看不懂"), 0);
    }

    #[test]
    fn 机型标识被拆成可读的名字() {
        for (raw, want) in [
            ("iphone17", "iPhone 17"),
            ("iphone17pro", "iPhone 17 Pro"),
            ("iphone17promax", "iPhone 17 Pro Max"),
            ("iphoneair", "iPhone Air"),
            ("iphone16e", "iPhone 16e"),
            ("iphone16plus", "iPhone 16 Plus"),
            ("iphone13mini", "iPhone 13 mini"),
            ("iphonese3", "iPhone SE 3"),
            // 不认识的前缀原样返回，不硬拼。
            ("ipad11", "ipad11"),
            ("", ""),
        ] {
            assert_eq!(family_display_name(raw), want, "机型 {raw} 拼错了");
        }
    }

    #[test]
    fn 非ascii机型标识不会切在字符中间() {
        // 只要求不 panic：库代码里任何输入都不允许把进程带走。
        for raw in ["iphone17专业版", "苹果17", "iphone🙂"] {
            let _ = family_display_name(raw);
        }
    }
}
