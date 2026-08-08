package watcher_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/watcher"
)

// testLocale 必须是 model.Regions 里真实存在的 locale，
// 因为 groupTargets 会用 RegionByLocale 反查地区，查不到的目标会被整条丢掉。
const testLocale = "zh_CN"

const (
	storeA = "R683"
	storeB = "R409"
	partA  = "MG724CH/A"
	partB  = "MG834CH/A"
	partC  = "MG944CH/A"
)

// fakeCall 记录一次 PickupMessage 调用的入参。
type fakeCall struct {
	locale string
	store  string
	parts  []string
}

// fakeFetcher 实现 watcher.Fetcher，完全不碰网络。
//
// 调度引擎的行为（边沿触发、按门店聚合、失败退避、并发安全）与 HTTP 无关，
// 用假实现才能把这些逻辑测得又快又确定。上游把调度、状态和界面刷新全揉在
// 一个 goroutine 里（services/listen.go:119-157），根本无法这样替换。
type fakeFetcher struct {
	mu    sync.Mutex
	calls []fakeCall

	// respond 决定第 n 次调用（n 从 1 开始）的返回值；为 nil 时所有型号都返回有货。
	respond func(n int, store string, parts []string) (*apple.StoreAvailability, error)
}

func (f *fakeFetcher) PickupMessage(ctx context.Context, region model.Region, storeNumber string, parts []string) (*apple.StoreAvailability, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{
		locale: region.Locale,
		store:  storeNumber,
		parts:  append([]string(nil), parts...),
	})
	n := len(f.calls)
	respond := f.respond
	f.mu.Unlock()

	if respond != nil {
		return respond(n, storeNumber, parts)
	}
	return storeResult(storeNumber, parts, model.InStock), nil
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeFetcher) snapshotCalls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

// storeResult 拼一个所有型号都是同一状态的门店结果。
func storeResult(storeNumber string, parts []string, a model.Availability) *apple.StoreAvailability {
	out := &apple.StoreAvailability{
		StoreNumber: storeNumber,
		StoreName:   "测试门店",
		Parts:       make(map[string]apple.PartStatus, len(parts)),
	}
	for _, p := range parts {
		out.Parts[p] = apple.PartStatus{PartNumber: p, Availability: a, Recognized: true}
	}
	return out
}

func target(store, part string) model.Target {
	return model.Target{
		Locale:      testLocale,
		StoreNumber: store,
		StoreTitle:  "上海-" + store,
		PartNumber:  part,
		ProductName: "iPhone 17 " + part,
	}
}

// newEngine 构造一个「跑一轮就停下等很久」的引擎。
//
// 间隔设成 1 小时，loop 会先立刻跑一轮再进入等待，于是整个用例期间有且只有一轮，
// 断言不需要依赖任何 sleep 时长。
func newEngine(f watcher.Fetcher, opts ...watcher.Option) *watcher.Engine {
	base := []watcher.Option{watcher.WithInterval(time.Hour), watcher.WithJitter(0)}
	return watcher.New(f, append(base, opts...)...)
}

// waitCycles 阻塞读事件，直到收到第 n 个 EventCycleComplete，返回期间收到的全部事件。
//
// 用事件而不是 sleep 来判断「一轮跑完了」：EventCycleComplete 是在 wg.Wait 之后发出的，
// 收到它就意味着这一轮所有状态都已写入。
func waitCycles(t *testing.T, e *watcher.Engine, n int, timeout time.Duration) []watcher.Event {
	t.Helper()
	deadline := time.After(timeout)
	var got []watcher.Event
	cycles := 0
	for cycles < n {
		select {
		case ev := <-e.Events():
			got = append(got, ev)
			if ev.Kind == watcher.EventCycleComplete {
				cycles++
			}
		case <-deadline:
			t.Fatalf("等待第 %d 轮查询完成超时，已完成 %d 轮，收到事件 %d 条", n, cycles, len(got))
		}
	}
	return got
}

// waitCalls 轮询等待假 fetcher 被调用够 n 次。
func waitCalls(t *testing.T, f *fakeFetcher, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.callCount() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 %d 次查询超时，实际只有 %d 次", n, f.callCount())
}

// waitTrouble 阻塞读事件，直到收到一条 EventTrouble。
func waitTrouble(t *testing.T, e *watcher.Engine, timeout time.Duration) watcher.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == watcher.EventTrouble {
				return ev
			}
		case <-deadline:
			t.Fatal("等待 EventTrouble 超时")
			return watcher.Event{}
		}
	}
}

// waitStopped 轮询等待引擎报告自己已经停下。
//
// 生命周期复位发生在监控 goroutine 退出的路上，收到事件的那一刻它可能还没走完，
// 所以只能轮询，不能收到事件就立刻断言。
func waitStopped(t *testing.T, e *watcher.Engine, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !e.Running() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("监控循环已经退出，Running() 却一直是 true —— 引擎成了叫不醒的僵尸")
}

// drainEvents 非阻塞地取走通道里已有的事件。
func drainEvents(e *watcher.Engine) []watcher.Event {
	var got []watcher.Event
	for {
		select {
		case ev := <-e.Events():
			got = append(got, ev)
		default:
			return got
		}
	}
}

func countKind(events []watcher.Event, kind watcher.EventKind) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func findState(t *testing.T, e *watcher.Engine, key string) watcher.State {
	t.Helper()
	for _, s := range e.Snapshot() {
		if s.Target.Key() == key {
			return s
		}
	}
	t.Fatalf("快照里没有目标 %s", key)
	return watcher.State{}
}

// TestInStockEventIsEdgeTriggered 验证提醒只在「变为有货」的那一刻发一次。
//
// 持续有货时每轮都响会把用户逼疯，最后的结果是用户关掉提示音，
// 于是真正的到货提醒也一起错过了。
func TestInStockEventIsEdgeTriggered(t *testing.T) {
	f := &fakeFetcher{}
	e := watcher.New(f, watcher.WithInterval(5*time.Millisecond), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	events := waitCycles(t, e, 3, 5*time.Second)
	e.Stop()
	events = append(events, drainEvents(e)...)

	if n := countKind(events, watcher.EventInStock); n != 1 {
		t.Fatalf("收到 %d 条 EventInStock, 期望恰好 1 条（持续有货不应重复提醒）", n)
	}
	if n := countKind(events, watcher.EventStateChanged); n != 1 {
		t.Fatalf("收到 %d 条 EventStateChanged, 期望恰好 1 条（Unknown -> InStock 只变了一次）", n)
	}

	st := findState(t, e, target(storeA, partA).Key())
	if st.Availability != model.InStock {
		t.Errorf("Availability = %v, 期望 InStock", st.Availability)
	}
	if st.LastError != nil {
		t.Errorf("LastError = %v, 期望成功后清空", st.LastError)
	}
	if st.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, 期望 0", st.ConsecutiveFailures)
	}
	if st.LastChecked.IsZero() {
		t.Error("LastChecked 仍为零值")
	}
}

// TestInStockEventFiresAgainAfterGoingOutOfStock 验证货卖光后再补货能再次提醒。
//
// 边沿触发不能做成「一辈子只提醒一次」，否则第二批放货就不会响了。
func TestInStockEventFiresAgainAfterGoingOutOfStock(t *testing.T) {
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			// 有货 -> 无货 -> 有货
			switch n {
			case 1, 3:
				return storeResult(store, parts, model.InStock), nil
			default:
				return storeResult(store, parts, model.OutOfStock), nil
			}
		},
	}
	e := watcher.New(f, watcher.WithInterval(5*time.Millisecond), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	events := waitCycles(t, e, 3, 5*time.Second)
	e.Stop()
	events = append(events, drainEvents(e)...)

	if n := countKind(events, watcher.EventInStock); n < 2 {
		t.Fatalf("收到 %d 条 EventInStock, 期望至少 2 条（补货后应再次提醒）", n)
	}
}

// TestQueryFailureStaysUnknown 是本项目与上游最本质的区别。
//
// 上游查询失败时返回空 map（services/listen.go:226-230），随后把查不到的一律
// 标成无货（:147），于是接口被拦之后满屏都是「无货」。这里必须是 Unknown + LastError。
func TestQueryFailureStaysUnknown(t *testing.T) {
	wantErr := fmt.Errorf("%w: HTTP 541", apple.ErrBlocked)
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			return nil, wantErr
		},
	}
	e := newEngine(f)
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	events := waitCycles(t, e, 1, 5*time.Second)
	e.Stop()

	st := findState(t, e, target(storeA, partA).Key())
	if st.Availability == model.OutOfStock {
		t.Fatal("查询失败被记成了 OutOfStock，这正是上游那屏假『无货』的成因")
	}
	if st.Availability != model.Unknown {
		t.Fatalf("Availability = %v, 期望 Unknown", st.Availability)
	}
	if st.LastError == nil {
		t.Fatal("LastError 为 nil，查询失败必须留下原因")
	}
	if !errors.Is(st.LastError, apple.ErrBlocked) {
		t.Errorf("LastError = %v, 期望保留 ErrBlocked 分类", st.LastError)
	}
	if st.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, 期望 1", st.ConsecutiveFailures)
	}

	// 被拦截是致命故障，必须显式告诉用户，而不是折叠成一屏「无货」。
	if countKind(events, watcher.EventTrouble) == 0 {
		t.Error("被拦截时没有发出 EventTrouble")
	}
}

// TestTargetsGroupedByStore 验证同门店的多个型号一轮只发一次请求。
//
// 不聚合的话，盯 10 个型号就是 10 倍请求量，风控来得只会更快。
func TestTargetsGroupedByStore(t *testing.T) {
	f := &fakeFetcher{}
	e := newEngine(f)
	e.SetTargets([]model.Target{
		target(storeA, partA),
		target(storeA, partB),
		target(storeB, partC),
	})

	e.Start()
	waitCycles(t, e, 1, 5*time.Second)
	e.Stop()

	calls := f.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("一轮发起了 %d 次查询, 期望 2 次（两个门店各一次）: %+v", len(calls), calls)
	}

	byStore := map[string][]string{}
	for _, c := range calls {
		if c.locale != testLocale {
			t.Errorf("查询用的 locale = %q, 期望 %q", c.locale, testLocale)
		}
		if _, dup := byStore[c.store]; dup {
			t.Fatalf("门店 %s 在同一轮里被查询了两次", c.store)
		}
		byStore[c.store] = c.parts
	}

	gotA := byStore[storeA]
	if len(gotA) != 2 || gotA[0] != partA || gotA[1] != partB {
		t.Errorf("门店 %s 的零件号 = %v, 期望 [%s %s]", storeA, gotA, partA, partB)
	}
	if gotB := byStore[storeB]; len(gotB) != 1 || gotB[0] != partC {
		t.Errorf("门店 %s 的零件号 = %v, 期望 [%s]", storeB, gotB, partC)
	}

	// 每个目标都要拿到自己的状态，聚合不能把结果串到别的型号上。
	for _, tg := range e.Targets() {
		if st := findState(t, e, tg.Key()); st.Availability != model.InStock {
			t.Errorf("目标 %s 的状态 = %v, 期望 InStock", tg.Key(), st.Availability)
		}
	}
}

// TestSetTargetsPreservesExistingState 验证增删目标不会把已有结果清零。
//
// 用户在监控过程中加一个型号，不应该让已经查出「有货」的那些目标退回 Unknown。
func TestSetTargetsPreservesExistingState(t *testing.T) {
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			out := storeResult(store, parts, model.OutOfStock)
			out.Parts[partA] = apple.PartStatus{PartNumber: partA, Availability: model.InStock, Recognized: true}
			return out, nil
		},
	}
	kept := target(storeA, partA)
	removed := target(storeA, partB)
	added := target(storeB, partC)

	e := newEngine(f)
	e.SetTargets([]model.Target{kept, removed})

	e.Start()
	waitCycles(t, e, 1, 5*time.Second)
	e.Stop()

	before := findState(t, e, kept.Key())
	if before.Availability != model.InStock {
		t.Fatalf("前置条件不成立: %s 的状态 = %v, 期望 InStock", kept.Key(), before.Availability)
	}
	if st := findState(t, e, removed.Key()); st.Availability != model.OutOfStock {
		t.Fatalf("前置条件不成立: %s 的状态 = %v, 期望 OutOfStock", removed.Key(), st.Availability)
	}

	e.SetTargets([]model.Target{kept, added})

	snapshot := e.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("快照里有 %d 个状态, 期望 2 个: %+v", len(snapshot), snapshot)
	}
	for _, s := range snapshot {
		if s.Target.Key() == removed.Key() {
			t.Fatalf("已删除的目标 %s 仍留在状态表里", removed.Key())
		}
	}

	after := findState(t, e, kept.Key())
	if after.Availability != model.InStock {
		t.Errorf("保留目标的状态 = %v, 期望仍是 InStock", after.Availability)
	}
	if !after.LastChecked.Equal(before.LastChecked) {
		t.Errorf("保留目标的 LastChecked 被重置了: %v -> %v", before.LastChecked, after.LastChecked)
	}

	newState := findState(t, e, added.Key())
	if newState.Availability != model.Unknown {
		t.Errorf("新增目标的状态 = %v, 期望 Unknown", newState.Availability)
	}
	if !newState.LastChecked.IsZero() {
		t.Errorf("新增目标的 LastChecked = %v, 期望零值", newState.LastChecked)
	}
}

// TestStopEndsQueries 验证 Stop 之后确实不再发请求。
//
// 上游那个 for {} + time.Sleep(500ms) 的循环没有任何退出条件
// （services/listen.go:122-156），点了停止也停不下来。
func TestStopEndsQueries(t *testing.T) {
	f := &fakeFetcher{}
	e := watcher.New(f, watcher.WithInterval(5*time.Millisecond), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})

	if e.Running() {
		t.Error("尚未 Start，Running() 却是 true")
	}

	e.Start()
	e.Start() // 重复 Start 必须无副作用，否则会跑出两条循环、请求量翻倍
	if !e.Running() {
		t.Error("Start 之后 Running() 仍是 false")
	}
	waitCycles(t, e, 2, 5*time.Second)

	e.Stop()
	if e.Running() {
		t.Error("Stop 之后 Running() 仍是 true")
	}
	e.Stop() // 重复 Stop 不能死锁

	// Stop 会等待循环退出，此后计数不应再增长。
	settled := f.callCount()
	time.Sleep(50 * time.Millisecond)
	if got := f.callCount(); got != settled {
		t.Fatalf("Stop 之后又发起了 %d 次查询（%d -> %d）", got-settled, settled, got)
	}

	// 停掉之后还得能重新启动。
	drainEvents(e)
	e.Start()
	waitCycles(t, e, 1, 5*time.Second)
	e.Stop()
	if got := f.callCount(); got <= settled {
		t.Fatalf("重新 Start 之后没有新的查询: %d -> %d", settled, got)
	}
}

// TestConcurrentSetTargetsAndSnapshot 是针对上游那类崩溃的回归测试。
//
// 上游的 items map 在 UI 线程和轮询 goroutine 之间无锁共享读写
// （main.go:125 与 services/listen.go:112-117），会触发
// fatal error: concurrent map read and map write —— 这种 fatal 无法被 recover，
// 进程直接没。配合 go test -race 跑这个用例就能守住。
func TestConcurrentSetTargetsAndSnapshot(t *testing.T) {
	f := &fakeFetcher{}
	e := watcher.New(f, watcher.WithInterval(time.Millisecond), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})
	e.Start()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		sets := [][]model.Target{
			{target(storeA, partA)},
			{target(storeA, partA), target(storeA, partB)},
			{target(storeB, partC)},
			{target(storeA, partA), target(storeB, partC)},
		}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			e.SetTargets(sets[i%len(sets)])
		}
	}()

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = e.Snapshot()
				_ = e.Targets()
				_ = e.Running()
			}
		}()
	}

	// 同时把事件消费掉，模拟界面订阅方。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-e.Events():
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	e.Stop()

	if f.callCount() == 0 {
		t.Fatal("整个并发过程中一次查询都没发生，用例没有真正跑起来")
	}
}

// TestEventsChannelFullDoesNotBlockEngine 验证没人消费事件时监控照常运转。
//
// 事件通道缓冲 256 条，写满即丢弃。要是这里改成阻塞写，界面一卡就会把整个
// 监控拖停——那比丢几条事件严重得多，状态本来就可以随时从 Snapshot 取。
func TestEventsChannelFullDoesNotBlockEngine(t *testing.T) {
	const targetCount = 200

	targets := make([]model.Target, 0, targetCount)
	for i := 0; i < targetCount; i++ {
		targets = append(targets, target(storeA, fmt.Sprintf("MG%03dCH/A", i)))
	}

	f := &fakeFetcher{}
	e := watcher.New(f, watcher.WithInterval(5*time.Millisecond), watcher.WithJitter(0))
	e.SetTargets(targets)

	// 全程不消费事件：第一轮就会产生 200 条 StateChanged + 200 条 InStock，
	// 远超 256 的缓冲，之后的每一次 emit 都会走丢弃分支。
	e.Start()
	waitCalls(t, f, 3, 5*time.Second)
	e.Stop()

	if got := len(e.Events()); got != 256 {
		t.Fatalf("事件通道里有 %d 条事件, 期望被填满到 256 条", got)
	}

	snapshot := e.Snapshot()
	if len(snapshot) != targetCount {
		t.Fatalf("快照里有 %d 个状态, 期望 %d 个", len(snapshot), targetCount)
	}
	for _, s := range snapshot {
		if s.Availability != model.InStock {
			t.Fatalf("目标 %s 的状态 = %v, 期望 InStock（事件丢弃不能影响状态本身）",
				s.Target.Key(), s.Availability)
		}
	}
}

// TestFetcherPanicDoesNotKillEngine 是本项目核心目标的回归测试。
//
// 上游在后台 goroutine 里直接 panic（services/listen.go:282 的 AlertMp3、
// :302 的 Bark 推送），Go 里任意 goroutine 的 panic 未被捕获就会终止进程，
// 这就是「一推送就闪退」的成因。这里让 fetcher 返回 (nil, nil)，
// queryStore 解引用空指针必然 panic，引擎必须把它拦下来并转成 EventTrouble。
func TestFetcherPanicDoesNotKillEngine(t *testing.T) {
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			return nil, nil
		},
	}
	e := newEngine(f)
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	events := waitCycles(t, e, 1, 5*time.Second)

	// 门店级 panic 是被单店 recover 兜住的局部故障，监控循环本身必须还活着；
	// 复位生命周期只能是循环整个塌掉时的行为，不能被这种局部故障误触发。
	if !e.Running() {
		t.Error("单个门店的 panic 之后 Running() 变成了 false，监控循环被误停了")
	}
	e.Stop()

	var trouble *watcher.Event
	for i := range events {
		if events[i].Kind == watcher.EventTrouble {
			trouble = &events[i]
			break
		}
	}
	if trouble == nil {
		t.Fatal("goroutine 内的 panic 没有转成 EventTrouble")
	}
	if !strings.Contains(trouble.Reason, storeA) {
		t.Errorf("EventTrouble.Reason = %q, 期望带上出问题的门店号 %s", trouble.Reason, storeA)
	}

	// panic 被拦下之后引擎还得是活的：状态保持 Unknown，本轮照常收尾。
	if st := findState(t, e, target(storeA, partA).Key()); st.Availability != model.Unknown {
		t.Errorf("Availability = %v, 期望 Unknown", st.Availability)
	}
}

// TestMissingPartInResponseIsUnknown 验证「请求了但响应里没有」不会被当成无货。
func TestMissingPartInResponseIsUnknown(t *testing.T) {
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			// 只回 partA，故意漏掉 partB，模拟零件号下架或写错。
			return storeResult(store, []string{partA}, model.InStock), nil
		},
	}
	e := newEngine(f)
	e.SetTargets([]model.Target{target(storeA, partA), target(storeA, partB)})

	e.Start()
	waitCycles(t, e, 1, 5*time.Second)
	e.Stop()

	if st := findState(t, e, target(storeA, partA).Key()); st.Availability != model.InStock {
		t.Errorf("%s 的状态 = %v, 期望 InStock", partA, st.Availability)
	}

	missing := findState(t, e, target(storeA, partB).Key())
	if missing.Availability == model.OutOfStock {
		t.Fatal("响应中缺失的型号被当成了 OutOfStock")
	}
	if missing.Availability != model.Unknown {
		t.Fatalf("%s 的状态 = %v, 期望 Unknown", partB, missing.Availability)
	}
	if !errors.Is(missing.LastError, apple.ErrUnexpectedSchema) {
		t.Errorf("LastError = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", missing.LastError)
	}
	if missing.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, 期望 1", missing.ConsecutiveFailures)
	}
}

// TestUnknownLocaleTargetIsSkipped 验证 locale 非法的目标只是被跳过，不会拖垮整轮。
//
// 配置文件是可以被手工编辑的，也可能是旧版本留下的；地区表里查不到的 locale
// 必须安全降级，而不是让这一轮直接崩掉。
func TestUnknownLocaleTargetIsSkipped(t *testing.T) {
	bad := model.Target{
		Locale:      "xx_YY",
		StoreNumber: storeB,
		StoreTitle:  "不存在的地区",
		PartNumber:  partC,
		ProductName: "未知机型",
	}
	good := target(storeA, partA)

	f := &fakeFetcher{}
	e := newEngine(f)
	e.SetTargets([]model.Target{bad, good})

	e.Start()
	waitCycles(t, e, 1, 5*time.Second)
	e.Stop()

	calls := f.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("发起了 %d 次查询, 期望 1 次（非法 locale 的目标应被跳过）: %+v", len(calls), calls)
	}
	if calls[0].store != storeA {
		t.Errorf("查询的门店 = %q, 期望 %q", calls[0].store, storeA)
	}
	if st := findState(t, e, bad.Key()); st.Availability != model.Unknown {
		t.Errorf("非法 locale 目标的状态 = %v, 期望 Unknown", st.Availability)
	}
}

// TestSnapshotIsSortedAndDetached 验证快照的顺序稳定且与内部状态解耦。
//
// 排序键是门店名与型号名（不是门店号），否则界面每轮刷新时行序都可能跳动。
func TestSnapshotIsSortedAndDetached(t *testing.T) {
	beijing := model.Target{
		Locale: testLocale, StoreNumber: "R448", StoreTitle: "北京-三里屯",
		PartNumber: partA, ProductName: "iPhone 17 128GB",
	}
	shanghai512 := model.Target{
		Locale: testLocale, StoreNumber: storeA, StoreTitle: "上海-环球港",
		PartNumber: partB, ProductName: "iPhone 17 512GB",
	}
	shanghai256 := model.Target{
		Locale: testLocale, StoreNumber: storeA, StoreTitle: "上海-环球港",
		PartNumber: partC, ProductName: "iPhone 17 256GB",
	}

	f := &fakeFetcher{}
	e := newEngine(f)
	e.SetTargets([]model.Target{beijing, shanghai512, shanghai256})

	got := e.Snapshot()
	if len(got) != 3 {
		t.Fatalf("快照里有 %d 个状态, 期望 3 个", len(got))
	}
	want := []string{shanghai256.Key(), shanghai512.Key(), beijing.Key()}
	for i, key := range want {
		if got[i].Target.Key() != key {
			t.Fatalf("第 %d 个状态是 %s, 期望 %s（应按门店名、型号名排序）", i, got[i].Target.Key(), key)
		}
	}

	// 改动快照不能影响引擎内部状态，否则界面层一改就污染监控数据。
	got[0].Availability = model.InStock
	if again := e.Snapshot(); again[0].Availability != model.Unknown {
		t.Error("修改快照影响到了引擎内部状态")
	}
}

// TestClearingTargetsStopsQueries 验证用户把目标全删光之后循环只是空转。
//
// 空转的一轮不会发出 EventCycleComplete（runCycle 在没有分组时直接返回），
// 所以这里用调用计数而不是事件来断言。
func TestClearingTargetsStopsQueries(t *testing.T) {
	f := &fakeFetcher{}
	e := watcher.New(f, watcher.WithInterval(2*time.Millisecond), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	waitCycles(t, e, 1, 5*time.Second)

	e.SetTargets(nil)
	// 清空的瞬间可能正有一轮在飞，先让它落地再取基准值。
	time.Sleep(20 * time.Millisecond)
	settled := f.callCount()

	time.Sleep(50 * time.Millisecond)
	if got := f.callCount(); got != settled {
		t.Errorf("目标已清空却又发起了 %d 次查询（%d -> %d）", got-settled, settled, got)
	}
	if len(e.Snapshot()) != 0 {
		t.Errorf("目标已清空，快照里却还有 %d 个状态", len(e.Snapshot()))
	}

	e.Stop()
}

// TestStartThenImmediateStop 覆盖「没有目标就点开始再点停止」这条最普通的路径。
//
// 它专门不与监控 goroutine 做任何通信：有目标时 waitCycles 从 Events() 收事件会
// 建立 happens-before 边，把 Start/Stop 之间的竞争掩盖掉。历史上这里的 done 通道
// 是在 goroutine 里通过 e 字段访问的，与 Stop 清空该字段构成数据竞争，
// 还会 close(nil) 把进程带走；现在 done 在 Start 里就捕获成局部变量，
// 配合 go test -race 跑这个用例可以守住。
func TestStartThenImmediateStop(t *testing.T) {
	f := &fakeFetcher{}
	e := watcher.New(f, watcher.WithInterval(time.Millisecond), watcher.WithJitter(0))

	e.Start()
	e.Stop()

	if e.Running() {
		t.Error("Stop 之后 Running() 仍是 true")
	}
	if n := f.callCount(); n != 0 {
		t.Errorf("没有目标却发起了 %d 次查询", n)
	}
}

// missingAllParts 返回一个结构完全合法、却一个请求的型号都没命中的响应，
// 用来模拟 Apple 改了 partsAvailability 的键格式。
func missingAllParts(store string, _ []string) (*apple.StoreAvailability, error) {
	return storeResult(store, []string{"MG000CH/A"}, model.InStock), nil
}

// TestAllPartsMissingIsStoreLevelFailure 验证「请求的型号一个都没在响应里出现」
// 必须按门店级失败处理。
//
// 之前只有「取值无法识别」的型号才计数，型号整个缺失时虽然记成了 Unknown，
// 门店却仍算查询成功：不发 EventTrouble、本轮也不算失败。于是 Apple 一改键格式，
// 所有型号统统对不上，程序反而认为一切正常，继续按原频率去请求一个已经失效的结构，
// 用户只能对着一屏「未知」干等，什么提示都没有。
func TestAllPartsMissingIsStoreLevelFailure(t *testing.T) {
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			return missingAllParts(store, parts)
		},
	}
	e := newEngine(f)
	e.SetTargets([]model.Target{target(storeA, partA), target(storeA, partB)})

	e.Start()
	events := waitCycles(t, e, 1, 5*time.Second)
	e.Stop()
	events = append(events, drainEvents(e)...)

	var trouble *watcher.Event
	for i := range events {
		if events[i].Kind == watcher.EventTrouble {
			trouble = &events[i]
			break
		}
	}
	if trouble == nil {
		t.Fatal("整店型号全部缺失却没有发出 EventTrouble，用户看不到接口已经对不上了")
	}
	if !strings.Contains(trouble.Reason, storeA) {
		t.Errorf("EventTrouble.Reason = %q, 期望带上出问题的门店号 %s", trouble.Reason, storeA)
	}

	for _, ev := range events {
		if ev.Kind == watcher.EventCycleComplete && ev.Healthy {
			t.Error("一个型号都没拿到结果，EventCycleComplete 却报告本轮健康")
		}
	}

	for _, part := range []string{partA, partB} {
		st := findState(t, e, target(storeA, part).Key())
		if st.Availability != model.Unknown {
			t.Errorf("%s 的状态 = %v, 期望 Unknown", part, st.Availability)
		}
		if !errors.Is(st.LastError, apple.ErrUnexpectedSchema) {
			t.Errorf("%s 的 LastError = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", part, st.LastError)
		}
	}
}

// TestAllPartsMissingTriggersGlobalBackoff 验证整店型号全缺失会计入全局失败计数。
//
// cycleFailures 不导出，只能从它唯一的外部表现去验：整轮全败会把下一轮的间隔翻倍。
// 之前这种失败被当成成功，计数每轮都被清零，接口失效时退避形同虚设 —— 程序会以
// 完全没变的频率继续猛冲，只会让风控来得更快。
func TestAllPartsMissingTriggersGlobalBackoff(t *testing.T) {
	const interval = 100 * time.Millisecond

	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			return missingAllParts(store, parts)
		},
	}
	e := watcher.New(f, watcher.WithInterval(interval), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	waitCycles(t, e, 1, 5*time.Second)
	begin := time.Now()
	waitCycles(t, e, 1, 5*time.Second)
	elapsed := time.Since(begin)
	e.Stop()

	// 退避生效时第二轮要等两倍间隔。定时器只会晚到不会早到，机器再慢也只会
	// 让 elapsed 更大，所以这个下界不会误报。
	if elapsed < interval+interval/2 {
		t.Fatalf("第一轮全军覆没后第二轮只等了 %v（基础间隔 %v），说明这一轮被算成了成功、没有进入全局退避",
			elapsed, interval)
	}
}

// TestPartialMissingPartDoesNotAlarmStore 守住上面那条修复的边界。
//
// 只有个别型号缺失时仍按较宽松的策略处理：那多半是某一个零件号自己下架或写错，
// 让它拖着整个门店告警并退避是过度反应，久了用户就会开始无视告警。
func TestPartialMissingPartDoesNotAlarmStore(t *testing.T) {
	f := &fakeFetcher{
		respond: func(n int, store string, parts []string) (*apple.StoreAvailability, error) {
			// 只回 partA，漏掉 partB。
			return storeResult(store, []string{partA}, model.InStock), nil
		},
	}
	e := newEngine(f)
	e.SetTargets([]model.Target{target(storeA, partA), target(storeA, partB)})

	e.Start()
	events := waitCycles(t, e, 1, 5*time.Second)
	e.Stop()
	events = append(events, drainEvents(e)...)

	if n := countKind(events, watcher.EventTrouble); n != 0 {
		t.Fatalf("只有一个型号缺失就发了 %d 条 EventTrouble，对单个零件号的问题反应过度", n)
	}
}

// concurrentFetcher 记录同一时刻在飞的查询数峰值。
//
// 只盯一个门店时，一条监控循环任何时刻最多只有一次查询在飞（runCycle 会等本轮
// 所有门店 goroutine 结束才返回）。峰值一旦超过 1，就只能是同时存在两条循环。
type concurrentFetcher struct {
	delay time.Duration

	mu       sync.Mutex
	inFlight int
	peak     int
	calls    int
}

func (f *concurrentFetcher) PickupMessage(ctx context.Context, region model.Region, storeNumber string, parts []string) (*apple.StoreAvailability, error) {
	f.mu.Lock()
	f.calls++
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.mu.Unlock()

	// 拖住一会儿，让「旧循环还在飞、新循环已经起来」的重叠窗口能被观测到。
	time.Sleep(f.delay)

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()

	return storeResult(storeNumber, parts, model.InStock), nil
}

func (f *concurrentFetcher) stats() (calls, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.peak
}

// TestConcurrentStopAndStartRunsSingleLoop 验证并发的 Stop 与 Start 不会跑出两套循环。
//
// Stop 必须先释放 runMu 才能去等旧循环退出。之前它在释放锁之前就把 running 置成了
// false，于是这段等待期间闯进来的 Start 会看到「没在运行」而再拉起一条循环：
// 两条循环同时查询，请求量翻倍（风控只会更严），还会交替往同一份状态里写，
// 界面上的状态和提醒都会开始跳。
func TestConcurrentStopAndStartRunsSingleLoop(t *testing.T) {
	f := &concurrentFetcher{delay: 5 * time.Millisecond}
	e := watcher.New(f, watcher.WithInterval(time.Millisecond), watcher.WithJitter(0))
	e.SetTargets([]model.Target{target(storeA, partA)})

	for i := 0; i < 20; i++ {
		e.Start()

		// 等循环真的开始查询，否则 Stop 撞上的是一条还没干活的循环，窗口根本没打开。
		before, _ := f.stats()
		waitConcurrentCalls(t, f, before+1, 5*time.Second)

		// 让两个调用尽量同时进入，才有机会命中 Stop 释放锁之后的那一小段窗口。
		fire := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-fire
			e.Stop()
		}()
		go func() {
			defer wg.Done()
			<-fire
			e.Start()
		}()
		close(fire)
		wg.Wait()

		e.Stop()
		drainEvents(e)

		if _, peak := f.stats(); peak > 1 {
			t.Fatalf("第 %d 次并发 Stop/Start 后出现了 %d 个同时在飞的查询，"+
				"单门店单循环最多只该有 1 个，说明同时跑起了两套循环", i, peak)
		}
	}

	if calls, _ := f.stats(); calls == 0 {
		t.Fatal("整个用例一次查询都没发生，没有真正跑起来")
	}
}

// waitConcurrentCalls 轮询等待 concurrentFetcher 被调用够 n 次。
func waitConcurrentCalls(t *testing.T, f *concurrentFetcher, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if calls, _ := f.stats(); calls >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	calls, _ := f.stats()
	t.Fatalf("等待 %d 次查询超时，实际只有 %d 次", n, calls)
}

// TestLoopPanicResetsLifecycle 验证监控循环因 panic 退出后引擎不会变成僵尸。
//
// 之前主 goroutine 的 recover 只发一条 EventTrouble 就算完，生命周期状态原样挂着：
// Running() 一直报告「在运行」，Start() 又因此变成空操作，监控其实早就死了，
// 界面却显示得好好的，用户点停止再点开始也救不回来，只能重启程序。
//
// panic 从时间源注入而不是从 fetcher 注入：fetcher 的 panic 由每个门店自己的
// recover 兜住（见 TestFetcherPanicDoesNotKillEngine），循环本来就该继续跑。
// 要模拟「循环整个塌掉」，panic 必须发生在门店 goroutine 之外 —— 第 2 次时间源
// 调用恰好落在 runCycle 收尾发 EventCycleComplete 那一处，那里不持有任何锁，
// panic 能干净地一路抛到 loop 外面。
func TestLoopPanicResetsLifecycle(t *testing.T) {
	var clockCalls int64
	clock := func() time.Time {
		// 只炸一次：引擎必须能从这次 panic 里彻底恢复，第二次 Start 要真的又跑起来。
		if atomic.AddInt64(&clockCalls, 1) == 2 {
			panic("时间源炸了")
		}
		return time.Now()
	}

	f := &fakeFetcher{}
	e := newEngine(f, watcher.WithClock(clock))
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	trouble := waitTrouble(t, e, 5*time.Second)
	if !strings.Contains(trouble.Reason, "时间源炸了") {
		t.Errorf("EventTrouble.Reason = %q, 期望带上原始 panic 的内容", trouble.Reason)
	}

	waitStopped(t, e, 5*time.Second)

	// 复位到位与否的真正验收标准：还能不能重新启动。
	before := f.callCount()
	e.Start()
	waitCalls(t, f, before+1, 5*time.Second)
	if !e.Running() {
		t.Error("重新 Start 之后 Running() 仍是 false")
	}

	e.Stop()
	if e.Running() {
		t.Error("Stop 之后 Running() 仍是 true")
	}
}

// TestNilOptionsFallBackToDefaults 验证 nil 的时间源 / 随机源不会把引擎炸掉。
//
// 这两个 Option 之前照单全收 nil：WithClock(nil) 会让第一次写状态时 e.now() 就崩，
// 而崩在 recover 处理路径上还会二次 panic 直接终止进程；WithRand(nil) 更隐蔽，
// 要到第一轮跑完、算下一轮抖动时才炸，此前一切看起来都正常。
func TestNilOptionsFallBackToDefaults(t *testing.T) {
	f := &fakeFetcher{}
	// 抖动必须大于 0，否则 nextDelay 根本不会碰随机源，WithRand(nil) 也就炸不出来。
	e := watcher.New(f,
		watcher.WithInterval(5*time.Millisecond),
		watcher.WithJitter(0.5),
		watcher.WithClock(nil),
		watcher.WithRand(nil),
	)
	e.SetTargets([]model.Target{target(storeA, partA)})

	e.Start()
	// 必须等到第二轮：随机源是在第一轮结束之后算下一轮间隔时才用到的。
	// 抖动大于 0 时下一轮至少要等 1 秒（nextDelay 的下限），这里的超时留足余量。
	events := waitCycles(t, e, 2, 10*time.Second)
	e.Stop()

	if n := countKind(events, watcher.EventTrouble); n != 0 {
		t.Fatalf("收到 %d 条 EventTrouble：nil 选项应当被忽略并退回默认值，而不是把循环炸掉", n)
	}

	st := findState(t, e, target(storeA, partA).Key())
	if st.LastChecked.IsZero() {
		t.Error("LastChecked 仍是零值，说明 nil 时间源没有退回 time.Now")
	}
	if st.Availability != model.InStock {
		t.Errorf("Availability = %v, 期望 InStock", st.Availability)
	}
}
