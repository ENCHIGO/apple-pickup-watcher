// Package watcher 实现库存监控的调度引擎。
//
// 引擎不依赖任何 UI 框架：它对外只暴露状态快照和事件流，由上层决定如何
// 展示与通知。上游把调度、状态、界面刷新和弹窗全部写在一个 goroutine 里
// （services/listen.go:119-157），既无法测试，也导致了下面注释中提到的
// 若干并发问题。
package watcher

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// State 是单个监控目标的当前状态。
type State struct {
	Target       model.Target
	Availability model.Availability
	// LastChecked 是最近一次完成查询的时间，零值表示尚未查询过。
	LastChecked time.Time
	// LastError 记录最近一次查询失败的原因；成功后清空。
	// Availability 为 Unknown 且 LastError 非空时，界面应当显示错误而不是「无货」。
	LastError error
	// ConsecutiveFailures 用于退避，连续失败越多，下一轮等待越久。
	ConsecutiveFailures int
}

// EventKind 区分事件类型。
type EventKind int

const (
	// EventStateChanged 表示某个目标的状态发生了变化。
	EventStateChanged EventKind = iota
	// EventInStock 表示某个目标从非有货变为有货，是需要通知用户的时刻。
	EventInStock
	// EventCycleComplete 表示一轮查询结束，界面可以整体刷新。
	EventCycleComplete
	// EventTrouble 表示出现了持续性故障（例如接口被拦截），需要提示用户。
	EventTrouble
)

// Event 是引擎向外发出的事件。
type Event struct {
	Kind   EventKind
	State  State
	At     time.Time
	Reason string
	// Healthy 只在 EventCycleComplete 上有意义：为真表示本轮所有目标都拿到了
	// 明确的答复，没有任何失败、缺失或无法识别的取值。
	//
	// 界面需要一个明确的「恢复」信号才能收起故障告警。用「所有行都没有错误」
	// 去反推恢复是不可靠的：某些故障路径下状态根本没被更新，旧的错误早已被清掉，
	// 于是刚亮起的告警会被同一轮的 CycleComplete 立刻收走，还附赠一句
	// 「查询已恢复正常」的假陈述。恢复与否只有引擎自己知道，必须由引擎明说。
	Healthy bool
}

// Option 用于定制引擎。
type Option func(*Engine)

// WithInterval 设置每轮查询之间的基础间隔。
func WithInterval(d time.Duration) Option {
	return func(e *Engine) { e.interval = d }
}

// WithJitter 设置间隔抖动比例（0 到 1）。
//
// 固定周期的请求本身就是机器人特征，抖动让请求节奏不那么规律。
func WithJitter(fraction float64) Option {
	return func(e *Engine) { e.jitter = fraction }
}

// WithConcurrency 设置同一轮内并发查询的门店数上限。
func WithConcurrency(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.concurrency = n
		}
	}
}

// WithClock 替换时间源，供测试使用。
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// WithRand 替换随机源，供测试使用。
func WithRand(r *rand.Rand) Option {
	return func(e *Engine) { e.rng = r }
}

// Engine 是监控调度引擎，所有导出方法均并发安全。
type Engine struct {
	client Fetcher

	// cfgMu 保护运行期间可调整的参数。用户在界面上改查询间隔时，
	// 引擎往往正在运行，不能直接写字段。
	cfgMu    sync.RWMutex
	interval time.Duration
	jitter   float64

	concurrency int
	now         func() time.Time

	// cycleFailures 记录「整轮查询全军覆没」连续发生了多少次，用于全局退避。
	//
	// 不用单个目标的失败次数来驱动退避：某个零件号下架会让它永远失败，
	// 若据此退避，一条陈旧的监控项就能把所有正常门店的查询频率拖慢八倍。
	// 只有当本轮所有门店都失败（说明是被拦截或断网这类全局故障）才退避。
	cycleFailures int

	// mu 保护 targets / states。
	//
	// 上游的 items map 在 UI 线程（main.go:125 的「添加」按钮、
	// services/listen.go:81 的 Clean）和后台轮询 goroutine
	// （services/listen.go:112-117 的 UpdateStatus）之间无锁共享读写，
	// 这在 Go 里会触发 fatal error: concurrent map read and map write
	// 直接终止进程，且无法被 recover 捕获。这是上游「用着用着就闪退」
	// 类问题的一个来源。
	mu      sync.RWMutex
	targets []model.Target
	states  map[string]*State

	rngMu sync.Mutex
	rng   *rand.Rand

	events chan Event

	runMu   sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// Fetcher 抽象出引擎依赖的查询能力，便于在测试中替换掉真实网络请求。
type Fetcher interface {
	PickupMessage(ctx context.Context, region model.Region, storeNumber string, parts []string) (*apple.StoreAvailability, error)
}

// New 构造一个引擎。
func New(client Fetcher, opts ...Option) *Engine {
	e := &Engine{
		client:      client,
		interval:    30 * time.Second,
		jitter:      0.2,
		concurrency: 4,
		now:         time.Now,
		states:      map[string]*State{},
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		events:      make(chan Event, 256),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Events 返回事件流。引擎绝不会阻塞在这个通道上：缓冲写满时新事件被丢弃，
// 因为让界面卡住去拖慢监控本身是本末倒置的。
func (e *Engine) Events() <-chan Event { return e.events }

// SetTargets 替换监控目标列表，并保留仍然存在的目标的既有状态。
func (e *Engine) SetTargets(targets []model.Target) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.targets = append([]model.Target(nil), targets...)

	next := make(map[string]*State, len(targets))
	for _, t := range targets {
		if existing, ok := e.states[t.Key()]; ok {
			existing.Target = t
			next[t.Key()] = existing
			continue
		}
		next[t.Key()] = &State{Target: t, Availability: model.Unknown}
	}
	e.states = next
}

// Targets 返回当前监控目标的副本。
func (e *Engine) Targets() []model.Target {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.Target(nil), e.targets...)
}

// Snapshot 返回当前所有状态的副本，按门店与型号排序，供界面渲染。
func (e *Engine) Snapshot() []State {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]State, 0, len(e.states))
	for _, s := range e.states {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Target.StoreTitle != out[j].Target.StoreTitle {
			return out[i].Target.StoreTitle < out[j].Target.StoreTitle
		}
		return out[i].Target.ProductName < out[j].Target.ProductName
	})
	return out
}

// Running 报告引擎是否正在运行。
func (e *Engine) Running() bool {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	return e.running
}

// Start 启动监控循环。重复调用无副作用。
func (e *Engine) Start() {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if e.running {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.cancel = cancel
	e.done = done
	e.running = true

	// 必须把 done 捕获成局部变量再交给 goroutine。
	//
	// 写成 defer close(e.done) 是错的：defer 语句在执行时就会读一次 e.done 字段，
	// 而这次读没有持有 runMu，与 Stop 里把 e.done 置 nil（本文件 Stop 方法内）
	// 构成数据竞争。更要命的是，一旦 Stop 抢先把字段清空，goroutine 读到的就是
	// nil，close(nil) 会 panic —— 而这个 defer 注册得最早、执行得最晚，
	// 下面那个 recover 兜底早已运行结束，根本拦不住它，进程直接崩。
	go func() {
		defer close(done)
		// 兜底：引擎内部任何未预料的 panic 都不应该带走整个应用。
		// 上游在后台 goroutine 里直接 panic（services/listen.go:282 的
		// AlertMp3、:302 的 Bark 推送），而 Go 中任意 goroutine 的
		// panic 未被捕获就会终止进程，这正是「bark 闪退」的成因。
		defer func() {
			if r := recover(); r != nil {
				e.emit(Event{
					Kind:   EventTrouble,
					At:     e.now(),
					Reason: fmt.Sprintf("监控循环内部错误已被拦截: %v", r),
				})
			}
		}()
		e.loop(ctx)
	}()
}

// Stop 停止监控循环并等待其退出。重复调用与并发调用都是安全的，
// 且每一个调用方返回时，监控循环都确实已经结束。
func (e *Engine) Stop() {
	e.runMu.Lock()
	done, cancel := e.done, e.cancel
	if e.running {
		e.running = false
		// 这里不把 e.done 置 nil：它要留给后来的调用方等待。
		// 之前的写法一并清空了 done，于是并发的第二个 Stop 看到
		// running 已是 false 就直接返回，此时监控循环可能还在跑
		// —— 方法名承诺了「等待其退出」，却没有真的等。
		e.cancel = nil
		e.runMu.Unlock()
		cancel()
	} else {
		e.runMu.Unlock()
	}

	if done != nil {
		// 循环退出时会 close 这个通道；已经关闭的通道立即返回。
		<-done
	}
}

// loop 是监控主循环。
//
// 与上游那个 for {} + time.Sleep(500ms) 且无退出条件的循环
// （services/listen.go:122-156）不同，这里通过 context 退出，
// 间隔可配置且带抖动。
func (e *Engine) loop(ctx context.Context) {
	for {
		e.runCycle(ctx)

		select {
		case <-ctx.Done():
			return
		case <-time.After(e.nextDelay()):
		}
	}
}

// SetInterval 调整查询间隔，在下一轮等待时生效。
//
// 界面上改间隔时引擎通常正在运行，因此这个方法必须能被安全地并发调用。
func (e *Engine) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	e.cfgMu.Lock()
	e.interval = d
	e.cfgMu.Unlock()
}

// Interval 返回当前查询间隔。
func (e *Engine) Interval() time.Duration {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.interval
}

// nextDelay 返回下一轮的等待时长，包含抖动与全局失败退避。
func (e *Engine) nextDelay() time.Duration {
	e.cfgMu.RLock()
	base, jitter := e.interval, e.jitter
	e.cfgMu.RUnlock()

	// 整轮全败时逐步拉长间隔，最多放大到 8 倍。被拦截还继续按原频率猛冲，
	// 只会让风控更严。
	e.mu.RLock()
	failures := e.cycleFailures
	e.mu.RUnlock()
	if failures > 0 {
		base *= time.Duration(1 << min(failures, 3))
	}

	if jitter <= 0 {
		return base
	}
	e.rngMu.Lock()
	delta := (e.rng.Float64()*2 - 1) * jitter
	e.rngMu.Unlock()

	d := time.Duration(float64(base) * (1 + delta))
	if d < time.Second {
		d = time.Second
	}
	return d
}

// storeGroup 是一轮查询中按门店聚合出的一个请求单元。
type storeGroup struct {
	region      model.Region
	storeNumber string
	parts       []string
}

// groupTargets 把目标按 (地区, 门店) 聚合，使每个门店每轮只发一次请求。
//
// 第二个返回值是地区无法识别、因而根本没法查的目标。它们必须被返回而不是丢掉：
// 直接 continue 会让这些行永远停在「待查询」的弱化配色上，用户看不出程序其实
// 从来没查过它们，也不知道原因。
func (e *Engine) groupTargets() ([]storeGroup, []model.Target) {
	e.mu.RLock()
	targets := append([]model.Target(nil), e.targets...)
	e.mu.RUnlock()

	index := map[string]*storeGroup{}
	order := make([]string, 0, len(targets))
	var invalid []model.Target

	for _, t := range targets {
		region, ok := model.RegionByLocale(t.Locale)
		if !ok {
			invalid = append(invalid, t)
			continue
		}
		key := t.Locale + "|" + t.StoreNumber
		g, exists := index[key]
		if !exists {
			g = &storeGroup{region: region, storeNumber: t.StoreNumber}
			index[key] = g
			order = append(order, key)
		}
		g.parts = append(g.parts, t.PartNumber)
	}

	groups := make([]storeGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, *index[key])
	}
	return groups, invalid
}

// cycleTally 汇总一轮查询的结果。
type cycleTally struct {
	sync.Mutex
	// ok 是查询成功且全部型号都拿到可识别答复的门店数。
	ok int
	// failed 是整店级失败的门店数，用于判断是否需要全局退避。
	failed int
	// problems 是本轮出现的任何异常总数（含个别型号缺失或取值无法识别），
	// 只要它大于零，本轮就不算健康。
	problems int
}

func (t *cycleTally) record(ok bool, problems int) {
	t.Lock()
	if ok {
		t.ok++
	} else {
		t.failed++
	}
	t.problems += problems
	t.Unlock()
}

// runCycle 执行一轮完整查询。
func (e *Engine) runCycle(ctx context.Context) {
	groups, invalid := e.groupTargets()
	if len(groups) == 0 && len(invalid) == 0 {
		return
	}

	var tally cycleTally

	// 地区无法识别的目标压根发不出请求，直接登记成故障，让用户看到原因。
	for _, t := range invalid {
		e.updateState(t.Key(), model.Unknown,
			fmt.Errorf("地区 %q 无法识别，这条监控无法查询，请删除后重新添加", t.Locale), true)
		tally.record(false, 1)
	}

	sem := make(chan struct{}, e.concurrency)
	var wg sync.WaitGroup

	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(g storeGroup) {
			// 记账放在最外层 defer 里统一提交。
			//
			// 之前 tally 是在 queryStore 返回之后才写的，一旦 queryStore 内部 panic，
			// 这次门店查询既不算成功也不算失败：全部门店都 panic 时
			// failed 为 0，下面的退避判断走 else 分支把 cycleFailures 清零，
			// 全局退避完全不起作用，而 EventCycleComplete 照常发出，
			// 界面据此认为一切正常。
			ok := false
			problems := 1
			defer func() {
				tally.record(ok, problems)
				wg.Done()
			}()

			// 单个门店的查询出问题不应波及其他门店，更不该拖垮进程。
			defer func() {
				if r := recover(); r != nil {
					err := fmt.Errorf("查询门店 %s 时内部错误已被拦截: %v", g.storeNumber, r)
					// 必须把受影响的目标记成「未知 + 错误原因」。
					//
					// 只发一个告警事件是不够的：这些目标的状态会原封不动地停在
					// 上一轮的取值上，而那个取值很可能正是「无货」。用户于是看到
					// 一个没有任何错误标记的陈旧「无货」—— 这正是本项目要根除的
					// 那种失败方式，出现在专门用来兜底的路径上尤其不能接受。
					e.recordFailure(g, err)
					e.emit(Event{Kind: EventTrouble, At: e.now(), Reason: err.Error()})
				}
			}()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				// 被取消不算故障，也不该计入退避。
				ok, problems = true, 0
				return
			}
			defer func() { <-sem }()

			ok, problems = e.queryStore(ctx, g)
		}(g)
	}

	wg.Wait()

	if ctx.Err() != nil {
		return
	}

	tally.Lock()
	okCount, failedCount, problemCount := tally.ok, tally.failed, tally.problems
	tally.Unlock()

	// 只有一个门店都没查成功，才认为是全局故障，进入退避。
	e.mu.Lock()
	if failedCount > 0 && okCount == 0 {
		e.cycleFailures++
	} else {
		e.cycleFailures = 0
	}
	e.mu.Unlock()

	e.emit(Event{
		Kind:    EventCycleComplete,
		At:      e.now(),
		Healthy: problemCount == 0 && okCount > 0,
	})
}

// queryStore 查询单个门店并更新相关目标的状态。
//
// 返回值：ok 表示这次门店查询是否算成功（用于全局退避判断），
// problems 是本次遇到的异常数量（用于判断整轮是否健康）。
func (e *Engine) queryStore(ctx context.Context, g storeGroup) (ok bool, problems int) {
	result, err := e.client.PickupMessage(ctx, g.region, g.storeNumber, g.parts)
	if ctx.Err() != nil {
		// 停止监控导致的中断不是故障。
		return true, 0
	}

	if err != nil {
		e.recordFailure(g, err)
		return false, len(g.parts)
	}

	unrecognized := 0
	for _, part := range g.parts {
		key := g.region.Locale + "|" + g.storeNumber + "|" + part
		status, found := result.Parts[part]
		switch {
		case !found:
			// 请求成功但响应里没有这个型号，通常意味着零件号已经下架或写错。
			// 这属于「查不到」，绝不能当作「无货」。
			e.updateState(key, model.Unknown,
				fmt.Errorf("%w: 响应中没有型号 %s", apple.ErrUnexpectedSchema, part), true)
			problems++

		case !status.Recognized:
			// Apple 返回了我们不认识的取值，说明接口词表变了。
			// 把原始值带进错误信息，否则排查时无从下手。
			e.updateState(key, model.Unknown,
				fmt.Errorf("%w: 型号 %s 返回了无法识别的 pickupDisplay %q",
					apple.ErrUnexpectedSchema, part, status.PickupDisplay), true)
			problems++
			unrecognized++

		default:
			e.updateState(key, status.Availability, nil, false)
		}
	}

	// 整店的型号全都无法识别，才判定为门店级故障并触发告警与退避。
	// 只有个别型号对不上时不能这么判 —— 那多半是某一个零件号本身的问题，
	// 让它拖着整个门店退避是过度反应。
	if unrecognized == len(g.parts) && len(g.parts) > 0 {
		e.emit(Event{
			Kind: EventTrouble,
			At:   e.now(),
			Reason: fmt.Sprintf("门店 %s 的全部型号都返回了无法识别的取值，Apple 可能已调整接口",
				g.storeNumber),
		})
		return false, problems
	}
	return true, problems
}

// recordFailure 把一次门店级失败登记到该门店下的所有目标上。
func (e *Engine) recordFailure(g storeGroup, err error) {
	for _, part := range g.parts {
		key := g.region.Locale + "|" + g.storeNumber + "|" + part
		e.updateState(key, model.Unknown, err, true)
	}

	// 被拦截是致命故障：整个程序失去作用，必须让用户看见，
	// 而不是像上游那样把它折叠成一屏「无货」。
	if errors.Is(err, apple.ErrBlocked) || errors.Is(err, apple.ErrUnexpectedSchema) {
		e.emit(Event{
			Kind:   EventTrouble,
			At:     e.now(),
			Reason: fmt.Sprintf("门店 %s 查询失败: %v", g.storeNumber, err),
		})
	}
}

// updateState 更新单个目标状态，并在状态变化时发出事件。
func (e *Engine) updateState(key string, availability model.Availability, err error, failed bool) {
	e.mu.Lock()
	state, ok := e.states[key]
	if !ok {
		// 目标可能在本轮查询进行中被用户删掉了，直接忽略。
		e.mu.Unlock()
		return
	}

	previous := state.Availability
	state.Availability = availability
	state.LastChecked = e.now()
	state.LastError = err
	if failed {
		state.ConsecutiveFailures++
	} else {
		state.ConsecutiveFailures = 0
	}
	snapshot := *state
	e.mu.Unlock()

	if previous == availability {
		return
	}

	e.emit(Event{Kind: EventStateChanged, State: snapshot, At: snapshot.LastChecked})

	// 只在「变为有货」的瞬间提醒一次，避免持续有货时每轮都响。
	if availability == model.InStock {
		e.emit(Event{Kind: EventInStock, State: snapshot, At: snapshot.LastChecked})
	}
}

// emit 非阻塞地投递事件。
func (e *Engine) emit(ev Event) {
	select {
	case e.events <- ev:
	default:
		// 订阅方消费不过来时丢弃事件。监控本身的节奏比事件完整性更重要，
		// 状态仍可通过 Snapshot 获取。
	}
}
