package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/catalog"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/config"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/watcher"
)

// 这些测试全部单线程运行：New 本身不起 goroutine（起 goroutine 的是 Run），
// 引擎跑完一轮后先停掉，再由测试 goroutine 逐条把事件喂给 handleEvent。
//
// 之所以不让 UI 的事件消费 goroutine 并发跑，是因为 Fyne 的测试驱动会把
// fyne.Do 直接内联执行（test/driver.go:53），而真实驱动是投递到主线程队列。
// 并发跑会让 -race 报出一堆生产环境根本不存在的竞态，测出来的东西是假的。

// fakeFetcher 是可编程的假查询源，用来精确构造各种响应。
type fakeFetcher struct {
	mu    sync.Mutex
	calls int
	fn    func(storeNumber string, parts []string) (*apple.StoreAvailability, error)
}

func (f *fakeFetcher) PickupMessage(_ context.Context, _ model.Region, storeNumber string, parts []string) (*apple.StoreAvailability, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(storeNumber, parts)
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func target(store, storeTitle, part, product string) model.Target {
	return model.Target{
		Locale:      "zh_CN",
		StoreNumber: store,
		StoreTitle:  storeTitle,
		PartNumber:  part,
		ProductName: product,
	}
}

// newTestUI 组装一套完全离线的界面，返回界面、引擎与假查询源。
func newTestUI(t *testing.T, fetcher *fakeFetcher, targets []model.Target) (*UI, *watcher.Engine) {
	t.Helper()

	engine := watcher.New(fetcher,
		watcher.WithInterval(50*time.Millisecond),
		watcher.WithJitter(0),
	)
	engine.SetTargets(targets)

	settings := config.Default()
	settings.Targets = targets
	settings.SoundEnabled = false
	settings.OpenBagOnHit = false

	u, err := New(Deps{
		App:      test.NewApp(),
		Catalog:  catalog.New(),
		Engine:   engine,
		Store:    config.NewStoreAt(filepath.Join(t.TempDir(), "settings.json")),
		Settings: settings,
	})
	if err != nil {
		t.Fatalf("构造界面失败: %v", err)
	}
	t.Cleanup(func() {
		engine.Stop()
		u.closed.Store(true)
	})
	return u, engine
}

// notifyRecorder 顶替默认的「另起 goroutine 发提醒」，把提醒就地记下来。
//
// 换掉它是为了守住本文件顶部那条单线程约定：真实实现会起一个后台 goroutine，
// 那个 goroutine 经 onMain 去碰 fyne.App，而测试驱动把 fyne.Do 内联执行，
// 于是 -race 会报出生产环境根本不存在的假竞态。记录本身不加锁，因为整个测试
// 只有一条线程在跑。
type notifyRecorder struct {
	states []watcher.State
}

// recordNotifications 接管界面的提醒发送，返回记录器。
func recordNotifications(u *UI) *notifyRecorder {
	rec := &notifyRecorder{}
	u.dispatchNotify = func(state watcher.State) {
		rec.states = append(rec.states, state)
	}
	return rec
}

// products 返回收到提醒的型号名，按发生顺序排列。
func (r *notifyRecorder) products() []string {
	out := make([]string, 0, len(r.states))
	for _, s := range r.states {
		out = append(out, s.Target.ProductName)
	}
	return out
}

// runOneCycle 让引擎跑到至少完成一轮查询后停下，然后把积压的事件喂给界面，
// 返回最后一个 EventCycleComplete 报告的健康状态。
func runOneCycle(t *testing.T, u *UI, engine *watcher.Engine, fetcher *fakeFetcher) bool {
	t.Helper()
	return runCycleDropping(t, u, engine, fetcher, nil)
}

// dropAllButCycleComplete 模拟引擎事件通道被写满的极端情形：除了「一轮结束」
// 之外的事件全部丢掉，界面能拿到的只有「该刷新了」这一个信号。
//
// 这不是杜撰的场景。watcher.Engine.emit 在通道满时直接丢弃事件，而首轮大量
// 目标同时有货、或界面卡顿导致事件堆积，都会真的把 EventInStock 冲掉。
func dropAllButCycleComplete(ev watcher.Event) bool {
	return ev.Kind != watcher.EventCycleComplete
}

// runCycleDropping 与 runOneCycle 相同，但 drop 返回 true 的事件不会喂给界面。
func runCycleDropping(t *testing.T, u *UI, engine *watcher.Engine, fetcher *fakeFetcher,
	drop func(watcher.Event) bool) bool {
	t.Helper()

	before := fetcher.callCount()
	engine.Start()
	deadline := time.Now().Add(5 * time.Second)
	for fetcher.callCount() == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// 再宽限一点，让本轮的状态更新和 EventCycleComplete 都落地。
	time.Sleep(150 * time.Millisecond)
	engine.Stop()

	healthy := false
	events := engine.Events()
	for {
		select {
		case ev := <-events:
			if ev.Kind == watcher.EventCycleComplete {
				healthy = ev.Healthy
			}
			if drop != nil && drop(ev) {
				continue
			}
			u.handleEvent(ev)
		default:
			// 事件通道排空后，再按引擎快照对齐一次界面。
			u.refreshRows()
			return healthy
		}
	}
}

// findRow 按型号名找出界面上的那一行状态。
func findRow(t *testing.T, u *UI, productName string) watcher.State {
	t.Helper()
	for _, row := range u.rows {
		if row.Target.ProductName == productName {
			return row
		}
	}
	t.Fatalf("界面上找不到型号 %q，现有行: %+v", productName, u.rows)
	return watcher.State{}
}

// TestQueryFailureRendersAsErrorNotOutOfStock 守卫本项目最核心的不变量。
//
// 上游的致命缺陷是把查询失败折叠成「无货」（services/listen.go:226-230 出错
// 返回空 map，:147 一律标成 StatusOutStock），用户因此对着一个早已失效的程序
// 干等。这个测试从界面层确认：失败就是失败，绝不能显示成无货。
func TestQueryFailureRendersAsErrorNotOutOfStock(t *testing.T) {
	fetcher := &fakeFetcher{fn: func(string, []string) (*apple.StoreAvailability, error) {
		return nil, fmt.Errorf("%w: HTTP 541", apple.ErrBlocked)
	}}
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)
	runOneCycle(t, u, engine, fetcher)

	row := findRow(t, u, "iPhone 17 512GB 黑色")
	if row.Availability == model.OutOfStock {
		t.Fatal("查询失败被显示成了「无货」，这正是上游那个致命缺陷")
	}
	if row.Availability != model.Unknown {
		t.Errorf("期望状态为未知，实际为 %v", row.Availability)
	}
	if row.LastError == nil {
		t.Fatal("查询失败但 LastError 为空，界面无法说明原因")
	}

	text, importance := statusAppearance(row)
	if !strings.Contains(text, "未知") {
		t.Errorf("展示文本应当体现「未知」，实际为 %q", text)
	}
	if importance != widget.DangerImportance {
		t.Errorf("查询失败应当用错误色突出显示，实际 importance = %v", importance)
	}
}

// TestTroubleBarAppearsOnBlockedAndClearsOnRecovery 验证告警条的出现与消失。
//
// 接口被拦截时必须有一条持续可见的提示告诉用户「现在的状态不可信」，
// 恢复正常后又要能自己收起来，否则用户会一直不敢相信界面。
func TestTroubleBarAppearsOnBlockedAndClearsOnRecovery(t *testing.T) {
	var blocked = true
	var mu sync.Mutex

	fetcher := &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		mu.Lock()
		isBlocked := blocked
		mu.Unlock()
		if isBlocked {
			return nil, fmt.Errorf("%w: HTTP 541", apple.ErrBlocked)
		}
		return &apple.StoreAvailability{
			StoreNumber: store,
			StoreName:   "环球港",
			Parts: map[string]apple.PartStatus{
				parts[0]: {PartNumber: parts[0], Availability: model.OutOfStock, PickupDisplay: "unavailable", Recognized: true},
			},
		}, nil
	}}
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)

	if healthy := runOneCycle(t, u, engine, fetcher); healthy {
		t.Error("整轮被拦截却报告为健康")
	}
	if !u.troubleBar.Visible() {
		t.Fatal("接口被拦截时告警条没有显示，用户会以为「无货」是真实结果")
	}

	mu.Lock()
	blocked = false
	mu.Unlock()

	if healthy := runOneCycle(t, u, engine, fetcher); !healthy {
		t.Error("查询已恢复，却没有报告为健康")
	}
	if u.troubleBar.Visible() {
		t.Error("查询已恢复正常，告警条却没有收起")
	}
	if row := findRow(t, u, "iPhone 17 512GB 黑色"); row.Availability != model.OutOfStock {
		t.Errorf("恢复后应当显示真实的无货状态，实际为 %v", row.Availability)
	}
}

// TestSummaryCountsFailuresSeparately 验证汇总行会把「查询失败」单独点出来。
func TestSummaryCountsFailuresSeparately(t *testing.T) {
	fetcher := &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		// R683 正常作答，R390 整店失败，构造出混合场景。
		if store == "R390" {
			return nil, fmt.Errorf("%w: HTTP 541", apple.ErrBlocked)
		}
		out := &apple.StoreAvailability{StoreNumber: store, StoreName: "环球港", Parts: map[string]apple.PartStatus{}}
		for i, p := range parts {
			availability := model.OutOfStock
			if i == 0 {
				availability = model.InStock
			}
			out.Parts[p] = apple.PartStatus{PartNumber: p, Availability: availability, Recognized: true}
		}
		return out, nil
	}}
	targets := []model.Target{
		target("R683", "上海-环球港", "MG724CH/A", "甲"),
		target("R683", "上海-环球港", "MG0A4CH/A", "乙"),
		target("R390", "上海-香港广场", "MG724CH/A", "丙"),
	}

	u, engine := newTestUI(t, fetcher, targets)
	runOneCycle(t, u, engine, fetcher)

	summary := u.summaryLabel.Text
	if !strings.Contains(summary, "监控 3 项") {
		t.Errorf("汇总行应当反映 3 项监控，实际为 %q", summary)
	}
	if !strings.Contains(summary, "查询失败") {
		t.Errorf("存在失败项时汇总行必须明确点出来，实际为 %q", summary)
	}
}

// TestOneRequestPerStore 确认同一门店的多个型号合并成一次请求。
//
// 这既是效率问题，也是风控问题：每个型号单独发一次请求会让出站请求量翻好几倍。
func TestOneRequestPerStore(t *testing.T) {
	var gotParts [][]string
	var mu sync.Mutex

	fetcher := &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		mu.Lock()
		gotParts = append(gotParts, append([]string(nil), parts...))
		mu.Unlock()
		out := &apple.StoreAvailability{StoreNumber: store, Parts: map[string]apple.PartStatus{}}
		for _, p := range parts {
			out.Parts[p] = apple.PartStatus{PartNumber: p, Availability: model.OutOfStock, Recognized: true}
		}
		return out, nil
	}}
	targets := []model.Target{
		target("R683", "上海-环球港", "MG724CH/A", "甲"),
		target("R683", "上海-环球港", "MG0A4CH/A", "乙"),
		target("R683", "上海-环球港", "MG364CH/A", "丙"),
	}

	u, engine := newTestUI(t, fetcher, targets)
	runOneCycle(t, u, engine, fetcher)

	mu.Lock()
	defer mu.Unlock()
	if len(gotParts) == 0 {
		t.Fatal("一次请求都没发出")
	}
	if n := len(gotParts[0]); n != 3 {
		t.Errorf("同一门店的 3 个型号应当合并成一次请求，实际这次请求只带了 %d 个零件号", n)
	}
	if len(u.rows) != 3 {
		t.Errorf("界面应当有 3 行，实际 %d 行", len(u.rows))
	}
}

// TestNotYetQueriedIsDistinctFromOutOfStock 确认「还没轮到」不会被当成无货。
func TestNotYetQueriedIsDistinctFromOutOfStock(t *testing.T) {
	fetcher := &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		return nil, fmt.Errorf("不应当被调用")
	}}
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	// 只构造界面，完全不启动引擎。
	u, _ := newTestUI(t, fetcher, targets)

	row := findRow(t, u, "iPhone 17 512GB 黑色")
	if row.Availability != model.Unknown {
		t.Errorf("尚未查询时状态应当是未知，实际为 %v", row.Availability)
	}
	text, importance := statusAppearance(row)
	if text != "待查询" {
		t.Errorf("尚未查询应当显示「待查询」，实际为 %q", text)
	}
	if importance == widget.DangerImportance {
		t.Error("尚未查询不是故障，不该用错误色")
	}
	if fetcher.callCount() != 0 {
		t.Error("引擎未启动却发出了请求")
	}
}

// TestUnrecognizedPickupDisplayIsTreatedAsSchemaDrift 守卫「接口悄悄改词表」这条路径。
//
// Apple 若把 pickupDisplay 换成新取值或改名，解析器会安静地退回 Unknown。
// 如果引擎把它当成一次正常作答，界面上就是「未知」但没有任何原因、没有告警、
// 汇总行也不显示失败数 —— 甚至会把已经亮起的告警条误判成「已恢复」。
// 那是另一种形式的静默失效，和上游满屏「无货」属于同一类错误。
func TestUnrecognizedPickupDisplayIsTreatedAsSchemaDrift(t *testing.T) {
	fetcher := &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		out := &apple.StoreAvailability{StoreNumber: store, Parts: map[string]apple.PartStatus{}}
		for _, p := range parts {
			// 模拟 Apple 新增了一个我们没见过的取值。
			out.Parts[p] = apple.PartStatus{
				PartNumber:    p,
				Availability:  model.Unknown,
				PickupDisplay: "limitedAvailability",
				Recognized:    false,
			}
		}
		return out, nil
	}}
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)
	if healthy := runOneCycle(t, u, engine, fetcher); healthy {
		t.Error("接口词表已漂移，本轮却被报告为健康")
	}

	row := findRow(t, u, "iPhone 17 512GB 黑色")
	if row.Availability != model.Unknown {
		t.Errorf("无法识别的取值必须落在未知，实际为 %v", row.Availability)
	}
	if row.LastError == nil {
		t.Fatal("无法识别的取值没有产生错误原因，用户无从得知接口已变")
	}
	if !strings.Contains(row.LastError.Error(), "limitedAvailability") {
		t.Errorf("错误信息里必须带上原始取值才能排查，实际为 %q", row.LastError)
	}
	if !u.troubleBar.Visible() {
		t.Error("整店型号全部无法识别时应当亮起告警条")
	}
}

// TestPanicDuringQueryDoesNotLeaveStaleOutOfStock 守卫兜底路径本身。
//
// 单门店 goroutine 的 recover 曾经只发一个告警事件，既不更新状态也不记账。
// 后果是这些目标原封不动停在上一轮的取值上 —— 而那个取值很可能正是「无货」，
// 于是用户看到一个没有任何错误标记的陈旧「无货」，而且同一轮的 CycleComplete
// 还会把刚亮起的告警条收走并宣布「查询已恢复正常」。
func TestPanicDuringQueryDoesNotLeaveStaleOutOfStock(t *testing.T) {
	var shouldPanic bool
	var mu sync.Mutex

	fetcher := &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		mu.Lock()
		boom := shouldPanic
		mu.Unlock()
		if boom {
			panic("模拟查询过程中的内部错误")
		}
		out := &apple.StoreAvailability{StoreNumber: store, Parts: map[string]apple.PartStatus{}}
		for _, p := range parts {
			out.Parts[p] = apple.PartStatus{
				PartNumber: p, Availability: model.OutOfStock,
				PickupDisplay: "unavailable", Recognized: true,
			}
		}
		return out, nil
	}}
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)

	// 第一轮正常，拿到真实的「无货」。
	runOneCycle(t, u, engine, fetcher)
	if row := findRow(t, u, "iPhone 17 512GB 黑色"); row.Availability != model.OutOfStock {
		t.Fatalf("前置条件不成立，第一轮应当是无货，实际为 %v", row.Availability)
	}

	// 第二轮 panic。
	mu.Lock()
	shouldPanic = true
	mu.Unlock()

	if healthy := runOneCycle(t, u, engine, fetcher); healthy {
		t.Error("查询过程 panic，本轮却被报告为健康")
	}

	row := findRow(t, u, "iPhone 17 512GB 黑色")
	if row.Availability == model.OutOfStock {
		t.Fatal("panic 之后状态仍停在陈旧的「无货」，用户会以为这是真实结果")
	}
	if row.Availability != model.Unknown {
		t.Errorf("panic 之后状态应当是未知，实际为 %v", row.Availability)
	}
	if row.LastError == nil {
		t.Error("panic 之后没有留下错误原因")
	}
	if !u.troubleBar.Visible() {
		t.Error("panic 之后告警条应当可见")
	}
	for _, line := range u.logs {
		if strings.Contains(line, "查询已恢复正常") {
			t.Errorf("panic 之后不该宣布查询已恢复正常，日志: %q", line)
		}
	}
}

// inStockFetcher 返回一个按开关作答的假查询源：开着报有货，关着报无货。
func inStockFetcher(available *bool, mu *sync.Mutex) *fakeFetcher {
	return &fakeFetcher{fn: func(store string, parts []string) (*apple.StoreAvailability, error) {
		mu.Lock()
		hit := *available
		mu.Unlock()

		out := &apple.StoreAvailability{StoreNumber: store, StoreName: "环球港", Parts: map[string]apple.PartStatus{}}
		for _, p := range parts {
			status := apple.PartStatus{PartNumber: p, PickupDisplay: "unavailable", Recognized: true, Availability: model.OutOfStock}
			if hit {
				status.PickupDisplay = "available"
				status.Availability = model.InStock
			}
			out.Parts[p] = status
		}
		return out, nil
	}}
}

// TestInStockStillNotifiesWhenEventWasDropped 守卫「到货提醒不能因为丢事件而丢失」。
//
// 提醒原本只挂在 EventInStock 上，而引擎的事件通道写满时会直接丢事件
// （watcher.Engine.emit）。一旦这条边沿事件被丢掉，用户会在列表里看到「有货」
// 却收不到任何提醒 —— 提示音、Bark、系统通知全都没有。对一个盯抢购的工具来说，
// 这跟上游那屏永远不变的「无货」是同一类失效：界面还在动，但它已经没用了。
func TestInStockStillNotifiesWhenEventWasDropped(t *testing.T) {
	available := true
	var mu sync.Mutex
	fetcher := inStockFetcher(&available, &mu)
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)
	rec := recordNotifications(u)

	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)

	if row := findRow(t, u, "iPhone 17 512GB 黑色"); row.Availability != model.InStock {
		t.Fatalf("前置条件不成立，这一行应当是有货，实际为 %v", row.Availability)
	}
	if got := rec.products(); len(got) != 1 {
		t.Fatalf("EventInStock 被丢掉之后提醒也跟着没了，实际发出的提醒: %v", got)
	}
	if got := rec.states[0].Target.ProductName; got != "iPhone 17 512GB 黑色" {
		t.Errorf("提醒发给了错误的型号: %q", got)
	}

	var announced bool
	for _, line := range u.logs {
		if strings.Contains(line, "有货！") {
			announced = true
		}
	}
	if !announced {
		t.Error("补发提醒时日志里没有留下记录，用户无从知道刚才响过")
	}
}

// TestContinuedInStockDoesNotNotifyTwice 确认对账不会把持续有货变成反复提醒。
//
// 每轮结束都要对一次账，如果不记住「这个目标已经提醒过」，一直有货就会每轮都响，
// 半夜盯货的人会被吵到直接关掉程序 —— 那等于把监控也一起关了。
func TestContinuedInStockDoesNotNotifyTwice(t *testing.T) {
	available := true
	var mu sync.Mutex
	fetcher := inStockFetcher(&available, &mu)
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)
	rec := recordNotifications(u)

	// 第一轮事件完整送达，走的是 EventInStock 那条路。
	runOneCycle(t, u, engine, fetcher)
	if len(rec.states) != 1 {
		t.Fatalf("第一轮应当提醒一次，实际 %d 次", len(rec.states))
	}

	// 后面几轮仍然有货，事件送不送达都不该再响。
	runOneCycle(t, u, engine, fetcher)
	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)
	if got := rec.products(); len(got) != 1 {
		t.Errorf("持续有货被重复提醒了，实际发出的提醒: %v", got)
	}
}

// TestInStockNotifiesAgainAfterLeavingStock 确认补货能再次提醒。
//
// 边沿触发的另一半：目标离开有货状态后必须把「已提醒」的记号撤掉，
// 否则第一次抢完之后再补货就永远不会响了。
func TestInStockNotifiesAgainAfterLeavingStock(t *testing.T) {
	available := true
	var mu sync.Mutex
	fetcher := inStockFetcher(&available, &mu)
	targets := []model.Target{target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")}

	u, engine := newTestUI(t, fetcher, targets)
	rec := recordNotifications(u)

	// 全程只放行 EventCycleComplete，逼着界面完全靠对账做判断。
	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)
	if len(rec.states) != 1 {
		t.Fatalf("第一次有货应当提醒一次，实际 %d 次", len(rec.states))
	}

	mu.Lock()
	available = false
	mu.Unlock()
	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)
	if len(rec.states) != 1 {
		t.Fatalf("变成无货时不该发提醒，实际累计 %d 次", len(rec.states))
	}

	mu.Lock()
	available = true
	mu.Unlock()
	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)
	if got := rec.products(); len(got) != 2 {
		t.Errorf("补货之后应当再提醒一次，实际发出的提醒: %v", got)
	}
}

// TestNotifiedSetForgetsRemovedTargets 确认「已提醒」集合会随目标删除一起收缩。
//
// 两个后果：集合按 target key 累积，不清理就只涨不落；而且删掉再加回同一个目标
// 时旧记号还在，那次到货会被当成「已经提醒过」而静默跳过。
func TestNotifiedSetForgetsRemovedTargets(t *testing.T) {
	available := true
	var mu sync.Mutex
	fetcher := inStockFetcher(&available, &mu)
	tgt := target("R683", "上海-环球港", "MG724CH/A", "iPhone 17 512GB 黑色")

	u, engine := newTestUI(t, fetcher, []model.Target{tgt})
	rec := recordNotifications(u)

	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)
	if len(rec.states) != 1 {
		t.Fatalf("前置条件不成立，第一次有货应当提醒一次，实际 %d 次", len(rec.states))
	}

	u.applyTargets(nil)
	if len(u.notified) != 0 {
		t.Errorf("目标已被删除，提醒记号却还留着 %d 条", len(u.notified))
	}

	// 重新加回来的同一个目标必须能再次提醒。
	u.applyTargets([]model.Target{tgt})
	runCycleDropping(t, u, engine, fetcher, dropAllButCycleComplete)
	if got := rec.products(); len(got) != 2 {
		t.Errorf("删除后重新添加的目标有货时应当提醒，实际发出的提醒: %v", got)
	}
}

// slowStore 包着一个真实的 config.Store，只是把每次写盘拖慢一段固定时间。
//
// 拖慢是为了稳定地构造出「写盘 goroutine 已经把 pending 取空、正卡在
// Store.Save 里」这个时序 —— 真实的一次写入只有几百字节，快到根本撞不上，
// 而「关窗口时最后一次设置会不会丢」恰恰只在这个窗口里见分晓。
type slowStore struct {
	*config.Store
	// entered 在每次 Save 开始时投递一次，测试据此知道写盘已经真的开始了。
	entered chan struct{}
	delay   time.Duration
}

func (s *slowStore) Save(settings config.Settings) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	time.Sleep(s.delay)
	return s.Store.Save(settings)
}

// TestCloseWaitsForInFlightSave 守卫「关窗口不丢最后一次设置」。
//
// Close 曾经只在 pending 非空时补写一次，理由是「写盘 goroutine 已经退出了」，
// 可 close(quit) 只是发了个信号，没有任何同步保证。真实时序是：写盘 goroutine
// 刚取走 pending（于是 pending 变成 nil）、正卡在 Store.Save 里，Close 看到
// pending == nil 就直接返回，main 随之结束，运行时把这个 goroutine 连同写了
// 一半的临时文件一起终止 —— 改了半天的 Bark 地址没落盘，配置目录里还多出一个
// .settings-*.json。
func TestCloseWaitsForInFlightSave(t *testing.T) {
	dir := t.TempDir()
	disk := config.NewStoreAt(filepath.Join(dir, "settings.json"))
	slow := &slowStore{
		Store:   disk,
		entered: make(chan struct{}, 1),
		delay:   200 * time.Millisecond,
	}

	fetcher := &fakeFetcher{fn: func(string, []string) (*apple.StoreAvailability, error) {
		return nil, fmt.Errorf("不应当被调用")
	}}
	u, _ := newTestUI(t, fetcher, nil)
	// 顶替掉 newTestUI 装的那个真实 store，好把写盘拖慢。
	u.store = slow

	// New 期间控件的初始化回调（选中地区、勾上提示音）已经排了一次写盘。
	// 先把它清掉，否则下面等到的是那一次，构造不出想要的时序。
	u.saveMu.Lock()
	u.pending = nil
	u.saveMu.Unlock()
	select {
	case <-u.saveSig:
	default:
	}

	u.startSaver()

	const bark = "https://api.day.app/关窗口之前最后改的"
	u.mutateSettings(func(s *config.Settings) { s.BarkURL = bark })
	u.scheduleSave()

	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("写盘 goroutine 迟迟没有开始写")
	}

	// 此刻 pending 已经被取空，正是原先那句「已经退出了」漏掉的瞬间。
	u.Close()

	loaded, err := disk.Load()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if loaded.BarkURL != bark {
		t.Errorf("Close 返回时最后一次设置还没落盘，磁盘上的 Bark 地址是 %q", loaded.BarkURL)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取配置目录失败: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".settings-") {
			t.Errorf("配置目录里残留了写到一半的临时文件 %s", e.Name())
		}
	}
}

// TestClosePersistsPendingSettings 确认压在 pending 里的改动一定会被写完。
//
// 与上一个测试互补：那个盯的是「正在写的那次」，这个盯的是「还没轮到写的那次」，
// 两者合起来才是「改完设置立刻关窗口不会丢配置」。
func TestClosePersistsPendingSettings(t *testing.T) {
	dir := t.TempDir()
	disk := config.NewStoreAt(filepath.Join(dir, "settings.json"))

	fetcher := &fakeFetcher{fn: func(string, []string) (*apple.StoreAvailability, error) {
		return nil, fmt.Errorf("不应当被调用")
	}}
	u, _ := newTestUI(t, fetcher, nil)
	u.store = disk

	// 不启动写盘 goroutine：模拟「改完设置立刻关窗口，后台还没来得及动手」。
	u.mutateSettings(func(s *config.Settings) { s.IntervalSeconds = 45 })
	u.scheduleSave()
	u.Close()

	loaded, err := disk.Load()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if loaded.IntervalSeconds != 45 {
		t.Errorf("待写的设置没有落盘，磁盘上的查询间隔是 %d 秒", loaded.IntervalSeconds)
	}
}
