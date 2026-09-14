//! 对 Apple 在线商店公开接口的访问。
//!
//! 这里只做「发请求 + 解析响应 + 分类错误」三件事，不含调度或界面逻辑，
//! 因此可以脱离应用单独测试。

use std::collections::{HashMap, VecDeque};
use std::sync::Arc;
use std::time::Duration;

use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use serde::{Deserialize, Serialize};
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

    /// 是否值得立刻重试。
    ///
    /// 结构不符和业务错误重试多少次结果都一样；**被拦截也不重试**：拦截是风控
    /// 评分的结果，几秒后再打一次只会把评分推得更高，issue #3 里六家门店一轮
    /// 就能发出十八次请求，正是这么来的。被拦后的处置是冷却，见
    /// [`AppleClient::pickup_message`]。
    pub fn is_retryable(&self) -> bool {
        matches!(self, Self::RateLimited(_) | Self::Transport(_))
    }
}

/// 与 Apple 说话时的一整套请求特征。
///
/// 这些头必须彼此一致：自称 Chrome 153 却不带任何 Chrome 必发的 `sec-ch-ua`，
/// 或者带着 jQuery 时代的 `X-Requested-With`，都是边缘风控一眼可辨的脚本特征。
/// 所以它们收在一个带名字的档案里整体切换，而不是散落各处各改各的；诊断输出
/// 也能写清「用的是哪一套」。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RequestProfile {
    /// 档案名，带采集日期，出现在诊断输出里。
    pub name: &'static str,
    pub user_agent: String,
    /// `sec-ch-ua` 三件套；`None` 表示不发。
    pub client_hints: Option<ClientHints>,
    /// 是否发送 `sec-fetch-*`（Fetch Metadata）。
    pub fetch_metadata: bool,
    /// 取货接口请求的 `Accept`。
    pub api_accept: String,
    /// 是否发送 `X-Requested-With: XMLHttpRequest`。
    pub x_requested_with: bool,
    /// 是否附带 Apple 商店前端自己加的 `x-skip-redirect` 与 `x-aos-ui-fetch-call-1`。
    /// 后者的生成规则未经证实，只是仿照样例格式，因此默认不发。
    pub apple_extras: bool,
}

/// Chrome 每个请求都会带的低熵 client hints。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ClientHints {
    pub ua: String,
    pub mobile: String,
    pub platform: String,
}

impl RequestProfile {
    /// 默认档案：macOS 上的 Chrome 149，头的形状按 2026-09-13 从真实 Chrome 购买页
    /// 抓到的取货请求来。
    ///
    /// 版本号取 149 而不是当天最新的 153，是为了和 [`Transport::ChromeTls`] 复刻的
    /// TLS / HTTP/2 指纹（wreq-util 的 Chrome 149 档案）保持一致：自称一个版本、
    /// 握手却是另一个版本的样子，正是要避免的那类不一致。更新时整套一起换
    /// （UA 主版本、`sec-ch-ua` 品牌列表、仿真档案），并改档案名。不要每次请求
    /// 随机换 UA，那本身就是特征。
    pub fn chrome() -> Self {
        Self {
            name: "chrome-149-macos-20260913",
            user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 \
                         (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
                .into(),
            client_hints: Some(ClientHints {
                ua: r#""Google Chrome";v="149", "Chromium";v="149", "Not)A;Brand";v="24""#.into(),
                mobile: "?0".into(),
                platform: r#""macOS""#.into(),
            }),
            fetch_metadata: true,
            api_accept: "*/*".into(),
            x_requested_with: false,
            apple_extras: false,
        }
    }

    /// v0.4.1 及以前的请求特征。只用于诊断对照，不要用作默认。
    pub fn legacy() -> Self {
        Self {
            name: "legacy-0.4.1",
            user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 \
                         (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
                .into(),
            client_hints: None,
            fetch_metadata: false,
            api_accept: "application/json, text/javascript, */*; q=0.01".into(),
            x_requested_with: true,
            apple_extras: false,
        }
    }
}

impl Default for RequestProfile {
    fn default() -> Self {
        Self::chrome()
    }
}

/// 暖场取哪个页面。
///
/// 默认取购物袋页：它是动态页面，一次就把 `dssid2`、`as_dc`、`dssf` 等商店会话
/// cookie 全发下来。购买页走 CDN 缓存（`cache-control: public, max-age=120`），
/// 只发一个 `geo`，攒不到会话 —— 2026-09-13 用 `apw doctor` 对照过：购物袋页暖场后
/// 罐里六个 cookie，购买页暖场后只有一个。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum WarmPage {
    /// 购物袋页（默认）。
    Bag,
    /// 该地区的默认购买页。只发 `geo`，留给诊断对照。
    BuyPage,
    /// 不暖场。诊断用：验证「不带 cookie」这一变量。
    None,
}

/// 传输层实现：决定 TLS 握手与 HTTP/2 帧长什么样。
///
/// 请求头可以逐个对齐，握手指纹却是库决定的：rustls + h2 的 ClientHello 和
/// HTTP/2 SETTINGS 与任何浏览器都不一样，而这正是边缘风控打分最常用的信号，
/// 也最能解释「同一台电脑浏览器正常、程序被拦」。`ChromeTls` 用 BoringSSL 复刻
/// Chrome 的握手（wreq / wreq-util 的 Chrome 149 档案），请求头仍由
/// [`RequestProfile`] 控制，两边版本一致。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Transport {
    /// rustls + h2（reqwest）。指纹与浏览器不同，保留给对照和没有 `chrome-tls` 的构建。
    Rustls,
    /// BoringSSL 复刻 Chrome 的 TLS 与 HTTP/2 指纹。需要 `chrome-tls` feature（默认开启）。
    ChromeTls,
}

impl Transport {
    /// 本次构建能用的默认传输：编译进了 `chrome-tls` 就用它。
    pub fn default_for_build() -> Self {
        if cfg!(feature = "chrome-tls") {
            Self::ChromeTls
        } else {
            Self::Rustls
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Rustls => "rustls",
            Self::ChromeTls => "chrome-tls",
        }
    }
}

/// 库存接口响应体的读取上限。一次查询的正常响应只有几 KB，拦截页也不过 128 KB，
/// 4 MB 足够；再大就不是我们要的东西了。
const MAX_RESPONSE_BYTES: usize = 4 << 20;

/// 暖场页的读取上限。读掉响应体是为了让连接能被复用；超过上限直接停下。
const MAX_WARM_BYTES: usize = 8 << 20;

/// 暖场失败后的最小重试间隔。每个门店任务都各自再试一遍暖场，只会把突发放大。
const WARM_RETRY_INTERVAL: Duration = Duration::from_secs(60);

/// 被拦后的冷却时长：首次 5 分钟，冷却结束后的探测再次被拦，依次延长到 10、20、30 分钟。
///
/// 这是保守的客户端策略，不是 Apple 已知的封禁时长。被拦之后继续以原频率发请求，
/// 只会让风控评分越来越高；issue #3 里「一旦 541 就一直 541」正是这个样子。
const BLOCK_COOLDOWNS: [Duration; 4] = [
    Duration::from_secs(5 * 60),
    Duration::from_secs(10 * 60),
    Duration::from_secs(20 * 60),
    Duration::from_secs(30 * 60),
];

/// 保留最近多少条出站请求记录，供诊断命令和日志使用。
const RECENT_RECORDS: usize = 256;

/// 客户端自己给自己定的请求预算。
///
/// # 这是 issue #3 的真正原因
///
/// Apple 的边缘节点按请求**次数**限制取货接口：2026-09-14 在维护者的网络上实测，
/// 大约连发 30 次之后开始返回 541，之后十几分钟内怎么发都是 541，停下来才慢慢
/// 恢复。报告者「6 家门店、60 秒一轮，5 轮后被拦」是同一个数字（5 轮 × 6 家 ≈ 30 次）。
/// 计数挂在会话 / 出口 IP 上：被烧掉的会话会一直 541，而同一浏览器换一个不带
/// cookie 的请求却能通过（实测浏览器带 cookie 541、不带 cookie 200，稳定复现）——
/// 这说明 cookie 本身不是元凶，被烧掉的会话才是。传输层指纹（rustls / chrome-tls）
/// 与请求头在阈值附近测过两个方向，都没能稳定决定一个新会话的成败，所以换指纹不是
/// 解药；把请求次数降下来才是。
///
/// 所以客户端必须自己记账：容量和恢复速度都取得比实测保守，留出同一出口 IP 上
/// 其他程序（浏览器、第二个实例）的份额。超出预算的请求不是被拒绝，而是排队等
/// 额度恢复；引擎会据此把轮询间隔拉长并告诉用户。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Budget {
    /// 桶的容量：不间断连发这么多次之后必须放慢。
    pub capacity: u32,
    /// 每恢复一次请求额度要等多久。
    pub refill_every: Duration,
}

impl Budget {
    /// 不设预算，只给测试和对照实验用。
    pub const fn unlimited() -> Self {
        Self {
            capacity: u32::MAX,
            refill_every: Duration::ZERO,
        }
    }

    fn is_unlimited(&self) -> bool {
        self.refill_every.is_zero()
    }
}

impl Default for Budget {
    fn default() -> Self {
        Self {
            capacity: 20,
            refill_every: Duration::from_secs(60),
        }
    }
}

/// 令牌桶的当前状态。`tokens` 允许为负：负数表示已经预约出去、要等恢复的额度。
#[derive(Debug)]
struct BudgetState {
    tokens: f64,
    updated: Instant,
}

impl BudgetState {
    fn new(budget: &Budget, now: Instant) -> Self {
        Self {
            tokens: f64::from(budget.capacity),
            updated: now,
        }
    }

    fn refill(&mut self, budget: &Budget, now: Instant) {
        if budget.is_unlimited() {
            self.tokens = f64::from(budget.capacity);
            self.updated = now;
            return;
        }
        let elapsed = now.saturating_duration_since(self.updated);
        let gained = elapsed.as_secs_f64() / budget.refill_every.as_secs_f64();
        self.tokens = (self.tokens + gained).min(f64::from(budget.capacity));
        self.updated = now;
    }

    /// 预约一次请求的额度，返回从现在起要等多久才轮到它。
    fn reserve(&mut self, budget: &Budget, now: Instant) -> Duration {
        self.refill(budget, now);
        if budget.is_unlimited() {
            return Duration::ZERO;
        }
        self.tokens -= 1.0;
        if self.tokens >= 0.0 {
            Duration::ZERO
        } else {
            budget.refill_every.mul_f64(-self.tokens)
        }
    }

    /// 要凑够 `requests` 次额度还得等多久；不预约。
    fn wait_for(&mut self, budget: &Budget, now: Instant, requests: usize) -> Duration {
        self.refill(budget, now);
        if budget.is_unlimited() {
            return Duration::ZERO;
        }
        let short = requests as f64 - self.tokens;
        if short <= 0.0 {
            Duration::ZERO
        } else {
            budget.refill_every.mul_f64(short)
        }
    }
}

/// 各地区被拦后的冷却账本。纯状态机，不碰网络，便于单测。
#[derive(Debug, Default)]
struct BlockTracker {
    states: HashMap<String, BlockState>,
}

impl BlockTracker {
    /// 该地区现在能不能发请求。`Ok(true)` 表示这次是冷却结束后放出的那一次探测。
    fn admit(&mut self, locale: &str, now: Instant) -> Result<bool, ApiError> {
        let Some(state) = self.states.get_mut(locale) else {
            return Ok(false);
        };
        if now < state.until {
            return Err(ApiError::Blocked(format!(
                "HTTP 541 后冷却中，{} 后自动重试",
                human_duration(state.until - now)
            )));
        }
        if state.probing {
            return Err(ApiError::Blocked(
                "冷却已结束，正在用一次探测确认是否解封".into(),
            ));
        }
        state.probing = true;
        Ok(true)
    }

    /// 登记一次被拦，返回这次采用的冷却时长。
    ///
    /// 只有**探测**再被拦才升档。同一轮里并发在飞的其他请求跟着被拦，是同一次
    /// 事件：v0.4.2-beta 曾把它们各算一次，六家门店一轮就把冷却从 5 分钟直接
    /// 抬到 30 分钟，用户看到的比不冷却还糟。
    fn record(&mut self, locale: &str, now: Instant, probing: bool) -> Duration {
        let state = self.states.entry(locale.to_string()).or_insert(BlockState {
            until: now,
            level: 0,
            probing: false,
        });
        if probing {
            state.level = (state.level + 1).min(BLOCK_COOLDOWNS.len() - 1);
        }
        state.probing = false;
        let cooldown = BLOCK_COOLDOWNS[state.level];
        state.until = state.until.max(now + cooldown);
        cooldown
    }

    /// 一次成功的查询。只有探测成功才算解封；冷却期内并发漏过去的成功不算数 ——
    /// 同一轮里有的通过有的被拦，说明额度正卡在边上，这时清掉冷却只会让下一轮
    /// 再撞一次。
    fn clear(&mut self, locale: &str, probing: bool) {
        if probing {
            self.states.remove(locale);
        }
    }

    fn end_probe(&mut self, locale: &str) {
        if let Some(state) = self.states.get_mut(locale) {
            state.probing = false;
        }
    }
}

/// 一次取货查询的范围：指定门店，或某个地点周边的所有门店。
#[derive(Debug, Clone)]
enum PickupScope {
    Store(String),
    Nearby(String),
}

impl PickupScope {
    fn query_param(&self) -> (String, String) {
        match self {
            Self::Store(store) => ("store".into(), store.clone()),
            Self::Nearby(location) => ("location".into(), location.clone()),
        }
    }

    fn store(&self) -> Option<String> {
        match self {
            Self::Store(store) => Some(store.clone()),
            Self::Nearby(_) => None,
        }
    }

    fn location(&self) -> Option<String> {
        match self {
            Self::Store(_) => None,
            Self::Nearby(location) => Some(location.clone()),
        }
    }
}

/// 客户端配置。
#[derive(Debug, Clone)]
pub struct ClientConfig {
    /// 任意两次出站请求之间的最小间隔，用于全局限速。
    pub min_interval: Duration,
    /// 单次调用内部的最大重试次数（不含首次请求）。
    ///
    /// 只对网络失败和限流生效；被拦截（HTTP 541 / 403）一律不重试，
    /// 见 [`ApiError::is_retryable`]。
    pub max_retries: u32,
    /// 单次请求的总超时。
    pub timeout: Duration,
    /// 请求特征档案。
    pub profile: RequestProfile,
    /// 暖场策略。
    pub warm_page: WarmPage,
    /// 传输层实现。
    pub transport: Transport,
    /// 出站请求预算，见 [`Budget`]。暖场与取货查询都计入。
    pub budget: Budget,
}

impl Default for ClientConfig {
    fn default() -> Self {
        Self {
            min_interval: Duration::from_millis(500),
            max_retries: 2,
            timeout: Duration::from_secs(15),
            profile: RequestProfile::chrome(),
            warm_page: WarmPage::Bag,
            transport: Transport::default_for_build(),
            budget: Budget::default(),
        }
    }
}

/// 一次真实出站请求的记录，供诊断命令与日志使用。只记 cookie 的名字，不记值。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RequestRecord {
    /// Unix 毫秒。
    pub at_ms: u64,
    /// `warm`（暖场）或 `pickup`（取货查询）。
    pub kind: &'static str,
    /// 用的传输层：`rustls` 或 `chrome-tls`。
    pub transport: &'static str,
    pub locale: String,
    pub store: Option<String>,
    /// 按地点查询时的 `location` 参数；按门店查询时为 `None`。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location: Option<String>,
    /// 这次请求携带的零件号数量（含搭档表带）。
    pub parts: usize,
    pub status: Option<u16>,
    /// 实际协商出的 HTTP 版本，如 `HTTP/2.0`。
    pub http_version: Option<String>,
    pub duration_ms: u64,
    /// `ok` / `blocked` / `rate_limited` / `http_error` / `not_json` / `no_cookie` / `transport`。
    pub outcome: String,
    /// 发送前 cookie 罐里对该地址生效的 cookie 名。
    pub cookies_sent: Vec<String>,
    /// 响应处理后对取货接口生效的 cookie 名。
    pub cookies_after: Vec<String>,
    /// 跟随重定向后的最终地址。
    pub final_url: Option<String>,
}

/// 某地区的暖场状态。
#[derive(Debug, Default)]
struct WarmState {
    /// 暖场页取回来了、而且罐里真的有对取货接口生效的 cookie。
    warmed: bool,
    last_attempt: Option<Instant>,
    failures: u32,
}

/// 某地区被拦后的冷却状态。
#[derive(Debug)]
struct BlockState {
    until: Instant,
    /// 已用到 [`BLOCK_COOLDOWNS`] 的第几档。
    level: usize,
    /// 冷却结束后是否已有一次探测在飞。同一时刻只放一个探测出去。
    probing: bool,
}

/// Apple 商店接口客户端，可跨任务共享。
///
/// 必须复用同一个实例：上游为每次查询都新建一个 HTTP 客户端，连接池完全无法
/// 复用，空闲连接持续堆积，配合它 500ms 一轮的轮询，几小时就能涨到十几 GB 内存。
/// `reqwest::Client` 内部就是 `Arc`，克隆代价极低，共享的是同一个连接池。
#[derive(Debug, Clone)]
pub struct AppleClient {
    /// 传输层与它自己的 cookie 罐。
    ///
    /// 罐由我们持有而不是用库里隐藏的内部罐，是为了能查得到里面到底有没有东西
    /// —— 暖场之后要确认真的攒到了 cookie。一个「以为自己在带 cookie、其实罐是
    /// 空的」的客户端，功能上和现在一模一样，没有任何迹象。
    http: Http,
    config: ClientConfig,
    /// 上一次出站请求的时刻，用于全局限速。
    last_sent: Arc<Mutex<Option<Instant>>>,
    /// 各地区的暖场状态。
    ///
    /// 锁在整个暖场请求期间持有，所以同一时刻只会有一次暖场：第一轮的几个门店
    /// 任务同时发现「还没暖过」时，只有一个去取页面，其余等它的结果。此前每个
    /// 任务各取一次，启动瞬间就是一波突发请求。
    warm: Arc<Mutex<HashMap<String, WarmState>>>,
    /// 各地区被拦后的冷却状态。
    blocks: Arc<Mutex<BlockTracker>>,
    /// 出站请求预算的令牌桶，见 [`Budget`]。
    budget: Arc<Mutex<BudgetState>>,
    /// 最近的出站请求记录。
    recent: Arc<Mutex<VecDeque<RequestRecord>>>,
}

impl AppleClient {
    pub fn new(config: ClientConfig) -> Result<Self, ApiError> {
        let http = Http::build(&config)?;
        let budget = BudgetState::new(&config.budget, Instant::now());
        Ok(Self {
            http,
            config,
            last_sent: Arc::new(Mutex::new(None)),
            warm: Arc::new(Mutex::new(HashMap::new())),
            blocks: Arc::new(Mutex::new(BlockTracker::default())),
            budget: Arc::new(Mutex::new(budget)),
            recent: Arc::new(Mutex::new(VecDeque::with_capacity(RECENT_RECORDS))),
        })
    }

    /// 当前的请求预算。
    pub fn budget(&self) -> Budget {
        self.config.budget
    }

    /// 要再发 `requests` 次请求，按预算得先等多久。不预约额度，只是估算，
    /// 引擎用它决定下一轮什么时候开始。
    pub async fn pacing_delay(&self, requests: usize) -> Duration {
        self.budget
            .lock()
            .await
            .wait_for(&self.config.budget, Instant::now(), requests)
    }

    /// 当前使用的请求特征档案。
    pub fn profile(&self) -> &RequestProfile {
        &self.config.profile
    }

    /// 当前使用的传输层。
    pub fn transport(&self) -> Transport {
        self.config.transport
    }

    /// 最近的出站请求记录，按时间先后排列。
    pub async fn recent_requests(&self) -> Vec<RequestRecord> {
        self.recent.lock().await.iter().cloned().collect()
    }

    async fn push_record(&self, record: RequestRecord) {
        let mut recent = self.recent.lock().await;
        if recent.len() >= RECENT_RECORDS {
            recent.pop_front();
        }
        recent.push_back(record);
    }

    /// 罐里对某个地址生效的 cookie 名。
    pub fn cookie_names_for(&self, url: &str) -> Vec<String> {
        self.http.cookie_names_for(url)
    }

    /// 这个地区当前攒到的 cookie，没有则返回 `None`。契约测试用；**不要打印它的值**。
    pub fn cookies_for(&self, region: &Region) -> Option<String> {
        self.http.cookie_header_for(&region.pickup_message_url())
    }

    /// 确保这个地区的 cookie 已经攒上了。
    ///
    /// # 为什么非做不可
    ///
    /// Apple 的边缘节点会对**没带 cookie** 的取货查询下手。issue #3 的报告者在
    /// 同一个浏览器里做了十轮成对对照，只差带不带 cookie：
    ///
    /// ```text
    /// 带 cookie  10/10 全部 200
    /// 不带 cookie 8/10  返回 541
    /// ```
    ///
    /// 而这件事**只在受审查的网络上才看得出来**：在没被盯上的网络里，带不带
    /// cookie 都是 200，怎么对照都测不出差别。所以别拿「我这里两种都正常」
    /// 当反证 —— 这个假设正是这么被误杀过一次的。
    ///
    /// # 三条纪律
    ///
    /// 1. **同一时刻只暖一次**：锁在整个请求期间持有，并发的门店任务等同一个结果。
    /// 2. **页面 2xx 不算数**：罐里真的有对取货接口生效的 cookie 才算暖好。
    /// 3. **失败不影响查询**：暖不上时照常发查询，只是 [`WARM_RETRY_INTERVAL`] 内
    ///    不再重试暖场。让一次辅助请求的失败去决定库存判定，正是这个项目最不该
    ///    有的东西。
    async fn ensure_warm(&self, region: &Region) {
        let url = match self.config.warm_page {
            WarmPage::Bag => region.bag_url(),
            WarmPage::BuyPage => region.default_buy_page_url(),
            WarmPage::None => return,
        };

        let mut states = self.warm.lock().await;
        let state = states.entry(region.locale.to_string()).or_default();
        if state.warmed {
            return;
        }
        if let Some(at) = state.last_attempt
            && at.elapsed() < WARM_RETRY_INTERVAL
        {
            return;
        }
        state.last_attempt = Some(Instant::now());

        self.throttle().await;
        let started = Instant::now();
        let mut record = RequestRecord {
            at_ms: now_ms(),
            kind: "warm",
            transport: self.http.label(),
            locale: region.locale.to_string(),
            store: None,
            location: None,
            parts: 0,
            status: None,
            http_version: None,
            duration_ms: 0,
            outcome: String::new(),
            cookies_sent: self.cookie_names_for(&url),
            cookies_after: Vec::new(),
            final_url: None,
        };

        let sent = self
            .http
            .fetch(
                &url,
                &[],
                navigation_headers(&self.config.profile, region),
                MAX_WARM_BYTES,
            )
            .await;
        match sent {
            Ok(fetched) => {
                // 响应体已经读掉（连接才能复用），内容本身用不上。
                let status = fetched.status;
                record.status = Some(status);
                record.http_version = Some(fetched.version);
                record.final_url = Some(fetched.final_url);
                let names = self.cookie_names_for(&region.pickup_message_url());
                let ok = (200..300).contains(&status) && !names.is_empty();
                record.cookies_after = names;
                record.outcome = if ok {
                    "ok".into()
                } else if (200..300).contains(&status) {
                    "no_cookie".into()
                } else {
                    "http_error".into()
                };
                if ok {
                    state.warmed = true;
                    state.failures = 0;
                } else {
                    state.failures = state.failures.saturating_add(1);
                }
            }
            Err(err) => {
                record.outcome = format!("transport: {err}");
                state.failures = state.failures.saturating_add(1);
            }
        }
        record.duration_ms = started.elapsed().as_millis() as u64;
        drop(states);
        self.push_record(record).await;
    }

    /// 忘掉某地区的暖场标记，下一次会重新取页面。
    ///
    /// 被拦截时调用。cookie 会过期，也会被边缘节点作废；一直拿着一份不再被认可
    /// 的 cookie 反复重试，只会一直被拦。只清标记不清罐：重新取页面会刷新会话。
    async fn forget_warm(&self, region: &Region) {
        if let Some(state) = self.warm.lock().await.get_mut(region.locale) {
            state.warmed = false;
            state.last_attempt = None;
        }
    }

    /// 检查该地区是否处于被拦后的冷却期。
    ///
    /// 返回 `Err` 表示这次不该发请求；`Ok(true)` 表示本次是冷却结束后放出的那一次探测。
    async fn admit(&self, region: &Region) -> Result<bool, ApiError> {
        self.blocks
            .lock()
            .await
            .admit(region.locale, Instant::now())
    }

    /// 登记一次被拦，返回这次采用的冷却时长。见 [`BlockTracker::record`]。
    async fn record_block(&self, region: &Region, probing: bool) -> Duration {
        self.blocks
            .lock()
            .await
            .record(region.locale, Instant::now(), probing)
    }

    async fn clear_block(&self, region: &Region, probing: bool) {
        self.blocks.lock().await.clear(region.locale, probing);
    }

    async fn end_probe(&self, region: &Region) {
        self.blocks.lock().await.end_probe(region.locale);
    }

    /// 查询 `store_number` 门店中 `parts` 各型号的可取货状态。
    ///
    /// 一次请求可以携带多个零件号，Apple 会在同一响应里返回全部结果，因此调用方
    /// 应当按门店聚合后再调用，而不是每个型号发一次请求。
    ///
    /// 被拦截（HTTP 541）后：不重试，登记冷却，之后对该地区的调用在冷却期内直接
    /// 返回 [`ApiError::Blocked`] 而不发请求；冷却结束后只放一次探测。
    pub async fn pickup_message(
        &self,
        region: &Region,
        store_number: &str,
        parts: &[String],
    ) -> Result<StoreAvailability, ApiError> {
        if store_number.is_empty() {
            return Err(ApiError::Transport("门店编号为空".into()));
        }
        let body = self
            .query(region, PickupScope::Store(store_number.to_string()), parts)
            .await?;
        parse_pickup_message(&body, store_number)
    }

    /// 一次查询 `location` 周边所有门店里 `parts` 各型号的可取货状态。
    ///
    /// `location` 的写法见 [`crate::model::Store::pickup_location`]。响应里有哪些
    /// 门店由 Apple 决定（按距离取若干家），调用方要自己核对想要的门店在不在，
    /// 不在的按门店单独补查。被拦与冷却的处理和 [`Self::pickup_message`] 相同。
    pub async fn pickup_message_nearby(
        &self,
        region: &Region,
        location: &str,
        parts: &[String],
    ) -> Result<Vec<StoreAvailability>, ApiError> {
        if location.trim().is_empty() {
            return Err(ApiError::Transport("查询地点为空".into()));
        }
        let body = self
            .query(region, PickupScope::Nearby(location.to_string()), parts)
            .await?;
        parse_pickup_stores(&body)
    }

    /// 两种范围共用的一次取货查询：冷却检查、暖场、发请求、被拦登记。
    async fn query(
        &self,
        region: &Region,
        scope: PickupScope,
        parts: &[String],
    ) -> Result<Vec<u8>, ApiError> {
        if parts.is_empty() {
            return Err(ApiError::Transport("零件号列表为空".into()));
        }

        let mut query: Vec<(String, String)> = vec![
            ("pl".into(), "true".into()),
            ("mts.0".into(), "regular".into()),
            scope.query_param(),
        ];
        for (i, part) in parts.iter().enumerate() {
            query.push((format!("parts.{i}"), part.clone()));
        }

        let probing = self.admit(region).await?;

        // 先把 cookie 攒上再查，见 ensure_warm；暖不上也照常查。
        self.ensure_warm(region).await;
        // 真实用户是在购买页上触发取货查询的，Referer 就写那一页。
        let referer = region.default_buy_page_url();

        let result = self
            .get(
                &region.pickup_message_url(),
                &query,
                region,
                &referer,
                &scope,
                parts.len(),
            )
            .await;

        match result {
            Ok(body) => {
                self.clear_block(region, probing).await;
                Ok(body)
            }
            Err(ApiError::Blocked(detail)) => {
                self.forget_warm(region).await;
                let cooldown = self.record_block(region, probing).await;
                Err(ApiError::Blocked(format!(
                    "{detail}；已进入冷却，{} 后自动重试一次",
                    human_duration(cooldown)
                )))
            }
            Err(err) => {
                if probing {
                    self.end_probe(region).await;
                }
                Err(err)
            }
        }
    }

    /// 探测与 `region` 之间实际协商出来的 HTTP 版本。
    ///
    /// 这个方法存在的唯一理由是给契约测试当护栏，功能上没人需要它。
    ///
    /// `reqwest` 的 HTTP/2 支持挂在 `http2` feature 上，而这个 crate 用的是
    /// `default-features = false`。那个 feature 曾经漏了整整一个版本：客户端
    /// 静默退回 HTTP/1.1，所有查询照常成功、所有测试照常通过，**功能上完全
    /// 看不出来**。但对 Apple 的边缘节点来说，一个自称最新 Chrome 的客户端
    /// 用 HTTP/1.1 跟它说话，是一眼可辨的脚本特征。
    ///
    /// 这种「配置写漏了、功能却没坏」的缺陷，只能靠一条真的去连一次的测试兜住。
    pub async fn negotiated_http_version(&self, region: &Region) -> Result<String, ApiError> {
        self.throttle().await;
        let fetched = self
            .http
            .fetch(
                &region.bag_url(),
                &[],
                navigation_headers(&self.config.profile, region),
                MAX_WARM_BYTES,
            )
            .await?;
        Ok(fetched.version)
    }

    /// 执行一次带限速与退避重试的 GET，返回响应体。
    async fn get(
        &self,
        url: &str,
        query: &[(String, String)],
        region: &Region,
        referer: &str,
        scope: &PickupScope,
        parts: usize,
    ) -> Result<Vec<u8>, ApiError> {
        with_retry(self.config.max_retries, || async {
            // 限速放在重试循环内部：每一次真正的出站请求都要排队，
            // 重试不该成为绕过全局节流的后门。
            self.throttle().await;
            self.get_once(url, query, region, referer, scope, parts)
                .await
        })
        .await
    }

    /// 每一次出站请求都要经过这里：任意两次之间至少间隔 `min_interval`，并且
    /// 要先从预算里拿到一次额度（见 [`Budget`]），额度不够就等它恢复。
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
        let budget_slot = {
            let now = Instant::now();
            let wait = self.budget.lock().await.reserve(&self.config.budget, now);
            now + wait
        };

        tokio::time::sleep_until(slot.max(budget_slot)).await;
    }

    /// 执行单次 HTTP 请求，把失败归类，并留下一条请求记录。
    async fn get_once(
        &self,
        url: &str,
        query: &[(String, String)],
        region: &Region,
        referer: &str,
        scope: &PickupScope,
        parts: usize,
    ) -> Result<Vec<u8>, ApiError> {
        let started = Instant::now();
        let mut record = RequestRecord {
            at_ms: now_ms(),
            kind: "pickup",
            transport: self.http.label(),
            locale: region.locale.to_string(),
            store: scope.store(),
            location: scope.location(),
            parts,
            status: None,
            http_version: None,
            duration_ms: 0,
            outcome: String::new(),
            cookies_sent: self.cookie_names_for(url),
            cookies_after: Vec::new(),
            final_url: None,
        };

        let sent = self
            .http
            .fetch(
                url,
                query,
                api_headers(&self.config.profile, region, referer),
                MAX_RESPONSE_BYTES,
            )
            .await;
        let fetched = match sent {
            Ok(fetched) => fetched,
            Err(err) => {
                record.duration_ms = started.elapsed().as_millis() as u64;
                record.outcome = "transport".into();
                self.push_record(record).await;
                return Err(err);
            }
        };

        let status = fetched.status;
        record.status = Some(status);
        record.http_version = Some(fetched.version);
        record.final_url = Some(fetched.final_url);
        let content_type = fetched.content_type;
        let body = fetched.body;
        record.duration_ms = started.elapsed().as_millis() as u64;
        record.cookies_after = self.cookie_names_for(url);

        // 先看状态码：拦截页的响应体读到一半失败，也仍然是「被拦」，不能降级成网络错误。
        if let Some(err) = classify_status(status) {
            record.outcome = match &err {
                ApiError::Blocked(_) => "blocked".into(),
                ApiError::RateLimited(_) => "rate_limited".into(),
                _ => "http_error".into(),
            };
            self.push_record(record).await;
            return Err(err);
        }
        let body = match body {
            Ok(body) => body,
            Err(err) => {
                record.outcome = "transport".into();
                self.push_record(record).await;
                return Err(err);
            }
        };
        // 状态码 200 也未必是 JSON：被拦截时可能返回 HTML。
        if looks_like_json(&content_type, &body) {
            record.outcome = "ok".into();
            self.push_record(record).await;
            Ok(body)
        } else {
            record.outcome = "not_json".into();
            self.push_record(record).await;
            Err(ApiError::Blocked("HTTP 200 但响应不是 JSON".into()))
        }
    }
}

/// 页面导航（暖场、抓购买页）用的请求头：和地址栏直接打开一个页面时 Chrome 发的一致。
pub(crate) fn navigation_headers(profile: &RequestProfile, region: &Region) -> HeaderMap {
    // 插入顺序照抄 Chrome 发出的顺序：头的先后也是指纹的一部分。
    // accept-encoding 与 cookie 由传输层自己补在后面。
    let mut headers = HeaderMap::new();
    if let Some(hints) = &profile.client_hints {
        put(&mut headers, "sec-ch-ua", &hints.ua);
        put(&mut headers, "sec-ch-ua-mobile", &hints.mobile);
        put(&mut headers, "sec-ch-ua-platform", &hints.platform);
    }
    put(&mut headers, "upgrade-insecure-requests", "1");
    put(&mut headers, "user-agent", &profile.user_agent);
    put(
        &mut headers,
        "accept",
        "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
    );
    if profile.fetch_metadata {
        put(&mut headers, "sec-fetch-site", "none");
        put(&mut headers, "sec-fetch-mode", "navigate");
        put(&mut headers, "sec-fetch-user", "?1");
        put(&mut headers, "sec-fetch-dest", "document");
    }
    put(&mut headers, "accept-language", region.accept_language());
    headers
}

/// 取货接口请求头：和购买页里同源 `fetch()` 发出的一致。
pub(crate) fn api_headers(profile: &RequestProfile, region: &Region, referer: &str) -> HeaderMap {
    // 插入顺序照抄 Chrome 里 fetch() 发出的顺序：脚本自定义头夹在 accept 与
    // sec-fetch-* 之间，accept-encoding 与 cookie 由传输层补在末尾。
    let mut headers = HeaderMap::new();
    if let Some(hints) = &profile.client_hints {
        put(&mut headers, "sec-ch-ua", &hints.ua);
        put(&mut headers, "sec-ch-ua-mobile", &hints.mobile);
    }
    put(&mut headers, "user-agent", &profile.user_agent);
    if let Some(hints) = &profile.client_hints {
        put(&mut headers, "sec-ch-ua-platform", &hints.platform);
    }
    put(&mut headers, "accept", &profile.api_accept);
    if profile.x_requested_with {
        put(&mut headers, "x-requested-with", "XMLHttpRequest");
    }
    if profile.apple_extras {
        put(&mut headers, "x-skip-redirect", "true");
        put(&mut headers, "x-aos-ui-fetch-call-1", &fetch_call_token());
    }
    if profile.fetch_metadata {
        put(&mut headers, "sec-fetch-site", "same-origin");
        put(&mut headers, "sec-fetch-mode", "cors");
        put(&mut headers, "sec-fetch-dest", "empty");
    }
    put(&mut headers, "referer", referer);
    put(&mut headers, "accept-language", region.accept_language());
    headers
}

/// 一次出站请求拿回来的东西，传输层无关。
struct Fetched {
    status: u16,
    /// 实际协商出的 HTTP 版本，如 `HTTP/2.0`。
    version: String,
    /// 跟随重定向后的最终地址。
    final_url: String,
    content_type: String,
    /// 响应体，读到上限即止；读到一半失败时保留状态码，由调用方决定怎么归类。
    body: Result<Vec<u8>, ApiError>,
}

/// 传输层实现与它自己的 cookie 罐。
#[derive(Clone)]
enum Http {
    Rustls {
        client: reqwest::Client,
        jar: Arc<reqwest::cookie::Jar>,
    },
    #[cfg(feature = "chrome-tls")]
    Chrome {
        client: wreq::Client,
        jar: Arc<wreq::cookie::Jar>,
    },
}

impl std::fmt::Debug for Http {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.label())
    }
}

impl Http {
    fn build(config: &ClientConfig) -> Result<Self, ApiError> {
        match config.transport {
            Transport::Rustls => {
                let jar = Arc::new(reqwest::cookie::Jar::default());
                let client = reqwest::Client::builder()
                    .timeout(config.timeout)
                    .connect_timeout(Duration::from_secs(5))
                    .pool_idle_timeout(Duration::from_secs(90))
                    .pool_max_idle_per_host(8)
                    .cookie_provider(jar.clone())
                    .build()
                    .map_err(|e| ApiError::Transport(format!("构造 HTTP 客户端失败：{e}")))?;
                Ok(Self::Rustls { client, jar })
            }
            Transport::ChromeTls => {
                #[cfg(feature = "chrome-tls")]
                {
                    // 只取仿真档案里的 TLS / HTTP/1 / HTTP/2 参数，请求头仍由
                    // RequestProfile 控制：握手是它的，说话是我们的，两边版本一致。
                    let emulation =
                        wreq::IntoEmulation::into_emulation(wreq_util::Emulation::Chrome149);
                    let jar = Arc::new(wreq::cookie::Jar::default());
                    let client = wreq::Client::builder()
                        .timeout(config.timeout)
                        .connect_timeout(Duration::from_secs(5))
                        .pool_idle_timeout(Duration::from_secs(90))
                        .pool_max_idle_per_host(8)
                        .cookie_provider(jar.clone())
                        .tls_options(emulation.tls_options)
                        .http1_options(emulation.http1_options)
                        .http2_options(emulation.http2_options)
                        .build()
                        .map_err(|e| ApiError::Transport(format!("构造 HTTP 客户端失败：{e}")))?;
                    Ok(Self::Chrome { client, jar })
                }
                #[cfg(not(feature = "chrome-tls"))]
                {
                    Err(ApiError::Transport(
                        "本次构建没有编译 chrome-tls 传输，请改用 Transport::Rustls".into(),
                    ))
                }
            }
        }
    }

    fn label(&self) -> &'static str {
        match self {
            Self::Rustls { .. } => Transport::Rustls.label(),
            #[cfg(feature = "chrome-tls")]
            Self::Chrome { .. } => Transport::ChromeTls.label(),
        }
    }

    /// 发一次 GET，读到上限为止。只有连接层面的失败才返回 `Err`；拿到响应就一定
    /// 返回 `Ok`，状态码由调用方归类。
    async fn fetch(
        &self,
        url: &str,
        query: &[(String, String)],
        headers: HeaderMap,
        max_body: usize,
    ) -> Result<Fetched, ApiError> {
        match self {
            Self::Rustls { client, .. } => {
                let resp = client
                    .get(url)
                    .query(query)
                    .headers(headers)
                    .send()
                    .await
                    .map_err(|e| ApiError::Transport(e.to_string()))?;
                let status = resp.status().as_u16();
                let version = format!("{:?}", resp.version());
                let final_url = resp.url().to_string();
                let content_type = content_type_of(resp.headers());
                let body = read_body_capped(resp, max_body).await;
                Ok(Fetched {
                    status,
                    version,
                    final_url,
                    content_type,
                    body,
                })
            }
            #[cfg(feature = "chrome-tls")]
            Self::Chrome { client, .. } => {
                let resp = client
                    .get(url)
                    .query(query)
                    .headers(headers)
                    .send()
                    .await
                    .map_err(|e| ApiError::Transport(e.to_string()))?;
                let status = resp.status().as_u16();
                let version = format!("{:?}", resp.version());
                let final_url = resp.uri().to_string();
                let content_type = content_type_of(resp.headers());
                let body = read_wreq_body_capped(resp, max_body).await;
                Ok(Fetched {
                    status,
                    version,
                    final_url,
                    content_type,
                    body,
                })
            }
        }
    }

    /// 罐里对某个地址生效的 cookie 名。
    fn cookie_names_for(&self, url: &str) -> Vec<String> {
        match self {
            Self::Rustls { jar, .. } => {
                use reqwest::cookie::CookieStore;
                let Ok(url) = url.parse() else {
                    return Vec::new();
                };
                let Some(value) = jar.cookies(&url) else {
                    return Vec::new();
                };
                let Ok(text) = value.to_str() else {
                    return Vec::new();
                };
                text.split(';')
                    .filter_map(|pair| pair.trim().split('=').next())
                    .filter(|name| !name.is_empty())
                    .map(str::to_string)
                    .collect()
            }
            #[cfg(feature = "chrome-tls")]
            Self::Chrome { jar, .. } => {
                let Ok(uri) = url.parse::<wreq::Uri>() else {
                    return Vec::new();
                };
                jar.matches(uri).map(|c| c.name().to_string()).collect()
            }
        }
    }

    /// 罐里对某个地址生效的整条 `Cookie` 头，没有则 `None`。**不要打印它**。
    fn cookie_header_for(&self, url: &str) -> Option<String> {
        match self {
            Self::Rustls { jar, .. } => {
                use reqwest::cookie::CookieStore;
                let url = url.parse().ok()?;
                jar.cookies(&url)
                    .and_then(|v| v.to_str().ok().map(str::to_owned))
            }
            #[cfg(feature = "chrome-tls")]
            Self::Chrome { jar, .. } => {
                let uri = url.parse::<wreq::Uri>().ok()?;
                let pairs: Vec<String> = jar
                    .matches(uri)
                    .map(|c| format!("{}={}", c.name(), c.value()))
                    .collect();
                (!pairs.is_empty()).then(|| pairs.join("; "))
            }
        }
    }
}

fn content_type_of(headers: &HeaderMap) -> String {
    headers
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default()
        .to_ascii_lowercase()
}

/// 与 [`read_body_capped`] 相同的分块限量读取，wreq 版。
#[cfg(feature = "chrome-tls")]
async fn read_wreq_body_capped(resp: wreq::Response, max: usize) -> Result<Vec<u8>, ApiError> {
    use futures_util::StreamExt;
    let mut stream = resp.bytes_stream();
    let mut body = Vec::new();
    while body.len() < max {
        let Some(chunk) = stream.next().await else {
            break;
        };
        let chunk = chunk.map_err(|e| ApiError::Transport(format!("读取响应失败：{e}")))?;
        let n = chunk.len().min(max - body.len());
        body.extend_from_slice(&chunk[..n]);
    }
    Ok(body)
}

/// 往头表里放一项。名字是我们自己写的常量，值是我们自己拼的 ASCII；万一不合法，
/// 宁可少发这一项也不能让库代码 panic。
fn put(headers: &mut HeaderMap, name: &'static str, value: &str) {
    if let Ok(value) = HeaderValue::from_str(value) {
        headers.insert(HeaderName::from_static(name), value);
    }
}

/// 仿照 Apple 商店前端 `x-aos-ui-fetch-call-1` 样例（`y9kyn7tf7c-mtz5ds9h`）拼一个请求标识：
/// 10 位 `[a-z0-9]` 随机串加短横线加毫秒时间戳的 36 进制。
///
/// **这只是仿样例格式，不是已经确认的 Apple 算法**，所以默认档案不发它，只供诊断对照。
fn fetch_call_token() -> String {
    use rand::Rng;
    const ALPHABET: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";
    let mut rng = rand::rng();
    let head: String = (0..10)
        .map(|_| ALPHABET[rng.random_range(0..ALPHABET.len())] as char)
        .collect();
    format!("{head}-{}", base36(now_ms()))
}

fn base36(mut n: u64) -> String {
    const DIGITS: &[u8] = b"0123456789abcdefghijklmnopqrstuvwxyz";
    if n == 0 {
        return "0".into();
    }
    let mut out = Vec::new();
    while n > 0 {
        out.push(DIGITS[(n % 36) as usize]);
        n /= 36;
    }
    out.reverse();
    String::from_utf8(out).unwrap_or_default()
}

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or_default()
}

/// 把时长写成人看的「5 分钟」「90 秒」。
fn human_duration(d: Duration) -> String {
    let secs = d.as_secs();
    if secs >= 60 && secs.is_multiple_of(60) {
        format!("{} 分钟", secs / 60)
    } else if secs >= 120 {
        format!("{} 分钟", secs.div_ceil(60))
    } else {
        format!("{secs} 秒")
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

    /// 一次拿到 `location` 周边所有门店里 `parts` 的状态，见
    /// [`AppleClient::pickup_message_nearby`]。
    fn pickup_message_nearby(
        &self,
        region: &'static Region,
        location: &str,
        parts: &[String],
    ) -> impl std::future::Future<Output = Result<Vec<StoreAvailability>, ApiError>> + Send;

    /// 再发 `requests` 次请求要先等多久，见 [`AppleClient::pacing_delay`]。
    /// 没有预算概念的实现返回零。
    fn pacing_delay(&self, requests: usize) -> impl std::future::Future<Output = Duration> + Send {
        let _ = requests;
        async { Duration::ZERO }
    }
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

    async fn pickup_message_nearby(
        &self,
        region: &'static Region,
        location: &str,
        parts: &[String],
    ) -> Result<Vec<StoreAvailability>, ApiError> {
        AppleClient::pickup_message_nearby(self, region, location, parts).await
    }

    async fn pacing_delay(&self, requests: usize) -> Duration {
        AppleClient::pacing_delay(self, requests).await
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
    let resp = parse_response(raw)?;
    let stores = store_list(&resp)?;

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

    store_availability(matched)
}

/// 解析按地点查询的响应，返回其中每一家门店的状态，顺序照响应。
///
/// 某家门店的 `partsAvailability` 为空时仍保留这家店（parts 为空），调用方按
/// 型号对账时自然会把它的目标标成未知，而不是在这里把整份响应判废。
pub fn parse_pickup_stores(raw: &[u8]) -> Result<Vec<StoreAvailability>, ApiError> {
    let resp = parse_response(raw)?;
    store_list(&resp)?.iter().map(store_availability).collect()
}

fn parse_response(raw: &[u8]) -> Result<PickupResponse, ApiError> {
    let resp: PickupResponse = serde_json::from_slice(raw).map_err(|e| ApiError::SchemaDrift {
        field: "(整个响应)".into(),
        raw: format!("无法解析成 JSON：{e}"),
    })?;
    check_envelope(&resp)?;
    Ok(resp)
}

fn store_list(resp: &PickupResponse) -> Result<&[PickupStore], ApiError> {
    let stores = if resp.body.stores.is_empty() {
        &resp.body.content.pickup_message.stores
    } else {
        &resp.body.stores
    };

    // Apple 对已经停售、尚未开售或当前不可购买的零件号，可能返回一个成功信封
    // （head.status=200）和空门店列表。它没有说目标门店不存在，更不代表门店无货。
    // 把这种结果报成结构漂移会误导用户等待程序更新；明确指向商品状态，用户才知道
    // 应先核对官网和型号目录。
    if stores.is_empty() {
        return Err(ApiError::Apple(
            "Apple 没有返回任何门店；所选型号可能已停售、尚未开售或当前无法购买".into(),
        ));
    }
    Ok(stores)
}

fn store_availability(matched: &PickupStore) -> Result<StoreAvailability, ApiError> {
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

#[cfg(test)]
mod tests {
    use super::*;

    const LOCALE: &str = "zh_HK";

    #[test]
    fn 同一轮并发被拦只算一次不升档() {
        let mut tracker = BlockTracker::default();
        let now = Instant::now();
        // 六家门店同时在飞，第一家被拦后其余五家的 541 陆续到达。
        let first = tracker.record(LOCALE, now, false);
        assert_eq!(first, BLOCK_COOLDOWNS[0]);
        for i in 1..6 {
            let again = tracker.record(LOCALE, now + Duration::from_millis(i * 100), false);
            assert_eq!(again, BLOCK_COOLDOWNS[0], "第 {i} 个并发失败不该把冷却抬高");
        }
        // 冷却期内一律拒绝。
        assert!(
            tracker
                .admit(LOCALE, now + Duration::from_secs(60))
                .is_err()
        );
    }

    #[test]
    fn 只有探测再被拦才升档且探测成功才解封() {
        let mut tracker = BlockTracker::default();
        let t0 = Instant::now();
        tracker.record(LOCALE, t0, false);
        let after = t0 + BLOCK_COOLDOWNS[0] + Duration::from_secs(1);

        // 冷却结束：放一次探测，其余请求等它的结果。
        assert!(matches!(tracker.admit(LOCALE, after), Ok(true)));
        assert!(tracker.admit(LOCALE, after).is_err());

        // 探测再被拦：升到下一档。
        assert_eq!(tracker.record(LOCALE, after, true), BLOCK_COOLDOWNS[1]);
        let after2 = after + BLOCK_COOLDOWNS[1] + Duration::from_secs(1);
        assert!(matches!(tracker.admit(LOCALE, after2), Ok(true)));

        // 冷却期内并发漏过去的成功不算解封；探测成功才算。
        tracker.clear(LOCALE, false);
        assert!(
            tracker.admit(LOCALE, after2).is_err(),
            "非探测的成功不该清掉冷却"
        );
        tracker.clear(LOCALE, true);
        assert!(
            matches!(tracker.admit(LOCALE, after2), Ok(false)),
            "解封后照常放行"
        );
    }

    #[test]
    fn 探测因网络失败结束后下一次仍是探测() {
        let mut tracker = BlockTracker::default();
        let t0 = Instant::now();
        tracker.record(LOCALE, t0, false);
        let after = t0 + BLOCK_COOLDOWNS[0] + Duration::from_secs(1);
        assert!(matches!(tracker.admit(LOCALE, after), Ok(true)));
        tracker.end_probe(LOCALE);
        assert!(matches!(tracker.admit(LOCALE, after), Ok(true)));
    }

    #[test]
    fn 预算桶按容量放行并按恢复速度排队() {
        let budget = Budget {
            capacity: 3,
            refill_every: Duration::from_secs(60),
        };
        let t0 = Instant::now();
        let mut state = BudgetState::new(&budget, t0);
        assert_eq!(state.reserve(&budget, t0), Duration::ZERO);
        assert_eq!(state.reserve(&budget, t0), Duration::ZERO);
        assert_eq!(state.reserve(&budget, t0), Duration::ZERO);
        // 第四次要等一个恢复周期，第五次等两个。
        assert_eq!(state.reserve(&budget, t0), Duration::from_secs(60));
        assert_eq!(state.reserve(&budget, t0), Duration::from_secs(120));
        // 两分钟后欠账还清，再要三次得等三个周期。
        let t1 = t0 + Duration::from_secs(120);
        assert_eq!(state.wait_for(&budget, t1, 3), Duration::from_secs(180));
        // 半小时后桶满，最多也只有容量那么多。
        let t2 = t0 + Duration::from_secs(1800);
        assert_eq!(state.wait_for(&budget, t2, 3), Duration::ZERO);
        assert_eq!(state.wait_for(&budget, t2, 4), Duration::from_secs(60));
    }

    #[test]
    fn 不设预算时从不等待() {
        let budget = Budget::unlimited();
        let t0 = Instant::now();
        let mut state = BudgetState::new(&budget, t0);
        for _ in 0..1000 {
            assert_eq!(state.reserve(&budget, t0), Duration::ZERO);
        }
        assert_eq!(state.wait_for(&budget, t0, 1000), Duration::ZERO);
    }

    #[test]
    fn 默认预算低于实测会被拦的阈值() {
        // 实测：30 次以内的突发被放行，之后 541；停下后每分钟大约恢复一到两次。
        let budget = Budget::default();
        assert!(
            budget.capacity <= 25,
            "容量要给同一出口 IP 上的浏览器留余量"
        );
        assert!(budget.refill_every >= Duration::from_secs(45));
    }
}
