use std::collections::BTreeMap;
use std::future::pending;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use apw_cli::{Mode, bounded, monitor};
use apw_core::apple::{ApiError, Fetcher, PartStatus, StoreAvailability};
use apw_core::model::{Availability, Region, Target};
use apw_core::watcher::WatcherConfig;
use serde_json::Value;

type Reply = dyn Fn(usize, &str, &[String]) -> Result<StoreAvailability, ApiError> + Send + Sync;
#[derive(Clone)]
struct Fake {
    calls: Arc<AtomicUsize>,
    reply: Arc<Reply>,
}
impl Fake {
    fn new(
        reply: impl Fn(usize, &str, &[String]) -> Result<StoreAvailability, ApiError>
        + Send
        + Sync
        + 'static,
    ) -> Self {
        Self {
            calls: Arc::new(AtomicUsize::new(0)),
            reply: Arc::new(reply),
        }
    }
}
impl Fetcher for Fake {
    async fn pickup_message(
        &self,
        _: &'static Region,
        store: &str,
        parts: &[String],
    ) -> Result<StoreAvailability, ApiError> {
        (self.reply)(self.calls.fetch_add(1, Ordering::SeqCst), store, parts)
    }
}
fn response(store: &str, parts: &[String], availability: Availability) -> StoreAvailability {
    StoreAvailability {
        store_number: store.into(),
        store_name: store.into(),
        parts: parts
            .iter()
            .map(|p| {
                (
                    p.clone(),
                    PartStatus {
                        part_number: p.clone(),
                        availability: availability.clone(),
                        product_title: None,
                        pickup_display: String::new(),
                    },
                )
            })
            .collect::<BTreeMap<_, _>>(),
    }
}
fn target(part: &str) -> Target {
    Target {
        locale: "zh_CN".into(),
        store_number: "R359".into(),
        store_title: "上海-南京东路".into(),
        part_number: part.into(),
        product_name: part.into(),
        companion_part: None,
    }
}
fn config() -> WatcherConfig {
    WatcherConfig {
        interval: Duration::from_secs(1),
        jitter: 0.0,
        ..WatcherConfig::default()
    }
}
fn lines(out: &[u8]) -> Vec<Value> {
    String::from_utf8_lossy(out)
        .lines()
        .map(|line| serde_json::from_str(line).expect("JSON line"))
        .collect()
}

#[tokio::test]
async fn out_of_stock_is_success_and_check_performs_one_cycle() {
    let fake = Fake::new(|_, store, parts| Ok(response(store, parts, Availability::OutOfStock)));
    let mut out = Vec::new();
    let code = monitor(
        fake.clone(),
        vec![target("MWUC3CH/A")],
        config(),
        Mode::Check,
        &mut out,
    )
    .await
    .unwrap();
    assert_eq!(code, 0);
    assert_eq!(fake.calls.load(Ordering::SeqCst), 1);
    let rows = lines(&out);
    assert_eq!(rows.len(), 1);
    assert_eq!(rows[0]["healthy"], true);
    assert_eq!(rows[0]["anyInStock"], false);
    assert_eq!(
        rows[0]["snapshot"][0]["availability"]["kind"],
        "out_of_stock"
    );
    assert!(rows[0]["snapshot"][0]["lastCheckedMs"].is_u64());
}

#[tokio::test]
async fn blocked_check_is_unknown_with_reason_and_nonzero_exit() {
    let fake = Fake::new(|_, _, _| Err(ApiError::Blocked("HTTP 541".into())));
    let mut out = Vec::new();
    let code = monitor(
        fake,
        vec![target("MWUC3CH/A")],
        config(),
        Mode::Check,
        &mut out,
    )
    .await
    .unwrap();
    assert_eq!(code, 3);
    let result = &lines(&out)[0];
    assert_eq!(result["healthy"], false);
    assert_eq!(
        result["snapshot"][0]["availability"],
        serde_json::json!({"kind": "unknown", "reason": "blocked", "detail": "HTTP 541"})
    );
}

#[tokio::test]
async fn missing_sku_does_not_erase_other_confirmed_stock() {
    let fake = Fake::new(|_, store, parts| Ok(response(store, &parts[..1], Availability::InStock)));
    let mut out = Vec::new();
    let code = monitor(
        fake,
        vec![target("AAAA1CH/A"), target("BBBB1CH/A")],
        config(),
        Mode::Check,
        &mut out,
    )
    .await
    .unwrap();
    assert_eq!(code, 3);
    let result = &lines(&out)[0];
    assert_eq!(result["anyInStock"], true);
    assert_eq!(result["snapshot"][0]["availability"]["kind"], "in_stock");
    assert_eq!(
        result["snapshot"][1]["availability"]["reason"],
        "schema_drift"
    );
}

#[tokio::test(start_paused = true)]
async fn continuous_stock_is_not_reannounced_but_stock_return_is() {
    let fake = Fake::new(|n, store, parts| {
        Ok(response(
            store,
            parts,
            if n == 1 {
                Availability::OutOfStock
            } else {
                Availability::InStock
            },
        ))
    });
    let mut out = Vec::new();
    let error = bounded(
        monitor(
            fake,
            vec![target("MWUC3CH/A")],
            config(),
            Mode::Watch {
                until_in_stock: false,
            },
            &mut out,
        ),
        4,
        pending(),
    )
    .await
    .unwrap_err();
    assert_eq!(error.exit_code, 4);
    let rows = lines(&out);
    assert_eq!(
        rows.iter()
            .filter(|r| r["event"]["type"] == "inStock")
            .count(),
        2
    );
    assert!(
        rows.iter()
            .filter(|r| r["event"]["type"] == "cycleComplete")
            .count()
            >= 3
    );
    assert!(
        rows.iter()
            .all(|r| r["schemaVersion"] == 1 && r["command"] == "watch")
    );
}

#[tokio::test(start_paused = true)]
async fn until_in_stock_waits_past_negative_result_then_exits() {
    let fake = Fake::new(|n, store, parts| {
        Ok(response(
            store,
            parts,
            if n == 0 {
                Availability::OutOfStock
            } else {
                Availability::InStock
            },
        ))
    });
    let mut out = Vec::new();
    let code = bounded(
        monitor(
            fake.clone(),
            vec![target("MWUC3CH/A")],
            config(),
            Mode::Watch {
                until_in_stock: true,
            },
            &mut out,
        ),
        10,
        pending(),
    )
    .await
    .unwrap();
    assert_eq!(code, 0);
    assert_eq!(fake.calls.load(Ordering::SeqCst), 2);
    assert_eq!(lines(&out).last().unwrap()["event"]["type"], "inStock");
}

#[derive(Clone)]
struct Hanging(Arc<AtomicUsize>);
struct InFlight(Arc<AtomicUsize>);
impl Drop for InFlight {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}
impl Fetcher for Hanging {
    async fn pickup_message(
        &self,
        _: &'static Region,
        _: &str,
        _: &[String],
    ) -> Result<StoreAvailability, ApiError> {
        self.0.fetch_add(1, Ordering::SeqCst);
        let _guard = InFlight(self.0.clone());
        pending().await
    }
}

#[tokio::test(start_paused = true)]
async fn deadline_cancels_inflight_query_and_emits_no_fabricated_result() {
    let active = Arc::new(AtomicUsize::new(0));
    let mut out = Vec::new();
    let error = bounded(
        monitor(
            Hanging(active.clone()),
            vec![target("MWUC3CH/A")],
            config(),
            Mode::Check,
            &mut out,
        ),
        1,
        pending(),
    )
    .await
    .unwrap_err();
    assert_eq!(error.exit_code, 4);
    assert!(out.is_empty());
    tokio::time::sleep(Duration::from_millis(1)).await;
    assert_eq!(active.load(Ordering::SeqCst), 0);
}

#[tokio::test(start_paused = true)]
async fn shutdown_cancels_unlimited_watch() {
    let active = Arc::new(AtomicUsize::new(0));
    let mut out = Vec::new();
    let shutdown = async {
        tokio::time::sleep(Duration::from_secs(1)).await;
        Ok(143)
    };
    let error = bounded(
        monitor(
            Hanging(active.clone()),
            vec![target("MWUC3CH/A")],
            config(),
            Mode::Watch {
                until_in_stock: false,
            },
            &mut out,
        ),
        0,
        shutdown,
    )
    .await
    .unwrap_err();
    assert_eq!(error.exit_code, 143);
    tokio::time::sleep(Duration::from_millis(1)).await;
    assert_eq!(active.load(Ordering::SeqCst), 0);
}

#[tokio::test]
async fn broken_output_pipe_stops_the_engine() {
    use tokio::io::AsyncWriteExt;
    let (mut output, reader) = tokio::io::duplex(128);
    drop(reader);
    let fake = Fake::new(|_, store, parts| Ok(response(store, parts, Availability::InStock)));
    let error = monitor(
        fake,
        vec![target("MWUC3CH/A")],
        config(),
        Mode::Watch {
            until_in_stock: false,
        },
        &mut output,
    )
    .await
    .unwrap_err();
    assert_eq!(error.exit_code, 141);
    let _ = output.shutdown().await;
}
