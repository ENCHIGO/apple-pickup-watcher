package ui

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"fyne.io/fyne/v2"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/notify"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/watcher"
)

// consumeEvents 消费引擎的事件流，跑在自己的后台 goroutine 上。
//
// 引擎的事件通道在缓冲写满时会丢弃事件（watcher.Engine.emit），所以这里绝不能
// 在处理某个事件时长时间占着不动：所有耗时的活（发通知、播提示音）都交给
// safeGo 另起 goroutine，本循环只负责分发。
func (u *UI) consumeEvents() {
	events := u.engine.Events()
	for {
		select {
		case <-u.quit:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			u.handleEvent(ev)
		}
	}
}

// handleEvent 分发单个事件。跑在后台 goroutine 上，因此任何界面操作都要经过 onMain。
func (u *UI) handleEvent(ev watcher.Event) {
	switch ev.Kind {
	case watcher.EventStateChanged:
		state := ev.State
		u.onMain(func() {
			u.refreshRows()
			u.logStateChange(state)
		})

	case watcher.EventInStock:
		state := ev.State
		u.onMain(func() {
			u.refreshRows()
			u.logf("有货！%s %s，快去下单。", state.Target.StoreTitle, state.Target.ProductName)
		})
		// 通知里既有网络请求也有音频播放，都可能要等好几秒，不能占住事件循环。
		u.safeGo("发送到货通知", func() { u.dispatchNotifications(state) })

	case watcher.EventCycleComplete:
		healthy := ev.Healthy
		u.onMain(func() {
			u.refreshRows()
			u.maybeClearTrouble(healthy)
		})

	case watcher.EventTrouble:
		reason := ev.Reason
		u.onMain(func() {
			u.showTrouble(reason)
			u.logf("告警：%s", reason)
		})
	}
}

// logStateChange 只把值得看的状态变化写进日志。
//
// 每一行的实时状态在列表里已经有了，日志里再逐条重复会把真正重要的信息淹掉。
// 「变成未知」是要记的：它意味着这一行的数据从此不可信。
func (u *UI) logStateChange(state watcher.State) {
	switch {
	case state.Availability == model.Unknown && state.LastError != nil:
		// 同一个故障每轮都会重复上报，只在第一次失败时记一条，
		// 否则被拦截时日志会被同一条信息刷屏。
		if state.ConsecutiveFailures <= 1 {
			u.logf("%s %s 查询失败：%v", state.Target.StoreTitle, state.Target.ProductName, state.LastError)
		}
	case state.Availability == model.OutOfStock:
		// 措辞上不说「变为」：首次查到的结果和「刚刚从有货变成无货」都会走到这里，
		// 而 State 里没有上一次的取值，分不清就不要写得像分得清。
		u.logf("%s %s 现在无货。", state.Target.StoreTitle, state.Target.ProductName)
	}
}

// refreshRows 从引擎重新取一份状态快照并刷新列表，只能在主线程调用。
func (u *UI) refreshRows() {
	u.rows = u.engine.Snapshot()
	if u.statusList != nil {
		if u.selected >= len(u.rows) {
			// 目标被删掉之后旧的选中下标会越界，必须连同列表上的高亮一起撤销，
			// 否则「删除选中」下一次会删到另一行。
			u.selected = -1
			u.statusList.UnselectAll()
		}
		u.statusList.Refresh()
	}
	u.updateSummary()
}

// updateSummary 刷新底部的汇总行。
func (u *UI) updateSummary() {
	var inStock, outOfStock, unknown, failed int
	for _, s := range u.rows {
		switch s.Availability {
		case model.InStock:
			inStock++
		case model.OutOfStock:
			outOfStock++
		default:
			unknown++
			if s.LastError != nil {
				failed++
			}
		}
	}

	summary := fmt.Sprintf("监控 %d 项 · 有货 %d · 无货 %d · 未知 %d", len(u.rows), inStock, outOfStock, unknown)
	if failed > 0 {
		// 把「其中查询失败多少项」单独点出来：这个数字大于 0 时，
		// 界面上那些「无货」也未必反映真实情况。
		summary += fmt.Sprintf("（其中 %d 项查询失败）", failed)
	}
	u.summaryLabel.SetText(summary)
}

// showTrouble 显示顶部告警条，只能在主线程调用。
func (u *UI) showTrouble(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "查询接口返回了预期之外的结果"
	}
	u.troubleLabel.SetText("监控当前不可信：" + reason +
		"\n此时列表里的状态不代表门店的真实库存，请先排查原因，不要干等。")
	// 主题可能在运行期间被系统切换过（浅色/深色），底色跟着重算一次。
	u.troubleBG.FillColor = troubleFill()
	u.troubleBG.Refresh()
	u.troubleBar.Show()
	u.troubleBar.Refresh()
}

// hideTrouble 收起告警条，只能在主线程调用。
func (u *UI) hideTrouble() {
	u.troubleBar.Hide()
	u.troubleBar.Refresh()
}

// maybeClearTrouble 在引擎明确报告本轮健康时收起告警条。
//
// healthy 由引擎给出（watcher.Event.Healthy），表示这一轮所有目标都拿到了明确
// 答复。这里不能再像原先那样遍历 u.rows 看「有没有人带着错误」来反推恢复：
// 有些故障路径根本不会更新状态，旧的错误早被清掉，于是刚刚因为故障亮起的告警条
// 会被同一轮的 CycleComplete 立刻收走，还会补上一句「查询已恢复正常」——
// 一个恰好与事实相反的陈述。恢复与否只有引擎知道，让它明说。
func (u *UI) maybeClearTrouble(healthy bool) {
	if !u.troubleBar.Visible() || !healthy {
		return
	}
	u.hideTrouble()
	u.logf("查询已恢复正常。")
}

// dispatchNotifications 在有货时按用户设置发出全部提醒。跑在后台 goroutine 上。
//
// 任何一个渠道失败都只写进日志，绝不向上冒泡成崩溃 —— 提醒没发出去是遗憾，
// 而上游那种「Bark 请求出错就 panic」（services/listen.go:302）连带把监控
// 一起弄没了，才是真正的损失。
func (u *UI) dispatchNotifications(state watcher.State) {
	settings := u.snapshotSettings()

	title := "到货提醒"
	body := fmt.Sprintf("%s %s 现在可以到店取货", state.Target.StoreTitle, state.Target.ProductName)
	bagURL := ""
	if region, ok := model.RegionByLocale(state.Target.Locale); ok {
		bagURL = region.BagURL()
	}

	// 系统通知与打开浏览器都要经过 fyne.App，必须回到主线程执行。
	u.onMain(func() {
		u.app.SendNotification(fyne.NewNotification(title, body))
		if settings.OpenBagOnHit && bagURL != "" {
			u.openBag(bagURL)
		}
	})

	// Bark 每次现构造：地址是用户随时可改的设置项，缓存一个实例就会在改地址后
	// 继续往旧地址推。共享的 *http.Client 一并传进去，连接池仍然是复用的。
	var channels []notify.Notifier
	if settings.SoundEnabled && u.sound != nil {
		channels = append(channels, u.sound)
	}
	if strings.TrimSpace(settings.BarkURL) != "" {
		channels = append(channels, notify.NewBark(settings.BarkURL, u.http))
	}
	if len(channels) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := notify.NewMulti(channels...).Notify(ctx, notify.Notification{
		Title: title,
		Body:  body,
		URL:   bagURL,
	}); err != nil {
		u.logfAsync("发送提醒时出错（不影响监控）：%v", err)
	}
}

// openBag 打开购物袋页面，只能在主线程调用。
func (u *UI) openBag(bagURL string) {
	link, err := url.Parse(bagURL)
	if err != nil {
		u.logf("购物袋地址无法解析：%v", err)
		return
	}
	if err := u.app.OpenURL(link); err != nil {
		// 打不开浏览器不是致命问题，把地址写进日志让用户自己复制。
		u.logf("打开购物袋失败：%v，请手动访问 %s", err, bagURL)
	}
}
