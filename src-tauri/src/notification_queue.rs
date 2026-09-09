//! 有界通知队列。慢速推送不应让常见的一轮到货事件卡住界面与启停状态。

use std::future::Future;

use tokio::sync::mpsc;
use tokio::task::JoinSet;

const QUEUE_CAPACITY: usize = 256;
const CONCURRENCY: usize = 4;

pub(crate) fn channel<T>() -> (mpsc::Sender<T>, mpsc::Receiver<T>) {
    mpsc::channel(QUEUE_CAPACITY)
}

pub(crate) async fn run<T, F, Fut>(mut incoming: mpsc::Receiver<T>, mut dispatch: F)
where
    T: Send + 'static,
    F: FnMut(T) -> Fut,
    Fut: Future<Output = ()> + Send + 'static,
{
    let mut active = JoinSet::new();
    loop {
        if active.len() == CONCURRENCY {
            let _ = active.join_next().await;
            continue;
        }
        tokio::select! {
            item = incoming.recv() => match item {
                Some(item) => { active.spawn(dispatch(item)); }
                None => break,
            },
            _ = active.join_next(), if !active.is_empty() => {},
        }
    }

    // 发送方关闭后，已经接收的通知也要发完。
    while active.join_next().await.is_some() {}
}

#[cfg(test)]
mod tests {
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::Duration;

    use tokio::sync::Semaphore;
    use tokio::time::timeout;

    use super::*;

    #[tokio::test]
    async fn 十五项事件无需等待慢通知且所有通知最终送达并限制并发() {
        let (sender, receiver) = channel();
        let (started, mut starts) = mpsc::channel(15);
        let release = Arc::new(Semaphore::new(0));
        let active = Arc::new(AtomicUsize::new(0));
        let peak = Arc::new(AtomicUsize::new(0));
        let delivered = Arc::new(Mutex::new(Vec::new()));

        let worker = tokio::spawn(run(receiver, {
            let release = release.clone();
            let active = active.clone();
            let peak = peak.clone();
            let delivered = delivered.clone();
            move |item| {
                let release = release.clone();
                let active = active.clone();
                let peak = peak.clone();
                let delivered = delivered.clone();
                let started = started.clone();
                async move {
                    let count = active.fetch_add(1, Ordering::SeqCst) + 1;
                    peak.fetch_max(count, Ordering::SeqCst);
                    started.send(item).await.unwrap();
                    // 模拟一直未返回的 Bark，测试结束前不允许任一通知完成。
                    release.acquire().await.unwrap().forget();
                    delivered.lock().unwrap().push(item);
                    active.fetch_sub(1, Ordering::SeqCst);
                }
            }
        }));

        let forwarded = timeout(Duration::from_secs(1), async {
            let mut forwarded = Vec::new();
            for item in 0..15 {
                forwarded.push(item);
                sender.send(item).await.unwrap();
            }
            forwarded
        })
        .await
        .expect("事件转发不应等待慢通知完成");
        assert_eq!(forwarded, (0..15).collect::<Vec<_>>());
        assert!(delivered.lock().unwrap().is_empty());

        for _ in 0..CONCURRENCY {
            timeout(Duration::from_secs(1), starts.recv())
                .await
                .unwrap()
                .unwrap();
        }
        assert!(starts.try_recv().is_err(), "不应启动第5个并发任务");

        // 关闭发送方也不能丢弃队列里的其余通知。
        drop(sender);
        release.add_permits(15);
        timeout(Duration::from_secs(1), worker)
            .await
            .unwrap()
            .unwrap();
        let mut delivered = delivered.lock().unwrap().clone();
        delivered.sort_unstable();
        assert_eq!(delivered, (0..15).collect::<Vec<_>>());
        assert_eq!(peak.load(Ordering::SeqCst), CONCURRENCY);
    }
}
