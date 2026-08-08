//! Tauri 应用外壳。
//!
//! 这一层只做装配和转译：起引擎、把命令转成引擎消息、把引擎事件转发给前端。
//! **所有业务判断都在 `apw-core` 里**，这里不许出现任何「什么算有货」之类的逻辑 ——
//! 一旦让界面层参与判断，那条核心不变量就多了一处可以被绕开的地方。

use std::time::Duration;

use apw_core::apple::{AppleClient, ClientConfig};
use apw_core::model::{REGIONS, Target};
use apw_core::watcher::{TargetState, Watcher, WatcherConfig};
use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager};

/// 前端事件通道名。前端用 `listen("watcher://event", ...)` 订阅。
const EVENT_CHANNEL: &str = "watcher://event";

/// 地区的可序列化形式。
///
/// `model::Region` 的字段都是 `&'static str`，不适合直接跨 IPC 传，
/// 而且界面也不需要知道 `base_url` 这类内部细节。
#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct RegionDto {
    title: &'static str,
    locale: &'static str,
}

struct AppState {
    watcher: Watcher,
}

#[tauri::command]
fn list_regions() -> Vec<RegionDto> {
    REGIONS
        .iter()
        .map(|r| RegionDto {
            title: r.title,
            locale: r.locale,
        })
        .collect()
}

#[tauri::command]
async fn get_snapshot(state: tauri::State<'_, AppState>) -> Result<Vec<TargetState>, String> {
    Ok(state.watcher.snapshot().await)
}

#[tauri::command]
async fn set_targets(
    state: tauri::State<'_, AppState>,
    targets: Vec<Target>,
) -> Result<Vec<TargetState>, String> {
    state.watcher.set_targets(targets).await;
    Ok(state.watcher.snapshot().await)
}

#[tauri::command]
async fn set_interval(state: tauri::State<'_, AppState>, seconds: u64) -> Result<(), String> {
    // 下限在这里挡一道。上游写死 500 毫秒一轮，那正是被风控盯上的原因。
    let secs = seconds.max(5);
    state.watcher.set_interval(Duration::from_secs(secs)).await;
    Ok(())
}

#[tauri::command]
async fn start_watching(state: tauri::State<'_, AppState>) -> Result<(), String> {
    state.watcher.start().await;
    Ok(())
}

#[tauri::command]
async fn stop_watching(state: tauri::State<'_, AppState>) -> Result<(), String> {
    state.watcher.stop().await;
    Ok(())
}

#[tauri::command]
async fn is_running(state: tauri::State<'_, AppState>) -> Result<bool, String> {
    Ok(state.watcher.is_running().await)
}

pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .setup(|app| {
            let client = AppleClient::new(ClientConfig::default())
                .map_err(|e| format!("构造 Apple 客户端失败：{e}"))?;
            // 用 Watcher::new 而不是 Watcher::spawn：setup 回调跑在主线程上，
            // 并不处在 tokio 运行时上下文里，在这里 tokio::spawn 会 panic，
            // 而且因为发生在不可展开的回调中，进程会直接 abort。
            // 引擎任务交给 Tauri 自己的运行时去驱动。
            let (watcher, mut events, engine) = Watcher::new(client, WatcherConfig::default());
            tauri::async_runtime::spawn(engine);

            app.manage(AppState {
                watcher: watcher.clone(),
            });

            // 把引擎事件原样转发给前端。
            //
            // 刻意不在这里做任何过滤或聚合：前端拿到的事件流应当与引擎发出的
            // 完全一致，中间少一层可能出错的翻译。
            let handle: AppHandle = app.handle().clone();
            tauri::async_runtime::spawn(async move {
                while let Some(event) = events.recv().await {
                    // 发送失败只可能是窗口已经销毁，此时无需处理。
                    let _ = handle.emit(EVENT_CHANNEL, &event);
                }
            });

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            list_regions,
            get_snapshot,
            set_targets,
            set_interval,
            start_watching,
            stop_watching,
            is_running,
        ])
        .run(tauri::generate_context!())
        .expect("Tauri 应用启动失败");
}
