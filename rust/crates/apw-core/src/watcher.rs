//! 库存监控的调度引擎。
//!
//! 引擎不依赖任何界面框架：对外只暴露一个命令句柄和一条事件流，由上层决定
//! 如何展示与通知。
//!
//! # 为什么是 actor 而不是「共享状态 + 锁」
//!
//! Go 版把状态放在共享的 map 里用读写锁保护，再用另一把锁串行化启停。那套写法
//! 一路踩了这些坑：`Stop` 必须先放锁才能去等旧循环退出，中间的窗口让并发的
//! `Start` 拉起了第二条循环；`defer close(e.done)` 在 defer 语句执行时就读了字段，
//! 与 `Stop` 置 nil 构成竞态，还可能 `close(nil)` 直接崩掉进程；循环因 panic 退出后
//! 生命周期标志没复位，引擎变成叫不醒的僵尸。
//!
//! 每一个都是靠加锁、加标志、加 recover 一个个补上的。这里换成 actor：
//! **一个任务独占全部状态，外界只能发消息**。没有共享可变状态，就没有锁序、
//! 没有「放锁去等待」的窗口、也没有第二条循环存在的可能 —— 这一整类问题是从
//! 结构上消失的，不是被堵住的。
//!
//! 另一个 Rust 特有的好处：future 是「丢弃即取消」的。要中断一轮正在进行的查询，
//! 把那个 future 丢掉就行，在飞的 HTTP 请求会一并取消，不需要像 Go 那样把 context
//! 一层层往下传，也就不会漏传。

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use rand::Rng;
use tokio::sync::{Semaphore, mpsc, oneshot};
use tokio::task::JoinSet;

use crate::apple::{ApiError, Fetcher};
use crate::model::{Availability, Target, TargetKey, UnknownReason, region_by_locale};

/// 单个监控目标的当前状态。
///
/// 注意这里**没有**独立的 `last_error` 字段：原因就装在
/// [`Availability::Unknown`] 里。Go 版把两者拆开，于是它们可能不同步 ——
/// 独立审查挑出的两条最严重的缺陷，根子都在这种不同步上。
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TargetState {
    pub target: Target,
    pub availability: Availability,
    /// 最近一次完成查询的时刻（Unix 毫秒），`None` 表示还没查过。
    pub last_checked_ms: Option<u64>,
    /// 连续失败次数，供界面展示。
    pub consecutive_failures: u32,
}

impl TargetState {
    fn new(target: Target) -> Self {
        Self {
            target,
            availability: Availability::Unknown(UnknownReason::NotYetChecked),
            last_checked_ms: None,
            consecutive_failures: 0,
        }
    }
}

/// 引擎向外发出的事件。
#[derive(Debug, Clone, serde::Serialize)]
#[serde(tag = "type", rename_all = "camelCase")]
pub enum Event {
    /// 某个目标的状态发生了变化。可丢弃：真实状态随时能从快照重新取。
    StateChanged { state: TargetState },
    /// 某个目标从非有货变成了有货 —— 需要提醒用户的时刻。
    ///
    /// **这条事件不允许丢**：它是整个程序存在的理由。投递用的是会产生背压的
    /// `send().await`，而不是丢弃式的 `try_send`。Go 版对所有事件一视同仁地
    /// 满即丢，于是界面一卡顿，用户就会看到「有货」却收不到任何提醒。
    InStock { state: TargetState },
    /// 一轮查询结束，带上完整快照。
    ///
    /// 快照让界面任何时候都能整体对齐，不必依赖那些可丢弃事件是否都收到了。
    CycleComplete {
        /// 本轮是否所有目标都拿到了明确答复。
        ///
        /// 界面需要一个明确的「恢复」信号才能收起故障告警。用「所有行都没有
        /// 错误」去反推是不可靠的：某些故障路径下状态根本没被更新，旧的错误
        /// 早已被清掉，于是刚亮起的告警会被同一轮的结束事件立刻收走，还附赠
        /// 一句「已恢复正常」的假陈述。恢复与否只有引擎自己知道。
        healthy: bool,
        snapshot: Vec<TargetState>,
    },
    /// 出现了需要用户注意的持续性故障。
    Trouble { reason: String },
    /// 监控的启停状态发生了变化。
    RunStateChanged { running: bool },
}

/// 引擎配置。
#[derive(Debug, Clone)]
pub struct WatcherConfig {
    /// 每轮查询之间的基础间隔。
    ///
    /// 上游写死 500 毫秒一轮，即每个门店每秒两次请求。这个频率对一个公开的
    /// 商品查询接口来说过高，是触发风控的直接原因。
    pub interval: Duration,
    /// 间隔抖动比例（0 到 1）。固定周期本身就是机器人特征。
    pub jitter: f64,
    /// 同一轮内并发查询的门店数上限。
    pub concurrency: usize,
    /// 事件通道容量。
    pub event_buffer: usize,
}

impl Default for WatcherConfig {
    fn default() -> Self {
        Self {
            interval: Duration::from_secs(30),
            jitter: 0.2,
            concurrency: 4,
            event_buffer: 256,
        }
    }
}

enum Command {
    SetTargets(Vec<Target>),
    SetInterval(Duration),
    Start(oneshot::Sender<()>),
    Stop(oneshot::Sender<()>),
    Snapshot(oneshot::Sender<Vec<TargetState>>),
    IsRunning(oneshot::Sender<bool>),
}

/// 引擎句柄，克隆代价极低，可以随意传递。
#[derive(Debug, Clone)]
pub struct Watcher {
    cmd: mpsc::Sender<Command>,
}

impl Watcher {
    /// 起一个引擎任务，返回句柄与事件流。
    pub fn spawn<F: Fetcher>(client: F, config: WatcherConfig) -> (Self, mpsc::Receiver<Event>) {
        let (cmd_tx, cmd_rx) = mpsc::channel(64);
        let (evt_tx, evt_rx) = mpsc::channel(config.event_buffer);

        tokio::spawn(Engine::new(client, config, evt_tx).run(cmd_rx));

        (Self { cmd: cmd_tx }, evt_rx)
    }

    /// 替换监控目标列表，保留仍然存在的目标的既有状态。
    pub async fn set_targets(&self, targets: Vec<Target>) {
        let _ = self.cmd.send(Command::SetTargets(targets)).await;
    }

    /// 调整查询间隔，下一轮等待时生效。
    pub async fn set_interval(&self, interval: Duration) {
        let _ = self.cmd.send(Command::SetInterval(interval)).await;
    }

    /// 启动监控。返回时引擎已经进入运行态。
    pub async fn start(&self) {
        let (tx, rx) = oneshot::channel();
        if self.cmd.send(Command::Start(tx)).await.is_ok() {
            let _ = rx.await;
        }
    }

    /// 停止监控。**返回时正在进行的那一轮查询确实已经结束。**
    ///
    /// 这个承诺在 actor 模型下是免费的：引擎串行处理命令，它回复的时候本轮
    /// 必然已经收尾。Go 版为了做到同样的事，前后加了两把锁和一个状态标志，
    /// 还是留下了并发窗口。
    pub async fn stop(&self) {
        let (tx, rx) = oneshot::channel();
        if self.cmd.send(Command::Stop(tx)).await.is_ok() {
            let _ = rx.await;
        }
    }

    /// 取当前全部状态的快照。
    pub async fn snapshot(&self) -> Vec<TargetState> {
        let (tx, rx) = oneshot::channel();
        if self.cmd.send(Command::Snapshot(tx)).await.is_err() {
            return Vec::new();
        }
        rx.await.unwrap_or_default()
    }

    /// 是否正在监控。
    pub async fn is_running(&self) -> bool {
        let (tx, rx) = oneshot::channel();
        if self.cmd.send(Command::IsRunning(tx)).await.is_err() {
            return false;
        }
        rx.await.unwrap_or(false)
    }
}

/// 一轮查询中按门店聚合出的一个请求单元。
#[derive(Debug, Clone)]
struct StoreGroup {
    locale: String,
    store_number: String,
    parts: Vec<String>,
}

/// 单个门店查询完的结果。
#[derive(Debug)]
struct StoreOutcome {
    locale: String,
    store_number: String,
    /// 每个**请求过**的零件号对应的判定结果。
    parts: Vec<(String, Availability)>,
    /// 这次门店查询是否算成功，用于全局退避判断。
    ok: bool,
    /// 本次遇到的异常数量，用于判断整轮是否健康。
    problems: usize,
    /// 需要弹告警时的原因。
    trouble: Option<String>,
}

/// 执行一轮查询。
///
/// 刻意写成不借用引擎的自由函数：调度循环需要一边跑这个 future、一边继续响应
/// 命令，如果它借着 `&mut self`，另一个分支就什么都动不了。顺带把「发请求」和
/// 「改状态」分开了，网络部分成了纯函数，测试时不必构造整个引擎。
async fn run_queries<F: Fetcher>(
    client: F,
    groups: Vec<StoreGroup>,
    concurrency: usize,
) -> Vec<StoreOutcome> {
    let sem = Arc::new(Semaphore::new(concurrency.max(1)));
    let mut set = JoinSet::new();

    for group in groups {
        let client = client.clone();
        let sem = Arc::clone(&sem);
        set.spawn(async move {
            // 拿不到许可只可能是信号量被关闭，这里不会发生；真发生了也只是
            // 少查一个门店，不该让整轮崩掉。
            let _permit = sem.acquire_owned().await;
            query_one_store(&client, group).await
        });
    }

    let mut outcomes = Vec::new();
    while let Some(joined) = set.join_next().await {
        match joined {
            Ok(outcome) => outcomes.push(outcome),
            Err(err) => {
                // 子任务 panic 了。Rust 不像 Go 那样会带走整个进程，但绝不能
                // 当作没发生 —— 这些目标的状态必须被标成未知，否则它们会停在
                // 上一轮的取值上，而那很可能正是「无货」。
                //
                // JoinError 拿不到是哪个门店，所以这里只发告警；具体目标的状态
                // 由调度层按「本轮没收到结果」统一处理。
                outcomes.push(StoreOutcome {
                    locale: String::new(),
                    store_number: String::new(),
                    parts: Vec::new(),
                    ok: false,
                    problems: 1,
                    trouble: Some(format!("查询任务内部错误已被拦截：{err}")),
                });
            }
        }
    }
    outcomes
}

async fn query_one_store<F: Fetcher>(client: &F, group: StoreGroup) -> StoreOutcome {
    let Some(region) = region_by_locale(&group.locale) else {
        // 地区认不出来就压根发不出请求。必须登记成故障，不能静默跳过 ——
        // 静默跳过会让这些行永远停在「待查询」，用户看不出程序从没查过它们。
        let reason = UnknownReason::SchemaDrift {
            field: "locale".into(),
            raw: group.locale.clone(),
        };
        let n = group.parts.len();
        return StoreOutcome {
            parts: group
                .parts
                .into_iter()
                .map(|p| (p, Availability::Unknown(reason.clone())))
                .collect(),
            trouble: Some(format!(
                "地区 {} 无法识别，这些监控项无法查询，请删除后重新添加",
                group.locale
            )),
            locale: group.locale,
            store_number: group.store_number,
            ok: false,
            problems: n,
        };
    };

    match client
        .pickup_message(region, &group.store_number, &group.parts)
        .await
    {
        Err(err) => {
            let trouble = matches!(err, ApiError::Blocked(_) | ApiError::SchemaDrift { .. })
                .then(|| format!("门店 {} 查询失败：{err}", group.store_number));

            let reason = err.into_unknown_reason();
            let n = group.parts.len();
            StoreOutcome {
                parts: group
                    .parts
                    .into_iter()
                    .map(|p| (p, Availability::Unknown(reason.clone())))
                    .collect(),
                locale: group.locale,
                store_number: group.store_number,
                ok: false,
                problems: n,
                trouble,
            }
        }
        Ok(result) => {
            let mut parts = Vec::with_capacity(group.parts.len());
            let mut problems = 0usize;
            // 真正拿到明确答复（有货或无货）的型号数。
            let mut resolved = 0usize;

            for part in &group.parts {
                match result.parts.get(part) {
                    None => {
                        // 请求成功但响应里没有这个型号，通常意味着零件号已经下架
                        // 或写错。这属于「查不到」，绝不能当作「无货」。
                        problems += 1;
                        parts.push((
                            part.clone(),
                            Availability::Unknown(UnknownReason::SchemaDrift {
                                field: "partsAvailability".into(),
                                raw: format!("响应中没有型号 {part}"),
                            }),
                        ));
                    }
                    Some(status) => {
                        if status.availability.is_failure() {
                            problems += 1;
                        } else {
                            resolved += 1;
                        }
                        parts.push((part.clone(), status.availability.clone()));
                    }
                }
            }

            // 一个型号都没拿到明确答复，说明这个门店这一轮实质上是废的：
            // 要么零件号全对不上，要么 Apple 换了词表。必须按门店级失败处理，
            // 否则不退避、不告警，程序会继续按原频率请求一个已经失效的结构。
            let dead = resolved == 0 && !group.parts.is_empty();
            StoreOutcome {
                trouble: dead.then(|| {
                    format!(
                        "门店 {} 的全部型号都没能拿到明确答复，Apple 可能已调整接口",
                        group.store_number
                    )
                }),
                locale: group.locale,
                store_number: group.store_number,
                parts,
                ok: !dead,
                problems,
            }
        }
    }
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or_default()
}

/// 引擎本体。所有字段都由这一个任务独占，因此不需要任何锁。
struct Engine<F: Fetcher> {
    client: F,
    config: WatcherConfig,
    events: mpsc::Sender<Event>,

    targets: Vec<Target>,
    states: BTreeMap<TargetKey, TargetState>,
    running: bool,
    /// 连续「整轮全败」的次数，用于全局退避。
    ///
    /// 不用单个目标的失败次数来驱动：某个零件号下架会让它永远失败，据此退避
    /// 的话，一条陈旧的监控项就能把所有正常门店的查询频率拖慢八倍。
    cycle_failures: u32,
}

impl<F: Fetcher> Engine<F> {
    fn new(client: F, config: WatcherConfig, events: mpsc::Sender<Event>) -> Self {
        Self {
            client,
            config,
            events,
            targets: Vec::new(),
            states: BTreeMap::new(),
            running: false,
            cycle_failures: 0,
        }
    }

    async fn run(mut self, mut cmd_rx: mpsc::Receiver<Command>) {
        loop {
            if !self.running {
                // 没在跑就安静地等命令，不浪费任何一次唤醒。
                match cmd_rx.recv().await {
                    Some(cmd) => self.handle_command(cmd).await,
                    None => return, // 句柄全没了，收工。
                }
                continue;
            }

            // 跑一轮。期间仍然响应命令：把 future 钉住反复轮询，SetTargets
            // 之类的命令不会打断本轮，而 Stop 会直接丢弃它 —— 丢弃即取消，
            // 在飞的 HTTP 请求会跟着一起停。
            let groups = self.group_targets();
            let queries = run_queries(self.client.clone(), groups, self.config.concurrency);
            tokio::pin!(queries);

            let outcomes = loop {
                tokio::select! {
                    // 优先把本轮跑完，避免命令频繁到来时把查询饿死。
                    biased;
                    outcomes = &mut queries => break Some(outcomes),
                    maybe = cmd_rx.recv() => match maybe {
                        Some(Command::Stop(reply)) => {
                            // 离开这个循环时 queries 被丢弃，本轮作废。
                            self.set_running(false).await;
                            let _ = reply.send(());
                            break None;
                        }
                        Some(cmd) => self.handle_command(cmd).await,
                        None => return,
                    },
                }
            };

            let Some(outcomes) = outcomes else { continue };
            self.apply(outcomes).await;

            if !self.running {
                continue;
            }

            let delay = self.next_delay();
            tokio::select! {
                () = tokio::time::sleep(delay) => {}
                maybe = cmd_rx.recv() => match maybe {
                    Some(cmd) => self.handle_command(cmd).await,
                    None => return,
                },
            }
        }
    }

    async fn handle_command(&mut self, cmd: Command) {
        match cmd {
            Command::SetTargets(targets) => self.set_targets(targets),
            Command::SetInterval(d) => {
                if d > Duration::ZERO {
                    self.config.interval = d;
                }
            }
            Command::Start(reply) => {
                self.set_running(true).await;
                let _ = reply.send(());
            }
            Command::Stop(reply) => {
                self.set_running(false).await;
                let _ = reply.send(());
            }
            Command::Snapshot(reply) => {
                let _ = reply.send(self.snapshot());
            }
            Command::IsRunning(reply) => {
                let _ = reply.send(self.running);
            }
        }
    }

    async fn set_running(&mut self, running: bool) {
        if self.running == running {
            return;
        }
        self.running = running;
        if !running {
            // 重新启动时应当从干净的节奏开始，不背着上一轮的退避。
            self.cycle_failures = 0;
        }
        self.emit_droppable(Event::RunStateChanged { running });
    }

    fn set_targets(&mut self, targets: Vec<Target>) {
        let mut next = BTreeMap::new();
        for t in &targets {
            let key = t.key();
            // 保留仍然存在的目标的既有状态，避免每次改列表都把已知状态清空。
            let state = self
                .states
                .remove(&key)
                .map(|mut s| {
                    s.target = t.clone();
                    s
                })
                .unwrap_or_else(|| TargetState::new(t.clone()));
            next.insert(key, state);
        }
        self.states = next;
        self.targets = targets;
    }

    fn snapshot(&self) -> Vec<TargetState> {
        // BTreeMap 本身有序，键是「地区|门店|零件号」，正好符合界面的分组直觉。
        self.states.values().cloned().collect()
    }

    /// 把目标按 (地区, 门店) 聚合，使每个门店每轮只发一次请求。
    fn group_targets(&self) -> Vec<StoreGroup> {
        let mut order: Vec<(String, String)> = Vec::new();
        let mut index: BTreeMap<(String, String), Vec<String>> = BTreeMap::new();

        for t in &self.targets {
            let k = (t.locale.clone(), t.store_number.clone());
            if !index.contains_key(&k) {
                order.push(k.clone());
            }
            index.entry(k).or_default().push(t.part_number.clone());
        }

        order
            .into_iter()
            .map(|(locale, store_number)| {
                let parts = index.remove(&(locale.clone(), store_number.clone()));
                StoreGroup {
                    locale,
                    store_number,
                    parts: parts.unwrap_or_default(),
                }
            })
            .collect()
    }

    /// 把一轮的结果写进状态，并发出相应事件。
    async fn apply(&mut self, outcomes: Vec<StoreOutcome>) {
        let mut ok = 0usize;
        let mut failed = 0usize;
        let mut problems = 0usize;
        let now = now_ms();

        for outcome in outcomes {
            if outcome.ok {
                ok += 1;
            } else {
                failed += 1;
            }
            problems += outcome.problems;

            if let Some(reason) = outcome.trouble {
                self.emit_droppable(Event::Trouble { reason });
            }

            for (part, availability) in outcome.parts {
                let key = TargetKey(format!(
                    "{}|{}|{}",
                    outcome.locale, outcome.store_number, part
                ));
                // 目标可能在本轮进行中被用户删掉了，直接忽略。
                let Some(state) = self.states.get_mut(&key) else {
                    continue;
                };

                let previous = std::mem::replace(&mut state.availability, availability);
                state.last_checked_ms = Some(now);
                if state.availability.is_failure() {
                    state.consecutive_failures = state.consecutive_failures.saturating_add(1);
                } else {
                    state.consecutive_failures = 0;
                }

                if previous == state.availability {
                    continue;
                }

                let snapshot = state.clone();
                let became_in_stock = snapshot.availability.is_in_stock();
                self.emit_droppable(Event::StateChanged {
                    state: snapshot.clone(),
                });
                if became_in_stock {
                    // 只在「变为有货」的瞬间提醒一次，持续有货不会重复响；
                    // 补货（离开有货再回来）时会再次触发。
                    self.emit_critical(Event::InStock { state: snapshot }).await;
                }
            }
        }

        // 只有一个门店都没查成功，才认为是全局故障，进入退避。
        if failed > 0 && ok == 0 {
            self.cycle_failures = self.cycle_failures.saturating_add(1);
        } else {
            self.cycle_failures = 0;
        }

        self.emit_droppable(Event::CycleComplete {
            healthy: problems == 0 && ok > 0,
            snapshot: self.snapshot(),
        });
    }

    /// 下一轮的等待时长，含抖动与全局退避。
    fn next_delay(&self) -> Duration {
        let mut base = self.config.interval;

        // 整轮全败时逐步拉长间隔，最多放大到 8 倍。被拦截还按原频率猛冲，
        // 只会让风控更严。
        if self.cycle_failures > 0 {
            let factor = 1u32 << self.cycle_failures.min(3);
            base = base.saturating_mul(factor);
        }

        if self.config.jitter <= 0.0 {
            return base;
        }
        let delta = rand::rng().random_range(-self.config.jitter..=self.config.jitter);
        let secs = base.as_secs_f64() * (1.0 + delta);
        Duration::from_secs_f64(secs.max(1.0))
    }

    /// 投递可以丢弃的事件。
    ///
    /// 通道写满时直接丢：让界面卡顿去拖慢监控本身是本末倒置的，而这些事件
    /// 承载的信息都能从 `CycleComplete` 带的快照里重新拿到。
    fn emit_droppable(&self, event: Event) {
        let _ = self.events.try_send(event);
    }

    /// 投递不允许丢失的事件。
    ///
    /// 到货提醒是这个程序存在的全部理由，宁可让引擎在这里等一会儿产生背压，
    /// 也不能像 Go 版那样满了就丢 —— 那会让用户在列表里看到「有货」，却
    /// 完全收不到任何提醒。
    async fn emit_critical(&self, event: Event) {
        let _ = self.events.send(event).await;
    }
}
