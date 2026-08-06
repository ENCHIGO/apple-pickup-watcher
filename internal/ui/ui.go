// Package ui 实现 Fyne 桌面界面。
//
// 这个包是整个程序里唯一允许触碰 Fyne 的地方，也是唯一需要关心「哪段代码跑在
// 哪个线程」的地方。约定只有一条，但必须严格遵守：
//
//	所有读写界面控件的代码都只能在 Fyne 主线程上执行。
//
// 后台 goroutine（事件消费、通知发送、目录刷新、写盘）一律通过 onMain 把界面
// 更新调度回主线程。上游没有这个约定：它在轮询 goroutine 里直接
// s.Logs.SetText（services/listen.go:109）、直接 dialog.ShowInformation
// 和 SendNotification（:138、:139），而 Fyne 的渲染状态不是并发安全的，
// 这类跨线程写入的表现是随机的界面错乱与崩溃。
package ui

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/catalog"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/config"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/notify"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/watcher"
)

const (
	// windowTitle 是窗口标题。
	windowTitle = "Apple Pickup Watcher"

	// maxLogLines 是日志区保留的行数上限。
	//
	// 上游把全部状态和日志拼成一个不断增长的大字符串塞进一个 Label
	// （services/listen.go:94-110），跑一整天下来这个字符串能有几 MB，
	// 每次刷新都要重新排版一遍，界面会越用越卡。这里用定长环形保留最近若干条。
	maxLogLines = 300

	// notifyTimeout 是一次通知（提示音 + Bark）的总超时。
	notifyTimeout = 30 * time.Second

	// refreshTimeout 是在线刷新商品目录的总超时。
	// 一个地区要抓三个购买页，每页几百 KB，留足余量但不允许无限等待。
	refreshTimeout = 90 * time.Second
)

// Deps 是界面运行所需的全部外部依赖，由 main 组装后注入。
//
// 标注为「可为 nil」的字段代表一项可以缺席的能力：对应功能会在界面上停用并说明
// 原因，而不是让程序起不来。这是本项目对初始化失败的统一态度 —— 音频设备不可用
// 或配置目录不可写，都不该导致一个盯库存的工具直接退出。
type Deps struct {
	// App 是 Fyne 应用实例，必填。
	App fyne.App
	// Catalog 提供门店与型号目录，必填。
	Catalog *catalog.Catalog
	// Engine 是监控调度引擎，必填。
	Engine *watcher.Engine

	// Store 负责设置持久化；为 nil 时本次运行的设置改动不落盘。
	Store *config.Store
	// Fetcher 提供在线抓取商品目录的能力；为 nil 时「刷新型号列表」按钮停用。
	Fetcher catalog.ProductFetcher
	// Sound 是提示音渠道；为 nil 时提示音相关功能停用。
	Sound notify.Notifier
	// HTTPClient 供 Bark 复用连接池；为 nil 时 Bark 自建一个带超时的客户端。
	HTTPClient *http.Client

	// Settings 是已经加载好的用户设置。
	Settings config.Settings
	// Warnings 是启动过程中的降级说明，界面打开后写进日志区让用户看见。
	Warnings []string
}

// UI 持有窗口与全部控件。
//
// 除非注释另有说明，所有字段都只能在 Fyne 主线程上访问。
type UI struct {
	app     fyne.App
	win     fyne.Window
	catalog *catalog.Catalog
	engine  *watcher.Engine
	store   *config.Store
	fetcher catalog.ProductFetcher
	sound   notify.Notifier
	http    *http.Client

	// settingsMu 保护 settings。设置由主线程修改，但发通知的后台 goroutine
	// 需要读 Bark 地址与几个开关，因此不能当成纯主线程状态。
	settingsMu sync.RWMutex
	settings   config.Settings

	// saveMu 保护 pending。
	saveMu  sync.Mutex
	pending *config.Settings
	saveSig chan struct{}

	// rows 是状态列表的数据源，logs 是日志区的数据源，都只在主线程读写。
	rows     []watcher.State
	logs     []string
	selected widget.ListItemID

	// 控件。
	regionRadio   *widget.RadioGroup
	storeSelect   *widget.Select
	productSelect *widget.Select
	intervalEntry *widget.Entry
	barkEntry     *widget.Entry
	soundCheck    *widget.Check
	openBagCheck  *widget.Check
	addButton     *widget.Button
	removeButton  *widget.Button
	clearButton   *widget.Button
	refreshButton *widget.Button
	testSoundBtn  *widget.Button
	testBarkBtn   *widget.Button
	startButton   *widget.Button
	stopButton    *widget.Button
	runStateLabel *widget.Label
	summaryLabel  *widget.Label
	statusList    *widget.List
	logList       *widget.List
	troubleBar    *fyne.Container
	troubleLabel  *widget.Label
	troubleBG     *canvas.Rectangle

	// quit 在关闭时被 close，通知所有后台 goroutine 退出。
	quit chan struct{}
	// closed 让已经排队的界面更新在窗口关掉之后自行作废。
	closed    atomic.Bool
	closeOnce sync.Once
}

// New 构造界面。它只搭好窗口内容，不显示窗口，也不启动任何后台 goroutine。
func New(d Deps) (*UI, error) {
	if d.App == nil {
		return nil, errors.New("缺少 fyne.App")
	}
	if d.Catalog == nil {
		return nil, errors.New("缺少商品目录")
	}
	if d.Engine == nil {
		return nil, errors.New("缺少监控引擎")
	}

	d.Settings.Normalize()

	u := &UI{
		app:      d.App,
		win:      d.App.NewWindow(windowTitle),
		catalog:  d.Catalog,
		engine:   d.Engine,
		store:    d.Store,
		fetcher:  d.Fetcher,
		sound:    d.Sound,
		http:     d.HTTPClient,
		settings: d.Settings,
		saveSig:  make(chan struct{}, 1),
		selected: -1,
		quit:     make(chan struct{}),
	}

	u.build()
	u.refreshRows()
	u.updateRunState()

	for _, w := range d.Warnings {
		u.logf("启动提示：%s", w)
	}
	if u.store == nil {
		u.logf("设置无法持久化，本次的改动在退出后会丢失。")
	} else {
		u.logf("设置文件：%s", u.store.Path())
	}
	return u, nil
}

// Window 返回主窗口，供 main 做额外配置。
func (u *UI) Window() fyne.Window { return u.win }

// Run 显示窗口并进入 Fyne 主循环，直到窗口关闭才返回。
//
// 必须在 main goroutine 上调用：Fyne 的渲染循环绑定在启动进程的那个 OS 线程上。
func (u *UI) Run() {
	u.startSaver()
	u.safeGo("消费监控事件", u.consumeEvents)

	u.win.Resize(fyne.NewSize(1060, 800))
	u.win.CenterOnScreen()
	// 窗口关闭与主循环退出都要收尾：用户可能关窗口，也可能是别的窗口结束了整个应用。
	u.win.SetOnClosed(u.Close)
	u.win.ShowAndRun()
	u.Close()
}

// Close 停止引擎与全部后台 goroutine，并把还没落盘的设置写完。可以重复调用。
func (u *UI) Close() {
	u.closeOnce.Do(func() {
		// 先置位再关引擎：此后排队中的界面更新都会自行作废，
		// 避免在窗口已经销毁之后还去碰控件。
		u.closed.Store(true)
		close(u.quit)

		u.engine.Stop()

		// 写盘 goroutine 已经退出了，最后一次改动可能还压在 pending 里。
		// 这里同步补写一次，代价是几毫秒，换的是「刚改完设置就关窗口」不丢配置。
		if u.store != nil {
			u.saveMu.Lock()
			last := u.pending
			u.pending = nil
			u.saveMu.Unlock()
			if last != nil {
				if err := u.store.Save(*last); err != nil {
					log.Printf("退出前保存设置失败: %v", err)
				}
			}
		}
	})
}

// onMain 把 fn 调度回 Fyne 主线程执行，只能从后台 goroutine 调用。
//
// 用 fyne.Do 而不是 fyne.DoAndWait：后者会阻塞调用方直到主线程执行完，
// 一旦主线程正好在等这个 goroutine（比如点「暂停」后等引擎停下来），
// 两边就死锁了。界面更新没有等待的必要。
func (u *UI) onMain(fn func()) {
	if u.closed.Load() {
		return
	}
	fyne.Do(func() {
		if u.closed.Load() {
			return
		}
		// 这段代码跑在 Fyne 的主循环里，此处的 panic 会直接带走整个进程，
		// 连界面日志都来不及写。兜底之后退回标准错误输出。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("界面更新时内部错误已被拦截: %v", r)
			}
		}()
		fn()
	})
}

// safeGo 起一个带 panic 兜底的后台 goroutine。
//
// Go 里任意 goroutine 的 panic 只要没被捕获就会终止整个进程，上游的「bark 闪退」
// （services/listen.go:302 在 :144 起的后台 goroutine 里 panic）就是这么来的。
// 本包起的每个 goroutine 都从这里出发，保证没有漏网的。
func (u *UI) safeGo(what string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				u.logfAsync("%s时内部错误已被拦截：%v", what, r)
			}
		}()
		fn()
	}()
}

// snapshotSettings 返回一份设置副本，可以从任意 goroutine 调用。
func (u *UI) snapshotSettings() config.Settings {
	u.settingsMu.RLock()
	defer u.settingsMu.RUnlock()
	s := u.settings
	// Targets 是切片，直接复制结构体只复制切片头，调用方拿到的仍是同一块底层数组。
	s.Targets = append([]model.Target(nil), u.settings.Targets...)
	return s
}

// mutateSettings 在锁内修改设置，只应从主线程调用。
func (u *UI) mutateSettings(fn func(s *config.Settings)) {
	u.settingsMu.Lock()
	fn(&u.settings)
	u.settingsMu.Unlock()
}

// startSaver 起一个串行写盘 goroutine。
//
// 设置里的 Bark 地址是逐字符输入的，每敲一个字就同步写一次盘，意味着每次按键都要
// 走一遍「建临时文件 + 写 + fsync + 改名」，机械盘上足以让输入框卡顿。这里把写盘
// 挪到后台，并且只保留最新的一份 —— 中途那些已经过期的快照没有落盘的必要。
func (u *UI) startSaver() {
	if u.store == nil {
		return
	}
	u.safeGo("保存设置", func() {
		for {
			select {
			case <-u.quit:
				return
			case <-u.saveSig:
				u.saveMu.Lock()
				next := u.pending
				u.pending = nil
				u.saveMu.Unlock()
				if next == nil {
					continue
				}
				if err := u.store.Save(*next); err != nil {
					u.logfAsync("保存设置失败：%v", err)
				}
			}
		}
	})
}

// scheduleSave 请求把当前设置写盘，立刻返回。只应从主线程调用。
func (u *UI) scheduleSave() {
	if u.store == nil {
		return
	}
	s := u.snapshotSettings()

	u.saveMu.Lock()
	u.pending = &s
	u.saveMu.Unlock()

	// 信号通道容量为 1：已经有待处理信号时不必再发，写盘 goroutine 取到的
	// 总是最新的那份 pending。
	select {
	case u.saveSig <- struct{}{}:
	default:
	}
}

// logf 往日志区追加一行，只能在主线程调用。
func (u *UI) logf(format string, args ...any) {
	u.pushLog(fmt.Sprintf(format, args...))
}

// logfAsync 从后台 goroutine 往日志区追加一行。
//
// 格式化在调用方的 goroutine 上完成：args 里可能有会被继续修改的值，
// 留到主线程再格式化就成了数据竞争。
func (u *UI) logfAsync(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	u.onMain(func() { u.pushLog(msg) })
}

// pushLog 把一行日志放到最前面，只能在主线程调用。
func (u *UI) pushLog(msg string) {
	line := time.Now().Format("15:04:05") + "  " + msg
	// 最新的在最上面：出问题时用户第一眼看到的应该是刚发生的事，
	// 而不是要先滚到底部。
	u.logs = append([]string{line}, u.logs...)
	if len(u.logs) > maxLogLines {
		u.logs = u.logs[:maxLogLines]
	}
	if u.logList != nil {
		u.logList.Refresh()
	}
}
