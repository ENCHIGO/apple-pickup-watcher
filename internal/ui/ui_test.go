package ui

import (
	"context"
	"fmt"
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

// runOneCycle 让引擎跑到至少完成一轮查询后停下，然后把积压的事件喂给界面，
// 返回最后一个 EventCycleComplete 报告的健康状态。
func runOneCycle(t *testing.T, u *UI, engine *watcher.Engine, fetcher *fakeFetcher) bool {
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
