//! 对真实 `AppleClient` 的行为测试：用本机的一个假 HTTP 服务器代替 Apple。
//!
//! 每条都对应 issue #3 排查时在源码里找到的一个实锤问题：暖场不是单飞、页面 2xx
//! 就算暖好、541 之后秒级重试、请求头彼此不一致。这些在真实网络上测不出来（维护者
//! 的网络从不被拦），只能在这里钉住。

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use apw_core::apple::{ApiError, AppleClient, ClientConfig, RequestProfile, WarmPage};
use apw_core::model::{Category, Family, Region};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

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
    assert!(ua.contains("Chrome/153"), "UA 应当是当前档案的版本：{ua}");
    assert!(
        pickup
            .header("sec-ch-ua")
            .is_some_and(|v| v.contains("v=\"153\"")),
        "自称 Chrome 就必须带 client hints，且版本一致"
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
