//! 对真实 `AppleClient` 的行为测试：用本机的一个假 HTTP 服务器代替 Apple。
//!
//! 每条都对应 issue #3 排查时在源码里找到的一个实锤问题：暖场不是单飞、页面 2xx
//! 就算暖好、541 之后秒级重试、请求头彼此不一致。这些在真实网络上测不出来（维护者
//! 的网络从不被拦），只能在这里钉住。

use std::net::SocketAddr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use apw_core::apple::{ApiError, AppleClient, ClientConfig, RequestProfile, Transport, WarmPage};
use apw_core::model::{Category, Family, Region};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

/// 假服务器收到的一次请求。
#[derive(Debug, Clone)]
struct Captured {
    path: String,
    headers: Vec<(String, String)>,
}

impl Captured {
    fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(name))
            .map(|(_, v)| v.as_str())
    }
}

type Responder = Arc<dyn Fn(&str) -> (u16, Vec<(String, String)>, String) + Send + Sync>;

struct FakeApple {
    addr: SocketAddr,
    seen: Arc<Mutex<Vec<Captured>>>,
}

impl FakeApple {
    async fn start(
        responder: impl Fn(&str) -> (u16, Vec<(String, String)>, String) + Send + Sync + 'static,
    ) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("监听本机端口");
        let addr = listener.local_addr().expect("本机地址");
        let seen: Arc<Mutex<Vec<Captured>>> = Arc::new(Mutex::new(Vec::new()));
        let responder: Responder = Arc::new(responder);
        let seen_writer = Arc::clone(&seen);
        tokio::spawn(async move {
            loop {
                let Ok((mut socket, _)) = listener.accept().await else {
                    break;
                };
                let responder = Arc::clone(&responder);
                let seen = Arc::clone(&seen_writer);
                tokio::spawn(async move {
                    let mut buf = Vec::new();
                    let mut chunk = [0u8; 4096];
                    loop {
                        match socket.read(&mut chunk).await {
                            Ok(0) | Err(_) => break,
                            Ok(n) => buf.extend_from_slice(&chunk[..n]),
                        }
                        if buf.windows(4).any(|w| w == b"\r\n\r\n") {
                            break;
                        }
                    }
                    let text = String::from_utf8_lossy(&buf).into_owned();
                    let mut lines = text.split("\r\n");
                    let path = lines
                        .next()
                        .unwrap_or_default()
                        .split_whitespace()
                        .nth(1)
                        .unwrap_or_default()
                        .to_string();
                    let headers = lines
                        .take_while(|l| !l.is_empty())
                        .filter_map(|l| l.split_once(':'))
                        .map(|(k, v)| (k.trim().to_string(), v.trim().to_string()))
                        .collect();
                    let (status, extra, body) = responder(&path);
                    seen.lock().unwrap().push(Captured { path, headers });
                    let mut response = format!(
                        "HTTP/1.1 {status} X\r\nContent-Length: {}\r\nConnection: close\r\n",
                        body.len()
                    );
                    for (k, v) in extra {
                        response.push_str(&format!("{k}: {v}\r\n"));
                    }
                    response.push_str("\r\n");
                    response.push_str(&body);
                    let _ = socket.write_all(response.as_bytes()).await;
                    let _ = socket.shutdown().await;
                });
            }
        });
        Self { addr, seen }
    }

    /// 一个指向假服务器的地区表项。`Region` 的字段都是 `'static`，只能泄漏一份。
    fn region(&self) -> &'static Region {
        let base_url: &'static str = Box::leak(format!("http://{}", self.addr).into_boxed_str());
        Box::leak(Box::new(Region {
            title: "测试",
            locale: "zh_CN",
            base_url,
            families: &[Family {
                category: Category::Iphone,
                slug: "iphone-18-pro",
            }],
        }))
    }

    fn requests(&self, prefix: &str) -> Vec<Captured> {
        self.seen
            .lock()
            .unwrap()
            .iter()
            .filter(|c| c.path.starts_with(prefix))
            .cloned()
            .collect()
    }
}

const WARM_PATH: &str = "/shop/buy-iphone/iphone-18-pro";
const BAG_PATH: &str = "/shop/bag";
const PICKUP_PATH: &str = "/shop/retail/pickup-message";

fn store_in(path: &str) -> String {
    path.split('?')
        .nth(1)
        .unwrap_or_default()
        .split('&')
        .find_map(|kv| kv.strip_prefix("store="))
        .unwrap_or("R000")
        .to_string()
}

fn pickup_json(store: &str) -> String {
    format!(
        r#"{{"head":{{"status":"200"}},"body":{{"stores":[{{"storeNumber":"{store}","storeName":"测试店",
            "partsAvailability":{{"MG724CH/A":{{"partNumber":"MG724CH/A","pickupDisplay":"available",
            "storePickupProductTitle":"iPhone"}}}}}}]}}}}"#
    )
}

/// 正常的 Apple：页面发 cookie，取货接口回 JSON。
fn healthy(path: &str) -> (u16, Vec<(String, String)>, String) {
    if path.starts_with(WARM_PATH) || path.starts_with(BAG_PATH) {
        (
            200,
            vec![
                ("Content-Type".into(), "text/html".into()),
                ("Set-Cookie".into(), "dssid2=secret-value; Path=/".into()),
            ],
            "<html></html>".into(),
        )
    } else if path.starts_with(PICKUP_PATH) {
        (
            200,
            vec![("Content-Type".into(), "application/json".into())],
            pickup_json(&store_in(path)),
        )
    } else {
        (404, Vec::new(), String::new())
    }
}

fn fast(config: ClientConfig) -> AppleClient {
    AppleClient::new(ClientConfig {
        min_interval: Duration::ZERO,
        ..config
    })
    .expect("构造客户端")
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn 并发查询只暖场一次且都带上cookie() {
    let apple = FakeApple::start(healthy).await;
    let region = apple.region();
    let client = fast(ClientConfig::default());

    let mut tasks = Vec::new();
    for store in ["R101", "R102", "R103", "R104"] {
        let client = client.clone();
        tasks.push(tokio::spawn(async move {
            client
                .pickup_message(region, store, &["MG724CH/A".to_string()])
                .await
        }));
    }
    for task in tasks {
        let result = task.await.expect("任务不该 panic");
        assert!(result.is_ok(), "查询应当成功：{result:?}");
    }

    // 四个任务同时发现「没暖过」，也只能有一个去取页面。
    assert_eq!(apple.requests(BAG_PATH).len(), 1, "暖场必须单飞");
    assert!(
        apple.requests(WARM_PATH).is_empty(),
        "默认暖场取购物袋页：会话 cookie 在那里发，购买页只发 geo"
    );
    let pickups = apple.requests(PICKUP_PATH);
    assert_eq!(pickups.len(), 4);
    for p in &pickups {
        assert!(
            p.header("cookie").is_some_and(|c| c.contains("dssid2=")),
            "取货请求必须带上暖场攒到的 cookie：{p:?}"
        );
    }
}

#[tokio::test]
async fn 请求头与真实浏览器一致() {
    let apple = FakeApple::start(healthy).await;
    let region = apple.region();
    let client = fast(ClientConfig::default());
    client
        .pickup_message(region, "R683", &["MG724CH/A".to_string()])
        .await
        .expect("查询应当成功");

    let warm = &apple.requests(BAG_PATH)[0];
    assert_eq!(warm.header("sec-fetch-mode"), Some("navigate"));
    assert_eq!(warm.header("sec-fetch-dest"), Some("document"));
    assert!(
        warm.header("accept")
            .is_some_and(|a| a.starts_with("text/html"))
    );

    let pickup = &apple.requests(PICKUP_PATH)[0];
    let ua = pickup.header("user-agent").expect("必须有 UA");
    assert!(ua.contains("Chrome/149"), "UA 应当是当前档案的版本：{ua}");
    assert!(
        pickup
            .header("sec-ch-ua")
            .is_some_and(|v| v.contains("v=\"149\"")),
        "自称 Chrome 就必须带 client hints，且版本一致"
    );
    // 头的顺序也照抄 Chrome：client hints 打头，UA 在中间，referer 与语言在后。
    let order: Vec<String> = pickup
        .headers
        .iter()
        .map(|(k, _)| k.to_ascii_lowercase())
        .filter(|k| {
            matches!(
                k.as_str(),
                "sec-ch-ua"
                    | "user-agent"
                    | "accept"
                    | "sec-fetch-site"
                    | "referer"
                    | "accept-language"
            )
        })
        .collect();
    assert_eq!(
        order,
        [
            "sec-ch-ua",
            "user-agent",
            "accept",
            "sec-fetch-site",
            "referer",
            "accept-language"
        ],
        "请求头顺序应当与 Chrome 一致：{:?}",
        pickup.headers
    );
    assert_eq!(pickup.header("sec-ch-ua-mobile"), Some("?0"));
    assert_eq!(pickup.header("sec-fetch-site"), Some("same-origin"));
    assert_eq!(pickup.header("sec-fetch-mode"), Some("cors"));
    assert_eq!(pickup.header("sec-fetch-dest"), Some("empty"));
    assert_eq!(pickup.header("accept"), Some("*/*"));
    assert!(
        pickup.header("x-requested-with").is_none(),
        "jQuery 时代的特征头不该再发"
    );
    assert!(
        pickup.header("x-aos-ui-fetch-call-1").is_none(),
        "未经证实的 Apple 自定义头默认不发"
    );
    // Referer 是该地区的默认购买页（真实用户在那里触发查询），不是写死的 /shop/buy-iphone。
    assert_eq!(
        pickup.header("referer"),
        Some(format!("{}{WARM_PATH}", region.base_url).as_str())
    );
}

#[tokio::test]
async fn 旧档案保留旧特征供对照() {
    let apple = FakeApple::start(healthy).await;
    let region = apple.region();
    let client = fast(ClientConfig {
        profile: RequestProfile::legacy(),
        warm_page: WarmPage::Bag,
        ..ClientConfig::default()
    });
    client
        .pickup_message(region, "R683", &["MG724CH/A".to_string()])
        .await
        .expect("查询应当成功");

    assert_eq!(apple.requests(BAG_PATH).len(), 1);
    let pickup = &apple.requests(PICKUP_PATH)[0];
    assert!(
        pickup
            .header("user-agent")
            .is_some_and(|ua| ua.contains("Chrome/130"))
    );
    assert_eq!(pickup.header("x-requested-with"), Some("XMLHttpRequest"));
    assert!(pickup.header("sec-ch-ua").is_none());
}

#[tokio::test]
async fn 被拦后不重试并进入冷却() {
    let apple = FakeApple::start(|path| {
        if path.starts_with(PICKUP_PATH) {
            (541, Vec::new(), "<html>Page Not Found</html>".into())
        } else {
            healthy(path)
        }
    })
    .await;
    let region = apple.region();
    // 默认配置允许重试两次，但被拦截不在此列。
    let client = fast(ClientConfig::default());

    let first = client
        .pickup_message(region, "R683", &["MG724CH/A".to_string()])
        .await;
    match &first {
        Err(ApiError::Blocked(detail)) => {
            assert!(detail.contains("541"), "{detail}");
            assert!(
                detail.contains("冷却"),
                "被拦后必须说明进入了冷却：{detail}"
            );
        }
        other => panic!("541 应当报被拦截，实际是 {other:?}"),
    }
    assert_eq!(
        apple.requests(PICKUP_PATH).len(),
        1,
        "541 之后绝不能秒级重试"
    );

    // 冷却期内的调用直接返回，不发请求 —— 同一地区其他门店也一样。
    for store in ["R683", "R359"] {
        let again = client
            .pickup_message(region, store, &["MG724CH/A".to_string()])
            .await;
        match again {
            Err(ApiError::Blocked(detail)) => {
                assert!(detail.contains("冷却中"), "{detail}")
            }
            other => panic!("冷却期内应当直接返回被拦截，实际是 {other:?}"),
        }
    }
    assert_eq!(
        apple.requests(PICKUP_PATH).len(),
        1,
        "冷却期内不该有任何出站请求"
    );
    assert_eq!(apple.requests(BAG_PATH).len(), 1, "冷却期内也不该重新暖场");

    let records = client.recent_requests().await;
    let blocked = records.iter().filter(|r| r.outcome == "blocked").count();
    assert_eq!(blocked, 1);
    assert!(
        records
            .iter()
            .all(|r| r.status.is_some() || r.kind == "warm")
    );
}

#[tokio::test]
async fn 页面没发cookie不算暖好但查询照发() {
    let apple = FakeApple::start(|path| {
        if path.starts_with(BAG_PATH) {
            (
                200,
                vec![("Content-Type".into(), "text/html".into())],
                "<html></html>".into(),
            )
        } else {
            healthy(path)
        }
    })
    .await;
    let region = apple.region();
    let client = fast(ClientConfig::default());

    for _ in 0..2 {
        client
            .pickup_message(region, "R683", &["MG724CH/A".to_string()])
            .await
            .expect("暖不上也必须照常查询，不能让辅助请求决定库存判定");
    }
    // 第一次暖场没拿到 cookie，一分钟内不再重试暖场，也不让每个查询各自再试一遍。
    assert_eq!(apple.requests(BAG_PATH).len(), 1);
    assert_eq!(apple.requests(PICKUP_PATH).len(), 2);

    let records = client.recent_requests().await;
    let warm = records
        .iter()
        .find(|r| r.kind == "warm")
        .expect("应当记录暖场");
    assert_eq!(warm.outcome, "no_cookie");
    assert!(warm.cookies_after.is_empty());
}

#[tokio::test]
async fn 两种传输层都能完成查询并在记录里写明() {
    let mut transports = vec![Transport::Rustls];
    if cfg!(feature = "chrome-tls") {
        transports.push(Transport::ChromeTls);
    }
    for transport in transports {
        let apple = FakeApple::start(healthy).await;
        let region = apple.region();
        let client = fast(ClientConfig {
            transport,
            ..ClientConfig::default()
        });
        assert_eq!(client.transport(), transport);
        client
            .pickup_message(region, "R683", &["MG724CH/A".to_string()])
            .await
            .unwrap_or_else(|e| panic!("{} 传输层应当能完成查询：{e}", transport.label()));
        let pickup = &apple.requests(PICKUP_PATH)[0];
        assert!(
            pickup
                .header("cookie")
                .is_some_and(|c| c.contains("dssid2=")),
            "{} 传输层也必须带上暖场攒到的 cookie",
            transport.label()
        );
        assert!(
            pickup.header("sec-ch-ua").is_some(),
            "{} 传输层丢了 client hints",
            transport.label()
        );
        let records = client.recent_requests().await;
        assert!(
            records.iter().all(|r| r.transport == transport.label()),
            "记录必须写明传输层：{records:?}"
        );
    }
}

#[tokio::test]
async fn 请求记录只含cookie名不含值() {
    let apple = FakeApple::start(healthy).await;
    let region = apple.region();
    let client = fast(ClientConfig::default());
    client
        .pickup_message(region, "R683", &["MG724CH/A".to_string()])
        .await
        .expect("查询应当成功");

    let records = client.recent_requests().await;
    assert_eq!(records.len(), 2, "一次暖场加一次取货");
    let pickup = records
        .iter()
        .find(|r| r.kind == "pickup")
        .expect("取货记录");
    assert_eq!(pickup.cookies_sent, vec!["dssid2".to_string()]);
    assert_eq!(pickup.outcome, "ok");
    assert_eq!(pickup.status, Some(200));
    assert!(pickup.http_version.is_some());
    let json = serde_json::to_string(&records).expect("记录可序列化");
    assert!(
        !json.contains("secret-value"),
        "记录里出现了 cookie 的值：{json}"
    );
}

#[tokio::test]
async fn 按地点查询用location参数并记在请求记录里() {
    let apple = FakeApple::start(healthy).await;
    let region = apple.region();
    let client = fast(ClientConfig::default());
    let stores = client
        .pickup_message_nearby(region, "上海 上海", &["MG724CH/A".to_string()])
        .await
        .expect("查询应当成功");
    assert_eq!(stores.len(), 1, "假 Apple 只回一家店");

    let pickup = &apple.requests(PICKUP_PATH)[0];
    assert!(
        pickup
            .path
            .contains("location=%E4%B8%8A%E6%B5%B7%20%E4%B8%8A%E6%B5%B7")
            || pickup
                .path
                .contains("location=%E4%B8%8A%E6%B5%B7+%E4%B8%8A%E6%B5%B7"),
        "按地点查询要带 location：{}",
        pickup.path
    );
    assert!(
        !pickup.path.contains("store="),
        "按地点查询不带 store：{}",
        pickup.path
    );
    assert!(pickup.path.contains("parts.0=MG724CH%2FA"));

    let records = client.recent_requests().await;
    let record = records
        .iter()
        .find(|r| r.kind == "pickup")
        .expect("应当有取货请求记录");
    assert_eq!(record.location.as_deref(), Some("上海 上海"));
    assert_eq!(record.store, None);
}

#[tokio::test]
async fn 预算用完后请求排队等恢复() {
    use apw_core::apple::Budget;
    let apple = FakeApple::start(healthy).await;
    let region = apple.region();
    // 容量 2：暖场一次 + 取货一次刚好用完，第二次取货得等一个恢复周期。
    let client = fast(ClientConfig {
        budget: Budget {
            capacity: 2,
            refill_every: Duration::from_millis(300),
        },
        ..ClientConfig::default()
    });
    let part = ["MG724CH/A".to_string()];
    let started = std::time::Instant::now();
    client
        .pickup_message(region, "R101", &part)
        .await
        .expect("第一次查询");
    assert!(
        started.elapsed() < Duration::from_millis(250),
        "容量内不该等"
    );
    assert!(
        client.pacing_delay(1).await >= Duration::from_millis(200),
        "额度用完后引擎应当被告知要等"
    );
    client
        .pickup_message(region, "R102", &part)
        .await
        .expect("第二次查询");
    assert!(
        started.elapsed() >= Duration::from_millis(280),
        "超出容量的请求必须等额度恢复：只过了 {:?}",
        started.elapsed()
    );
    assert_eq!(apple.requests(PICKUP_PATH).len(), 2);
}

// ---------------------------------------------------------------------------
// 多条出口线路（issue #37）
// ---------------------------------------------------------------------------

/// 一个极简的 HTTP 正向代理：收下 `GET http://host:port/path` 这种绝对地址的
/// 请求，连到目标，把请求行改写成相对路径并在查询串里加上 `via=<名字>`，其余
/// 原样转发，响应整段回传。假 Apple 由此分得清请求是直连来的还是经代理来的。
struct FakeProxy {
    addr: SocketAddr,
    hits: Arc<AtomicUsize>,
}

impl FakeProxy {
    async fn start(name: &'static str) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("监听本机端口");
        let addr = listener.local_addr().expect("本机地址");
        let hits = Arc::new(AtomicUsize::new(0));
        let counter = Arc::clone(&hits);
        tokio::spawn(async move {
            loop {
                let Ok((mut downstream, _)) = listener.accept().await else {
                    break;
                };
                let counter = Arc::clone(&counter);
                tokio::spawn(async move {
                    let mut buf = Vec::new();
                    let mut chunk = [0u8; 4096];
                    loop {
                        match downstream.read(&mut chunk).await {
                            Ok(0) | Err(_) => break,
                            Ok(n) => buf.extend_from_slice(&chunk[..n]),
                        }
                        if buf.windows(4).any(|w| w == b"\r\n\r\n") {
                            break;
                        }
                    }
                    let text = String::from_utf8_lossy(&buf).into_owned();
                    let mut lines = text.split("\r\n");
                    let request_line = lines.next().unwrap_or_default();
                    let mut words = request_line.split_whitespace();
                    let method = words.next().unwrap_or("GET");
                    let target = words.next().unwrap_or("");
                    let version = words.next().unwrap_or("HTTP/1.1");
                    let Some(rest) = target.strip_prefix("http://") else {
                        return;
                    };
                    let (hostport, path) = match rest.find('/') {
                        Some(i) => (&rest[..i], &rest[i..]),
                        None => (rest, "/"),
                    };
                    let sep = if path.contains('?') { '&' } else { '?' };
                    let mut out =
                        format!("{method} {path}{sep}via={name} {version}\r\n").into_bytes();
                    for line in lines {
                        if line.is_empty() {
                            break;
                        }
                        if line.to_ascii_lowercase().starts_with("proxy-") {
                            continue;
                        }
                        out.extend_from_slice(line.as_bytes());
                        out.extend_from_slice(b"\r\n");
                    }
                    out.extend_from_slice(b"\r\n");
                    let Ok(mut upstream) = TcpStream::connect(hostport).await else {
                        return;
                    };
                    counter.fetch_add(1, Ordering::SeqCst);
                    if upstream.write_all(&out).await.is_err() {
                        return;
                    }
                    let mut resp = Vec::new();
                    let _ = upstream.read_to_end(&mut resp).await;
                    let _ = downstream.write_all(&resp).await;
                    let _ = downstream.shutdown().await;
                });
            }
        });
        Self { addr, hits }
    }

    fn url(&self) -> String {
        format!("http://{}", self.addr)
    }

    fn hits(&self) -> usize {
        self.hits.load(Ordering::SeqCst)
    }
}

/// 直连来的取货查询一律 541，经代理来的（路径里带 `via=`）正常应答。
fn blocked_unless_via_proxy(path: &str) -> (u16, Vec<(String, String)>, String) {
    if path.starts_with(PICKUP_PATH) && !path.contains("via=") {
        return (
            541,
            vec![("Content-Type".into(), "text/html".into())],
            "<html>blocked</html>".into(),
        );
    }
    healthy(path)
}

fn blocked_always(path: &str) -> (u16, Vec<(String, String)>, String) {
    if path.starts_with(PICKUP_PATH) {
        return (
            541,
            vec![("Content-Type".into(), "text/html".into())],
            "<html>blocked</html>".into(),
        );
    }
    healthy(path)
}

/// 一个已经关掉的本机端口：连过去立刻被拒，模拟连不上的代理。
async fn dead_proxy_url() -> String {
    let listener = TcpListener::bind("127.0.0.1:0").await.expect("监听");
    let addr = listener.local_addr().expect("地址");
    drop(listener);
    format!("http://{addr}")
}

fn pickup_routes(records: &[apw_core::apple::RequestRecord]) -> Vec<(String, String)> {
    records
        .iter()
        .filter(|r| r.kind == "pickup")
        .map(|r| (r.route.clone(), r.outcome.clone()))
        .collect()
}

#[tokio::test]
async fn 被拦时同一次调用就换到下一条线路且之后直接走它() {
    let apple = FakeApple::start(blocked_unless_via_proxy).await;
    let proxy = FakeProxy::start("proxy1").await;
    let region = apple.region();
    let client = fast(ClientConfig {
        proxies: vec![proxy.url()],
        warm_page: WarmPage::None,
        max_retries: 0,
        ..ClientConfig::default()
    });
    assert_eq!(client.route_labels().await, ["direct", "proxy#1"]);
    let part = ["MG724CH/A".to_string()];

    client
        .pickup_message(region, "R101", &part)
        .await
        .expect("直连被拦后应当换代理成功，而不是把 541 报出去");
    assert_eq!(
        pickup_routes(&client.recent_requests().await),
        [
            ("direct".to_string(), "blocked".to_string()),
            ("proxy#1".to_string(), "ok".to_string())
        ],
        "先直连被拦，再经代理成功，都要留下记录"
    );

    client
        .pickup_message(region, "R102", &part)
        .await
        .expect("直连冷却中，第二次应当直接走代理");
    let records = client.recent_requests().await;
    let routes = pickup_routes(&records);
    assert_eq!(routes.len(), 3, "冷却中的直连不该再被碰：{routes:?}");
    assert_eq!(routes[2], ("proxy#1".to_string(), "ok".to_string()));
    assert_eq!(proxy.hits(), 2);
}

#[tokio::test]
async fn 所有线路都被拦时报冷却且不再发请求() {
    let apple = FakeApple::start(blocked_always).await;
    let proxy = FakeProxy::start("proxy1").await;
    let region = apple.region();
    let client = fast(ClientConfig {
        proxies: vec![proxy.url()],
        warm_page: WarmPage::None,
        max_retries: 0,
        ..ClientConfig::default()
    });
    let part = ["MG724CH/A".to_string()];

    let err = client
        .pickup_message(region, "R101", &part)
        .await
        .expect_err("两条线路都被拦");
    assert!(matches!(err, ApiError::Blocked(_)), "{err}");
    assert!(
        err.to_string().contains("线路 proxy#1"),
        "最后被拦的是代理，提示要点名：{err}"
    );
    assert_eq!(pickup_routes(&client.recent_requests().await).len(), 2);

    let err = client
        .pickup_message(region, "R102", &part)
        .await
        .expect_err("全在冷却，直接拒绝");
    let text = err.to_string();
    assert!(text.contains("所有线路都不可用"), "{text}");
    assert!(
        text.contains("direct") && text.contains("proxy#1"),
        "{text}"
    );
    assert_eq!(
        pickup_routes(&client.recent_requests().await).len(),
        2,
        "冷却期间一次请求都不该发"
    );
}

#[tokio::test]
async fn 代理连不上时换下一条线路且报的是代理的错() {
    let apple = FakeApple::start(blocked_unless_via_proxy).await;
    let region = apple.region();
    let client = fast(ClientConfig {
        proxies: vec![dead_proxy_url().await],
        warm_page: WarmPage::None,
        max_retries: 0,
        ..ClientConfig::default()
    });
    let part = ["MG724CH/A".to_string()];

    let err = client
        .pickup_message(region, "R101", &part)
        .await
        .expect_err("直连被拦、代理不通");
    assert!(matches!(err, ApiError::Transport(_)), "{err}");
    assert!(
        err.to_string().contains("proxy#1"),
        "要说清是哪条线路不通：{err}"
    );
    let routes = pickup_routes(&client.recent_requests().await);
    assert_eq!(routes[0], ("direct".to_string(), "blocked".to_string()));
    assert_eq!(routes[1].0, "proxy#1");
    assert_eq!(routes[1].1, "transport");

    // 直连在冷却，第二次只会去撞那个不通的代理，不会再碰直连。
    let _ = client.pickup_message(region, "R102", &part).await;
    let routes = pickup_routes(&client.recent_requests().await);
    assert_eq!(routes.len(), 3, "{routes:?}");
    assert_eq!(routes[2].0, "proxy#1");
}

#[tokio::test]
async fn 无效的代理地址在构造和更换时都报错且原线路不动() {
    let err = AppleClient::new(ClientConfig {
        proxies: vec!["这不是地址".into()],
        ..ClientConfig::default()
    })
    .expect_err("解析不了的代理地址应当在构造时报错");
    assert!(err.to_string().contains("代理地址无效"), "{err}");

    let client = fast(ClientConfig::default());
    let err = client
        .set_proxies(&["::nope".to_string()])
        .await
        .expect_err("换成坏地址应当失败");
    assert!(err.to_string().contains("代理地址无效"), "{err}");
    assert_eq!(
        client.route_labels().await,
        ["direct"],
        "失败时原线路要保持不变"
    );
}

#[tokio::test]
async fn 换代理立即生效并按线路各自记预算() {
    let apple = FakeApple::start(healthy).await;
    let proxy = FakeProxy::start("proxy1").await;
    let region = apple.region();
    let client = fast(ClientConfig {
        warm_page: WarmPage::None,
        max_retries: 0,
        budget: apw_core::apple::Budget {
            capacity: 1,
            refill_every: Duration::from_millis(400),
        },
        ..ClientConfig::default()
    });
    assert_eq!(client.route_labels().await, ["direct"]);
    client.set_proxies(&[proxy.url()]).await.expect("加代理");
    assert_eq!(client.route_labels().await, ["direct", "proxy#1"]);

    let part = ["MG724CH/A".to_string()];
    let started = std::time::Instant::now();
    client
        .pickup_message(region, "R101", &part)
        .await
        .expect("第一次");
    client
        .pickup_message(region, "R102", &part)
        .await
        .expect("第二次");
    assert!(
        started.elapsed() < Duration::from_millis(300),
        "两条线路各有一次额度，前两次不该等：{:?}",
        started.elapsed()
    );
    let routes = pickup_routes(&client.recent_requests().await);
    let mut used: Vec<&str> = routes.iter().map(|(r, _)| r.as_str()).collect();
    used.sort_unstable();
    assert_eq!(
        used,
        ["direct", "proxy#1"],
        "请求应在两条线路间轮流：{routes:?}"
    );

    client
        .pickup_message(region, "R103", &part)
        .await
        .expect("第三次");
    assert!(
        started.elapsed() >= Duration::from_millis(350),
        "两条线路额度都用完了，第三次必须等恢复：{:?}",
        started.elapsed()
    );
}
