//! 对 Apple 在线商店公开接口的访问。
//!
//! 这里只做「发请求 + 解析响应 + 分类错误」三件事，不含调度或界面逻辑，
//! 因此可以脱离应用单独测试。

use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;
use tokio::sync::Mutex;
use tokio::time::Instant;

use crate::model::{Availability, Region, UnknownReason};

/// 请求失败的分类。
///
/// 调度层据此决定是退避、告警还是直接放弃；界面层据此告诉用户到底出了什么事。
/// 每一类都能转成一个 [`UnknownReason`]，从而保证「失败」这件事在整条链路上
/// 一路都是「未知」，任何一环都无法把它悄悄降级成「无货」。
#[derive(Debug, thiserror::Error)]
pub enum ApiError {
    /// 请求被 Apple 边缘节点拦截，而不是门店真的没货。
    ///
    /// 上游项目正是死在这里：它使用的 `/shop/fulfillment-messages` 现在对任意
    /// 请求恒定返回 HTTP 541 加一个 128002 字节的「Page Not Found」HTML 拦截页
    /// （中国大陆站与美国站响应完全一致，且同一时刻 apple.com.cn 首页正常返回
    /// 200，可排除 IP 封禁）。上游把这个错误当成「无货」处理，于是所有用户看到
    /// 的都是一屏永远不会变的「无货」。
    #[error("请求被 Apple 拦截：{0}")]
    Blocked(String),

    /// 触发了频率限制，应当退避后重试。
    #[error("请求过于频繁被限流：{0}")]
    RateLimited(String),

    /// 响应能解析成 JSON，但结构与预期不符，通常意味着 Apple 又改了接口。
    #[error("接口返回结构与预期不符：字段 {field} 的取值为 {raw:?}")]
    SchemaDrift { field: String, raw: String },

    /// Apple 明确返回了一条业务错误信息。
    #[error("Apple 返回错误：{0}")]
    Apple(String),

    /// 网络层面的失败。
    #[error("网络请求失败：{0}")]
    Transport(String),
}

impl ApiError {
    /// 转成状态机能直接采用的未知原因。
    ///
    /// 这个转换是单向且全覆盖的：**没有任何一条 `ApiError` 能变成
    /// `InStock` 或 `OutOfStock`**。上游那个致命缺陷在这里从类型上就写不出来。
    pub fn into_unknown_reason(self) -> UnknownReason {
        match self {
            Self::Blocked(detail) => UnknownReason::Blocked { detail },
            Self::RateLimited(_) => UnknownReason::RateLimited,
            Self::SchemaDrift { field, raw } => UnknownReason::SchemaDrift { field, raw },
            Self::Apple(message) => UnknownReason::AppleError { message },
            Self::Transport(detail) => UnknownReason::Transport { detail },
        }
    }

    /// 是否值得重试。结构不符和业务错误重试多少次结果都一样。
    pub fn is_retryable(&self) -> bool {
        matches!(
            self,
            Self::Blocked(_) | Self::RateLimited(_) | Self::Transport(_)
        )
    }
}

/// 一个当前主流浏览器的 UA。
///
/// 上游写死的是 Chrome/94（2021 年），这种年代久远的 UA 本身就是明显的机器人
/// 特征。UA 不是被拦的唯一原因，但没有理由主动留下这个特征。
const DEFAULT_USER_AGENT: &str = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) \
     AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";

/// 响应体读取上限，避免异常情况下把整个拦截页甚至更大的内容读进内存。
const MAX_RESPONSE_BYTES: usize = 4 << 20;

/// 客户端配置。
#[derive(Debug, Clone)]
pub struct ClientConfig {
    /// 任意两次出站请求之间的最小间隔，用于全局限速。
    pub min_interval: Duration,
    /// 单次调用内部的最大重试次数（不含首次请求）。
    pub max_retries: u32,
    /// 单次请求的总超时。
    pub timeout: Duration,
    pub user_agent: String,
}

impl Default for ClientConfig {
    fn default() -> Self {
        Self {
            min_interval: Duration::from_millis(500),
            max_retries: 2,
            timeout: Duration::from_secs(15),
            user_agent: DEFAULT_USER_AGENT.to_string(),
        }
    }
}

/// Apple 商店接口客户端，可跨任务共享。
///
/// 必须复用同一个实例：上游为每次查询都新建一个 HTTP 客户端，连接池完全无法
/// 复用，空闲连接持续堆积，配合它 500ms 一轮的轮询，几小时就能涨到十几 GB 内存。
/// `reqwest::Client` 内部就是 `Arc`，克隆代价极低，共享的是同一个连接池。
#[derive(Debug, Clone)]
pub struct AppleClient {
    http: reqwest::Client,
    config: ClientConfig,
    /// 上一次出站请求的时刻，用于全局限速。
    last_sent: Arc<Mutex<Option<Instant>>>,
}

impl AppleClient {
    pub fn new(config: ClientConfig) -> Result<Self, ApiError> {
        let http = reqwest::Client::builder()
            .timeout(config.timeout)
            .connect_timeout(Duration::from_secs(5))
            .pool_idle_timeout(Duration::from_secs(90))
            .pool_max_idle_per_host(8)
            .build()
            .map_err(|e| ApiError::Transport(format!("构造 HTTP 客户端失败：{e}")))?;

        Ok(Self {
            http,
            config,
            last_sent: Arc::new(Mutex::new(None)),
        })
    }

    /// 查询 `store_number` 门店中 `parts` 各型号的可取货状态。
    ///
    /// 一次请求可以携带多个零件号，Apple 会在同一响应里返回全部结果，因此调用方
    /// 应当按门店聚合后再调用，而不是每个型号发一次请求。
    pub async fn pickup_message(
        &self,
        region: &Region,
        store_number: &str,
        parts: &[String],
    ) -> Result<StoreAvailability, ApiError> {
        if store_number.is_empty() {
            return Err(ApiError::Transport("门店编号为空".into()));
        }
        if parts.is_empty() {
            return Err(ApiError::Transport("零件号列表为空".into()));
        }

        let mut query: Vec<(String, String)> = vec![
            ("pl".into(), "true".into()),
            ("mts.0".into(), "regular".into()),
            ("store".into(), store_number.to_string()),
        ];
        for (i, part) in parts.iter().enumerate() {
            query.push((format!("parts.{i}"), part.clone()));
        }

        let body = self
            .get(&region.pickup_message_url(), &query, region)
            .await?;
        parse_pickup_message(&body, store_number)
    }

    /// 执行一次带限速与退避重试的 GET，返回响应体。
    async fn get(
        &self,
        url: &str,
        query: &[(String, String)],
        region: &Region,
    ) -> Result<Vec<u8>, ApiError> {
        with_retry(self.config.max_retries, || async {
            // 限速放在重试循环内部：每一次真正的出站请求都要排队，
            // 重试不该成为绕过全局节流的后门。
            self.throttle().await;
            self.get_once(url, query, region).await
        })
        .await
    }

    /// 保证任意两次出站请求之间至少间隔 `min_interval`。
    async fn throttle(&self) {
        let slot = {
            let mut last = self.last_sent.lock().await;
            let now = Instant::now();
            // `last` 存的是上一次**预约**的发送时刻，可能仍在未来。必须在它之上
            // 累加间隔，而不是拿它和 now 求差 —— 那样几个并发调用会各自算出同一个
            // 「再等 min_interval」，然后在同一时刻一起冲出去。
            let slot = match *last {
                Some(prev) => (prev + self.config.min_interval).max(now),
                None => now,
            };
            *last = Some(slot);
            slot
        };

        tokio::time::sleep_until(slot).await;
    }

    /// 执行单次 HTTP 请求，并把失败归类。
    async fn get_once(
        &self,
        url: &str,
        query: &[(String, String)],
        region: &Region,
    ) -> Result<Vec<u8>, ApiError> {
        let resp = self
            .http
            .get(url)
            .query(query)
            .header(reqwest::header::USER_AGENT, &self.config.user_agent)
            .header(
                reqwest::header::ACCEPT,
                "application/json, text/javascript, */*; q=0.01",
            )
            .header(reqwest::header::ACCEPT_LANGUAGE, region.accept_language())
            .header(
                reqwest::header::REFERER,
                format!("{}/shop/buy-iphone", region.base_url),
            )
            .header("X-Requested-With", "XMLHttpRequest")
            .send()
            .await
            .map_err(|e| ApiError::Transport(e.to_string()))?;

        let status = resp.status();
        let content_type = resp
            .headers()
            .get(reqwest::header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
            .to_ascii_lowercase();

        let body = read_body_capped(resp, MAX_RESPONSE_BYTES).await?;

        if let Some(err) = classify_status(status.as_u16()) {
            return Err(err);
        }
        // 状态码 200 也未必是 JSON：被拦截时可能返回 HTML。
        if looks_like_json(&content_type, &body) {
            Ok(body)
        } else {
            Err(ApiError::Blocked("HTTP 200 但响应不是 JSON".into()))
        }
    }
}

/// 把非 200 的状态码归类成本模块定义的错误；200 返回 `None`，交给调用方按各自的
/// 内容规则判断（库存接口要求是 JSON，购买页只要求非空）。
///
/// 抽出来是因为库存查询与购买页抓取原本各写了一份，五个分支逐字相同。同一件事有
/// 两处定义，迟早会只改其中一处 —— 比如哪天 Apple 换个新的拦截状态码。
pub(crate) fn classify_status(code: u16) -> Option<ApiError> {
    match code {
        200 => None,
        // 541 是 Apple 自定义的拦截状态码，不是标准 HTTP 状态码。
        541 => Some(ApiError::Blocked("HTTP 541".into())),
        403 => Some(ApiError::Blocked("HTTP 403".into())),
        429 => Some(ApiError::RateLimited("HTTP 429".into())),
        c if c >= 500 => Some(ApiError::RateLimited(format!("HTTP {c}"))),
        c => Some(ApiError::Transport(format!("HTTP {c}"))),
    }
}

/// 带指数退避的重试。只有可自愈的错误才重试，结构不符与业务错误重试多少次都一样。
///
/// 同样是原本两处各写一份：库存查询与购买页抓取的退避逻辑此前逐字相同。
pub(crate) async fn with_retry<T, F, Fut>(max_retries: u32, mut once: F) -> Result<T, ApiError>
where
    F: FnMut() -> Fut,
    Fut: std::future::Future<Output = Result<T, ApiError>>,
{
    let mut last_err = None;

    for attempt in 0..=max_retries {
        if attempt > 0 {
            // 被拦截或限流时继续以原频率猛冲只会让情况更糟。
            tokio::time::sleep(Duration::from_secs(1u64 << (attempt - 1))).await;
        }
        match once().await {
            Ok(v) => return Ok(v),
            Err(err) if err.is_retryable() => last_err = Some(err),
            Err(err) => return Err(err),
        }
    }

    Err(last_err.unwrap_or_else(|| ApiError::Transport("重试次数耗尽".into())))
}

/// 分块读取响应体，累计到 `max` 立刻停下。
///
/// 不能用 `resp.bytes()` 再 `.take(max)`：那个方法会先把整份响应缓冲进内存，
/// 上限是在「已经吃完」之后才生效的，对超大响应或 gzip 解压炸弹起不到任何保护。
/// 边读边截才是真的有上限。
pub(crate) async fn read_body_capped(
    mut resp: reqwest::Response,
    max: usize,
) -> Result<Vec<u8>, ApiError> {
    let mut body = Vec::new();
    while body.len() < max {
        let chunk = resp
            .chunk()
            .await
            .map_err(|e| ApiError::Transport(format!("读取响应失败：{e}")))?;
        let Some(chunk) = chunk else { break };
        let n = chunk.len().min(max - body.len());
        body.extend_from_slice(&chunk[..n]);
    }
    Ok(body)
}

fn looks_like_json(content_type: &str, body: &[u8]) -> bool {
    if content_type.contains("json") {
        return true;
    }
    body.iter()
        .find(|b| !b.is_ascii_whitespace())
        .is_some_and(|b| *b == b'{' || *b == b'[')
}

/// 抽象出调度引擎依赖的查询能力，便于在测试里替换掉真实网络请求。
///
/// 用泛型约束而不是 trait object：async fn in trait 在泛型位置可以直接写，
/// 做成 `dyn` 还得引第三方宏来装箱 future，而引擎只需要一个具体实现，不值得。
pub trait Fetcher: Clone + Send + Sync + 'static {
    fn pickup_message(
        &self,
        region: &'static Region,
        store_number: &str,
        parts: &[String],
    ) -> impl std::future::Future<Output = Result<StoreAvailability, ApiError>> + Send;
}

impl Fetcher for AppleClient {
    async fn pickup_message(
        &self,
        region: &'static Region,
        store_number: &str,
        parts: &[String],
    ) -> Result<StoreAvailability, ApiError> {
        AppleClient::pickup_message(self, region, store_number, parts).await
    }
}

/// 单个零件号在单个门店的查询结果。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PartStatus {
    pub part_number: String,
    pub availability: Availability,
    /// Apple 返回的商品名，可用于校验本地目录是否过期。
    pub product_title: Option<String>,
    /// 原始字段值，保留下来便于排查问题和适配未来新增的取值。
    pub pickup_display: String,
}

/// 单个门店的查询结果。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StoreAvailability {
    pub store_number: String,
    pub store_name: String,
    pub parts: std::collections::BTreeMap<String, PartStatus>,
}

// ---- 响应结构。只声明用得到的字段：Apple 的响应有几十个字段，全部映射既无必要，
// ---- 也更容易随接口调整而整体失配。

#[derive(Debug, Deserialize)]
struct PickupResponse {
    #[serde(default)]
    head: PickupHead,
    #[serde(default)]
    body: PickupBody,
}

#[derive(Debug, Default, Deserialize)]
struct PickupHead {
    /// 用 `serde_json::Value` 承接而不是 `String`：字段缺失与「给了但为空串」
    /// 在 `String` 下都是空，而这两者的处置恰好相反。也顺带容下 `"200"` 改成
    /// 数字 `200` 这类形态变化。
    #[serde(default)]
    status: Option<serde_json::Value>,
}

#[derive(Debug, Default, Deserialize)]
struct PickupBody {
    #[serde(default)]
    stores: Vec<PickupStore>,
    #[serde(rename = "errorMessage", default)]
    error_message: Option<String>,
    /// 旧版 fulfillment-messages 的嵌套结构，留作兜底，以防 Apple 把数据挪回去。
    #[serde(default)]
    content: PickupContent,
}

#[derive(Debug, Default, Deserialize)]
struct PickupContent {
    #[serde(rename = "pickupMessage", default)]
    pickup_message: PickupMessageNode,
}

#[derive(Debug, Default, Deserialize)]
struct PickupMessageNode {
    #[serde(default)]
    stores: Vec<PickupStore>,
}

#[derive(Debug, Deserialize)]
struct PickupStore {
    #[serde(rename = "storeNumber", default)]
    store_number: String,
    #[serde(rename = "storeName", default)]
    store_name: String,
    #[serde(rename = "partsAvailability", default)]
    parts_availability: std::collections::BTreeMap<String, PickupPart>,
}

#[derive(Debug, Deserialize)]
struct PickupPart {
    #[serde(rename = "partNumber", default)]
    part_number: Option<String>,
    #[serde(rename = "pickupDisplay", default)]
    pickup_display: Option<String>,
    #[serde(rename = "messageTypes", default)]
    message_types: MessageTypes,
}

#[derive(Debug, Default, Deserialize)]
struct MessageTypes {
    #[serde(default)]
    regular: RegularMessage,
}

#[derive(Debug, Default, Deserialize)]
struct RegularMessage {
    #[serde(rename = "storePickupProductTitle", default)]
    store_pickup_product_title: Option<String>,
}

/// 解析取货状态响应。
pub fn parse_pickup_message(raw: &[u8], want_store: &str) -> Result<StoreAvailability, ApiError> {
    let resp: PickupResponse = serde_json::from_slice(raw).map_err(|e| ApiError::SchemaDrift {
        field: "(整个响应)".into(),
        raw: format!("无法解析成 JSON：{e}"),
    })?;

    check_envelope(&resp)?;

    let stores = if resp.body.stores.is_empty() {
        &resp.body.content.pickup_message.stores
    } else {
        &resp.body.stores
    };

    // 指定了 store 参数时 Apple 只返回该门店，但仍按编号核对，
    // 避免把别的门店的库存错认成目标门店的。
    let matched = stores
        .iter()
        .find(|s| s.store_number == want_store)
        .ok_or_else(|| ApiError::SchemaDrift {
            field: "body.stores[].storeNumber".into(),
            raw: format!("响应中没有门店 {want_store}"),
        })?;

    if matched.parts_availability.is_empty() {
        return Err(ApiError::SchemaDrift {
            field: "body.stores[].partsAvailability".into(),
            raw: format!("门店 {want_store} 没有返回任何型号状态"),
        });
    }

    let mut parts = std::collections::BTreeMap::new();
    for (key, info) in &matched.parts_availability {
        // 条目里的 partNumber 与 map 键不一致时，无法判断哪个可信。随便选一个
        // 继续解析，可能把另一个型号的库存记到目标型号名下 —— 那比报错更糟。
        if let Some(inner) = info.part_number.as_deref().map(str::trim)
            && !inner.is_empty()
            && inner != key
        {
            return Err(ApiError::SchemaDrift {
                field: format!("body.stores[].partsAvailability.{key}.partNumber"),
                raw: format!("条目内零件号 {inner} 与键 {key} 不一致"),
            });
        }

        let raw_display = info.pickup_display.clone().unwrap_or_default();
        parts.insert(
            key.clone(),
            PartStatus {
                part_number: key.clone(),
                availability: availability_from(&raw_display),
                product_title: info
                    .message_types
                    .regular
                    .store_pickup_product_title
                    .clone(),
                pickup_display: raw_display,
            },
        );
    }

    Ok(StoreAvailability {
        store_number: matched.store_number.clone(),
        store_name: matched.store_name.clone(),
        parts,
    })
}

/// 在读取门店数据之前先校验响应信封。
///
/// 必须先做这一步。Go 版只在 `stores` 为空时才看 `errorMessage`，也从不检查
/// `head.status`，于是「head.status=500 + errorMessage 非空 + stores 非空」
/// 这样一个明确的失败响应会被当成正常数据解析，最终得出「无货」。
fn check_envelope(resp: &PickupResponse) -> Result<(), ApiError> {
    if let Some(msg) = resp.body.error_message.as_deref()
        && !msg.trim().is_empty()
    {
        return Err(ApiError::Apple(msg.to_string()));
    }

    // status 缺失（含 JSON null）不判失败：保留的旧接口兜底路径本就没有这一层，
    // 把「没给」当失败会让那条路径直接报废。给了就必须是成功值。
    match &resp.head.status {
        None | Some(serde_json::Value::Null) => Ok(()),
        Some(serde_json::Value::String(s)) if s == "200" => Ok(()),
        Some(serde_json::Value::Number(n)) if n.as_u64() == Some(200) => Ok(()),
        Some(other) => Err(ApiError::SchemaDrift {
            field: "head.status".into(),
            raw: other.to_string(),
        }),
    }
}

/// 把 Apple 的 `pickupDisplay` 字段翻译成三态。
///
/// 已实际观测到的取值：`available`（可取货）、`unavailable`（不可取货）、
/// `ineligible`（该型号在此门店不支持到店取货）。
///
/// 未知取值一律归为 `Unknown` 并带上原始值，而不是 `OutOfStock` —— 猜错成
/// 「无货」会让用户错过机会，猜错成「未知」只是让用户多看一眼。同样重要的是，
/// 「不认识」和「Apple 说不知道」在类型上是可区分的：`SchemaDrift` 会一路传到
/// 界面上，提示用户接口可能已经变了，而不是安静地显示成一个普通的未知。
pub fn availability_from(pickup_display: &str) -> Availability {
    match pickup_display.trim().to_ascii_lowercase().as_str() {
        "available" => Availability::InStock,
        "unavailable" | "ineligible" => Availability::OutOfStock,
        other => Availability::Unknown(UnknownReason::SchemaDrift {
            field: "pickupDisplay".into(),
            raw: other.to_string(),
        }),
    }
}
