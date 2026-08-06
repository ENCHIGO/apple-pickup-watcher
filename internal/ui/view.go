package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/config"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/notify"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/watcher"
)

// helpText 是顶部的使用说明。
//
// 第 4 条是这个程序和上游最关键的差别，必须写在用户第一眼能看到的地方：
// 上游把查询失败显示成「无货」，用户完全没有机会知道程序已经失效了。
const helpText = `1. 先在 Apple 官网登录账号并把想买的型号加进购物袋 —— 本工具只负责盯库存，不会替你下单。
2. 选好地区、门店和型号，点「添加」加入下面的监控列表，可以同时盯多个门店和多个型号。
3. 点「开始」。检测到有货时会按下面的设置播放提示音、推送 Bark、发系统通知并打开购物袋。
4. 状态显示「未知」表示这一轮没查到，不等于没货，具体原因写在同一行的「说明」列里。`

// build 搭建窗口内容。只能在主线程调用。
func (u *UI) build() {
	u.buildTroubleBar()
	u.buildSelectors()
	u.buildSettings()
	u.buildButtons()
	u.buildStatusList()
	u.buildLogList()

	selectorForm := widget.NewForm(
		widget.NewFormItem("选择地区", u.regionRadio),
		widget.NewFormItem("选择门店", u.storeSelect),
		widget.NewFormItem("选择型号", u.productSelect),
	)
	settingsForm := widget.NewForm(
		widget.NewFormItem("查询间隔(秒)", u.intervalEntry),
		widget.NewFormItem("Bark 地址", u.barkEntry),
		widget.NewFormItem("有货时", container.NewHBox(u.soundCheck, u.openBagCheck)),
	)

	help := widget.NewLabel(helpText)
	help.Wrapping = fyne.TextWrapWord

	actionRow := container.NewBorder(nil, nil,
		container.NewHBox(u.addButton, u.removeButton, u.clearButton, u.refreshButton),
		container.NewHBox(u.testSoundBtn, u.testBarkBtn, widget.NewSeparator(),
			u.startButton, u.stopButton, u.runStateLabel),
	)

	top := container.NewVBox(
		u.troubleBar,
		help,
		selectorForm,
		settingsForm,
		actionRow,
		widget.NewSeparator(),
	)

	statusPane := container.NewBorder(u.statusHeader(), nil, nil, nil, u.statusList)
	logPane := container.NewBorder(
		widget.NewLabelWithStyle("日志", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil, u.logList)

	split := container.NewVSplit(statusPane, logPane)
	// 状态列表是主角，日志只在出问题时才需要细看。
	split.Offset = 0.68

	u.win.SetContent(container.NewBorder(top, u.summaryLabel, nil, nil, split))
}

// buildTroubleBar 搭建顶部告警条。
//
// 这条告警是本项目存在的理由的直接体现：接口被拦、响应结构变了这类故障发生时，
// 每一行状态都会退化成「未知」，如果不在显眼处说明「监控当前不可信」，用户会
// 以为程序还在正常工作，只是恰好没货 —— 上游正是这样静默失效了整整一个版本。
func (u *UI) buildTroubleBar() {
	u.troubleLabel = widget.NewLabel("")
	u.troubleLabel.Importance = widget.DangerImportance
	u.troubleLabel.TextStyle = fyne.TextStyle{Bold: true}
	u.troubleLabel.Wrapping = fyne.TextWrapWord

	u.troubleBG = canvas.NewRectangle(troubleFill())

	dismiss := widget.NewButtonWithIcon("", theme.CancelIcon(), func() {
		u.hideTrouble()
	})
	dismiss.Importance = widget.LowImportance

	u.troubleBar = container.NewStack(
		u.troubleBG,
		container.NewBorder(nil, nil, widget.NewIcon(theme.WarningIcon()), dismiss, u.troubleLabel),
	)
	u.troubleBar.Hide()
}

// troubleFill 返回告警条的底色：主题里的错误色调到低透明度。
//
// 直接用不透明的错误色会把上面的文字压得看不清，而低透明度的同一颜色既醒目又
// 能跟随浅色/深色主题，不必自己维护两套配色。
func troubleFill() color.Color {
	r, g, b, _ := theme.Color(theme.ColorNameError).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 48}
}

// buildSelectors 搭建地区、门店、型号三个选择器。
func (u *UI) buildSelectors() {
	u.storeSelect = widget.NewSelect(nil, nil)
	u.storeSelect.PlaceHolder = "请选择自提门店"
	u.productSelect = widget.NewSelect(nil, nil)
	u.productSelect.PlaceHolder = "请选择 iPhone 型号"

	u.regionRadio = widget.NewRadioGroup(model.RegionTitles(), func(title string) {
		// 取消选中时回调会带一个空串进来，此时什么都不该做。
		if title == "" {
			return
		}
		region, ok := model.RegionByTitle(title)
		if !ok {
			return
		}
		u.mutateSettings(func(s *config.Settings) { s.Locale = region.Locale })
		u.scheduleSave()
		u.reloadOptions(region.Locale, true)
	})
	u.regionRadio.Horizontal = true

	current := u.snapshotSettings()
	region, ok := model.RegionByLocale(current.Locale)
	if !ok {
		region = model.Regions[0]
	}
	// 先直接填一次选项，再让 SetSelected 去触发回调。RadioGroup.SetSelected 在
	// 值没变化时会直接返回，不会回调，所以填充选项这件事不能完全托付给它。
	u.reloadOptions(region.Locale, false)
	u.regionRadio.SetSelected(region.Title)
}

// reloadOptions 按地区重填门店与型号选项。
//
// clear 为 true 时清空已选项：切换地区后原来选中的门店在新地区并不存在，
// 留着它会让「添加」拿到一个查不到的门店。
func (u *UI) reloadOptions(locale string, clear bool) {
	if titles, err := u.catalog.StoreTitles(locale); err != nil {
		u.storeSelect.SetOptions(nil)
		u.storeSelect.ClearSelected()
		u.logf("载入门店列表失败：%v", err)
	} else {
		u.storeSelect.SetOptions(titles)
		if clear {
			u.storeSelect.ClearSelected()
		}
	}

	if titles, err := u.catalog.ProductTitles(locale); err != nil {
		u.productSelect.SetOptions(nil)
		u.productSelect.ClearSelected()
		u.logf("载入型号列表失败：%v", err)
	} else {
		u.productSelect.SetOptions(titles)
		if clear {
			u.productSelect.ClearSelected()
		}
	}
}

// buildSettings 搭建查询间隔、Bark 地址与两个开关，改动即时生效并落盘。
func (u *UI) buildSettings() {
	current := u.snapshotSettings()

	u.intervalEntry = widget.NewEntry()
	u.intervalEntry.SetText(strconv.Itoa(current.IntervalSeconds))
	u.intervalEntry.Validator = func(text string) error {
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			return errors.New("请填写整数秒数")
		}
		if n < config.MinIntervalSeconds {
			// 下限不是随手定的：上游 500 毫秒一轮的频率正是被风控盯上的原因，
			// 接口关掉之后所有人都用不了。
			return fmt.Errorf("不能小于 %d 秒", config.MinIntervalSeconds)
		}
		return nil
	}
	u.intervalEntry.OnChanged = func(text string) {
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < config.MinIntervalSeconds {
			// 校验器已经把错误提示在输入框上了，这里静默跳过即可，
			// 不能把一个非法值写进设置。
			return
		}
		u.mutateSettings(func(s *config.Settings) { s.IntervalSeconds = n })
		// 引擎此刻很可能正在跑，SetInterval 是并发安全的，下一轮等待就会生效。
		u.engine.SetInterval(time.Duration(n) * time.Second)
		u.scheduleSave()
	}

	u.barkEntry = widget.NewEntry()
	u.barkEntry.SetPlaceHolder("https://api.day.app/你的BarkKey（留空表示不推送）")
	u.barkEntry.SetText(current.BarkURL)
	u.barkEntry.OnChanged = func(text string) {
		u.mutateSettings(func(s *config.Settings) { s.BarkURL = strings.TrimSpace(text) })
		u.scheduleSave()
	}

	u.soundCheck = widget.NewCheck("播放提示音", func(on bool) {
		u.mutateSettings(func(s *config.Settings) { s.SoundEnabled = on })
		u.scheduleSave()
		if on && u.sound == nil {
			u.logf("提示音不可用（音频资源缺失），这个开关不会生效。")
		}
	})
	u.soundCheck.SetChecked(current.SoundEnabled)

	u.openBagCheck = widget.NewCheck("自动打开购物袋", func(on bool) {
		u.mutateSettings(func(s *config.Settings) { s.OpenBagOnHit = on })
		u.scheduleSave()
	})
	u.openBagCheck.SetChecked(current.OpenBagOnHit)
}

// buildButtons 搭建全部按钮。
func (u *UI) buildButtons() {
	u.addButton = widget.NewButton("添加", u.onAdd)
	u.addButton.Importance = widget.HighImportance
	u.removeButton = widget.NewButton("删除选中", u.onRemoveSelected)
	u.clearButton = widget.NewButton("清空", u.onClear)

	u.refreshButton = widget.NewButton("刷新型号列表", u.onRefreshProducts)
	if u.fetcher == nil {
		u.refreshButton.Disable()
	}

	u.testSoundBtn = widget.NewButton("试听提示音", u.onTestSound)
	if u.sound == nil {
		u.testSoundBtn.Disable()
	}
	u.testBarkBtn = widget.NewButton("测试 Bark", u.onTestBark)

	u.startButton = widget.NewButton("开始", u.onStart)
	u.startButton.Importance = widget.HighImportance
	u.stopButton = widget.NewButton("暂停", u.onStop)

	u.runStateLabel = widget.NewLabel("")
	u.summaryLabel = widget.NewLabel("")
}

// statusColumns 是状态列表的列标题，行与表头共用同一个网格布局保证对齐。
var statusColumns = []string{"状态", "门店", "型号", "最后检查", "说明"}

// statusHeader 返回状态列表的表头。
func (u *UI) statusHeader() fyne.CanvasObject {
	cells := make([]fyne.CanvasObject, 0, len(statusColumns))
	for _, name := range statusColumns {
		cells = append(cells, widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	}
	return container.New(layout.NewGridLayoutWithColumns(len(statusColumns)), cells...)
}

// buildStatusList 搭建监控状态列表。
//
// 用 widget.List 而不是像上游那样把所有状态拼成一个大字符串塞进 Label
// （services/listen.go:94-110）：List 只渲染可见的那几行，状态再多也不会变卡，
// 而且每个单元格是独立控件，能按状态单独着色。
func (u *UI) buildStatusList() {
	u.statusList = widget.NewList(
		func() int { return len(u.rows) },
		func() fyne.CanvasObject {
			cells := make([]fyne.CanvasObject, 0, len(statusColumns))
			for range statusColumns {
				cells = append(cells, newCellLabel())
			}
			return container.New(layout.NewGridLayoutWithColumns(len(statusColumns)), cells...)
		},
		func(id widget.ListItemID, item fyne.CanvasObject) {
			row, ok := item.(*fyne.Container)
			if !ok || len(row.Objects) != len(statusColumns) {
				return
			}
			if id < 0 || id >= len(u.rows) {
				return
			}
			state := u.rows[id]

			text, importance := statusAppearance(state)
			checked := "—"
			if !state.LastChecked.IsZero() {
				checked = state.LastChecked.Format("15:04:05")
			}
			// 出错时把原因写进「说明」列。这一列存在的唯一目的就是让「未知」
			// 有个交代 —— 只显示三个字的「未知」和上游显示「无货」一样没用。
			note := ""
			if state.LastError != nil {
				note = state.LastError.Error()
				if state.ConsecutiveFailures > 1 {
					note = fmt.Sprintf("连续失败 %d 次：%s", state.ConsecutiveFailures, note)
				}
			}

			values := []string{text, state.Target.StoreTitle, state.Target.ProductName, checked, note}
			for i, obj := range row.Objects {
				label, ok := obj.(*widget.Label)
				if !ok {
					continue
				}
				label.Importance = widget.MediumImportance
				if i == 0 {
					label.Importance = importance
				}
				if i == len(values)-1 && note != "" {
					label.Importance = widget.DangerImportance
				}
				label.SetText(values[i])
			}
		},
	)

	u.statusList.OnSelected = func(id widget.ListItemID) { u.selected = id }
	u.statusList.OnUnselected = func(widget.ListItemID) { u.selected = -1 }
}

// buildLogList 搭建日志区。
func (u *UI) buildLogList() {
	u.logList = widget.NewList(
		func() int { return len(u.logs) },
		func() fyne.CanvasObject { return newCellLabel() },
		func(id widget.ListItemID, item fyne.CanvasObject) {
			label, ok := item.(*widget.Label)
			if !ok || id < 0 || id >= len(u.logs) {
				return
			}
			label.SetText(u.logs[id])
		},
	)
}

// newCellLabel 返回一个用于列表单元格的标签。
//
// 关掉换行并启用省略号：某些 Apple 的错误信息很长，允许换行会让行高在刷新时
// 反复跳动，列表滚动起来会抖。完整内容仍然会写进日志区。
func newCellLabel() *widget.Label {
	label := widget.NewLabel("")
	label.Wrapping = fyne.TextWrapOff
	label.Truncation = fyne.TextTruncateEllipsis
	return label
}

// statusAppearance 把一条状态翻译成展示文本与配色。
//
// 「未知 + 有错误原因」和「尚未查询」必须分开：前者是故障，要用错误色让用户
// 立刻注意到；后者只是还没轮到，用弱化色即可。上游把这两种和「无货」全部混成
// 一种显示，用户无从分辨程序是在正常工作还是已经废了。
func statusAppearance(state watcher.State) (string, widget.Importance) {
	switch state.Availability {
	case model.InStock:
		return "有货", widget.SuccessImportance
	case model.OutOfStock:
		return "无货", widget.MediumImportance
	default:
		if state.LastError != nil {
			return "未知(查询失败)", widget.DangerImportance
		}
		if state.LastChecked.IsZero() {
			return "待查询", widget.LowImportance
		}
		return "未知", widget.WarningImportance
	}
}

// onAdd 把当前选中的门店与型号加入监控列表。
func (u *UI) onAdd() {
	current := u.snapshotSettings()
	if u.storeSelect.Selected == "" || u.productSelect.Selected == "" {
		dialog.ShowInformation("还差一步", "请先选择门店和型号。", u.win)
		return
	}

	// 用返回 (值, bool) 的查找，找不到就提示用户重选。上游同样的位置是
	// funk.Find(...).(model.Product) 硬断言（services/product.go:19），
	// 选中项在刷新目录后不存在时直接 panic 掉整个程序。
	store, ok := u.catalog.StoreByTitle(current.Locale, u.storeSelect.Selected)
	if !ok {
		dialog.ShowInformation("门店已失效", "这个门店在当前地区的数据里找不到了，请重新选择。", u.win)
		u.storeSelect.ClearSelected()
		return
	}
	product, ok := u.catalog.ProductByTitle(current.Locale, u.productSelect.Selected)
	if !ok {
		dialog.ShowInformation("型号已失效", "这个型号在当前地区的数据里找不到了，请重新选择。", u.win)
		u.productSelect.ClearSelected()
		return
	}

	target := model.Target{
		Locale:      current.Locale,
		StoreNumber: store.Number,
		StoreTitle:  store.Title,
		PartNumber:  product.PartNumber,
		ProductName: product.Title,
	}
	for _, existing := range current.Targets {
		if existing.Key() == target.Key() {
			u.logf("%s %s 已经在监控列表里了。", target.StoreTitle, target.ProductName)
			return
		}
	}

	u.applyTargets(append(current.Targets, target))
	u.logf("已添加：%s %s", target.StoreTitle, target.ProductName)
}

// onRemoveSelected 删除列表中选中的那一行。
func (u *UI) onRemoveSelected() {
	if u.selected < 0 || u.selected >= len(u.rows) {
		dialog.ShowInformation("还差一步", "请先在下面的列表里选中要删除的那一行。", u.win)
		return
	}
	removed := u.rows[u.selected].Target

	current := u.snapshotSettings()
	kept := make([]model.Target, 0, len(current.Targets))
	for _, t := range current.Targets {
		if t.Key() != removed.Key() {
			kept = append(kept, t)
		}
	}

	u.statusList.UnselectAll()
	u.selected = -1
	u.applyTargets(kept)
	u.logf("已删除：%s %s", removed.StoreTitle, removed.ProductName)
}

// onClear 清空监控列表。
func (u *UI) onClear() {
	if len(u.rows) == 0 {
		return
	}
	dialog.ShowConfirm("清空监控列表", "确定要移除全部监控项吗？", func(confirmed bool) {
		if !confirmed {
			return
		}
		u.statusList.UnselectAll()
		u.selected = -1
		u.applyTargets(nil)
		u.logf("已清空监控列表。")
	}, u.win)
}

// applyTargets 把新的监控目标同时写进引擎、设置和界面。
func (u *UI) applyTargets(targets []model.Target) {
	u.engine.SetTargets(targets)
	u.mutateSettings(func(s *config.Settings) { s.Targets = targets })
	u.scheduleSave()
	u.refreshRows()
}

// onStart 开始监控。
func (u *UI) onStart() {
	if len(u.rows) == 0 {
		dialog.ShowInformation("监控列表是空的", "请先添加至少一个门店和型号。", u.win)
		return
	}
	u.engine.Start()
	u.updateRunState()
	u.logf("已开始监控，每 %s 查询一轮。", u.engine.Interval())
}

// onStop 暂停监控。
//
// Stop 要等当前这一轮的请求真正结束才返回，直接在按钮回调里调用会把界面卡住，
// 所以放到后台执行，期间先把两个按钮禁用掉，避免用户连点。
func (u *UI) onStop() {
	if !u.engine.Running() {
		return
	}
	u.startButton.Disable()
	u.stopButton.Disable()
	u.runStateLabel.SetText("状态：正在停止…")

	u.safeGo("停止监控", func() {
		u.engine.Stop()
		u.onMain(func() {
			u.startButton.Enable()
			u.stopButton.Enable()
			u.updateRunState()
			u.logf("已暂停监控。")
		})
	})
}

// updateRunState 刷新运行状态标签与按钮可用性。
func (u *UI) updateRunState() {
	if u.engine.Running() {
		u.runStateLabel.SetText("状态：监控中")
	} else {
		u.runStateLabel.SetText("状态：已暂停")
	}
}

// onRefreshProducts 从 Apple 购买页在线刷新当前地区的型号列表。
func (u *UI) onRefreshProducts() {
	if u.fetcher == nil {
		return
	}
	current := u.snapshotSettings()
	region, ok := model.RegionByLocale(current.Locale)
	if !ok {
		u.logf("当前地区无效，无法刷新型号列表。")
		return
	}

	u.refreshButton.Disable()
	u.refreshButton.SetText("正在刷新…")
	u.logf("正在从 Apple 官网刷新 %s 的型号列表…", region.Title)

	u.safeGo("刷新型号列表", func() {
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		err := u.catalog.RefreshProducts(ctx, region, u.fetcher)
		titles, titlesErr := u.catalog.ProductTitles(region.Locale)

		u.onMain(func() {
			u.refreshButton.SetText("刷新型号列表")
			u.refreshButton.Enable()

			// 刷新期间用户可能已经切到别的地区了，这时不该把结果盖到新地区的选择器上。
			latest := u.snapshotSettings()
			if latest.Locale == region.Locale && titlesErr == nil {
				selected := u.productSelect.Selected
				u.productSelect.SetOptions(titles)
				// 同名型号仍然存在就保留原来的选择，免得用户白选一次。
				u.productSelect.ClearSelected()
				u.productSelect.SetSelected(selected)
			}

			switch {
			case err != nil:
				u.logf("刷新型号列表失败：%v", err)
				dialog.ShowInformation("刷新未完成",
					"没能从 Apple 官网取到最新型号，已继续使用内置数据。\n\n"+err.Error(), u.win)
			case titlesErr != nil:
				u.logf("刷新后读取型号列表失败：%v", titlesErr)
			default:
				u.logf("型号列表已更新，共 %d 个型号。", len(titles))
			}
		})
	})
}

// onTestSound 试听提示音。
func (u *UI) onTestSound() {
	if u.sound == nil {
		return
	}
	u.safeGo("试听提示音", func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		// 上游同样的按钮是 go services.Listen.AlertMp3()（main.go:140），
		// 而 AlertMp3 解码失败就 panic（services/listen.go:281），
		// 一台没有可用音频设备的机器点一下这个按钮就能让程序消失。
		if err := u.sound.Notify(ctx, notify.Notification{Title: "试听", Body: "提示音测试"}); err != nil {
			u.logfAsync("试听提示音失败：%v", err)
		}
	})
}

// onTestBark 用当前填写的地址发一条测试推送。
func (u *UI) onTestBark() {
	current := u.snapshotSettings()
	if strings.TrimSpace(current.BarkURL) == "" {
		dialog.ShowInformation("还没填地址", "请先在「Bark 地址」里填入 Bark App 给出的推送地址。", u.win)
		return
	}
	region, ok := model.RegionByLocale(current.Locale)
	bagURL := ""
	if ok {
		bagURL = region.BagURL()
	}

	u.testBarkBtn.Disable()
	u.safeGo("测试 Bark 推送", func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		err := notify.NewBark(current.BarkURL, u.http).Notify(ctx, notify.Notification{
			Title: "到货提醒（测试）",
			Body:  "这是一条测试推送，点击会打开购物袋页面。",
			URL:   bagURL,
		})
		u.onMain(func() {
			u.testBarkBtn.Enable()
			if err != nil {
				u.logf("Bark 测试失败：%v", err)
				dialog.ShowInformation("Bark 测试失败", err.Error(), u.win)
				return
			}
			u.logf("Bark 测试推送已发出，请检查手机是否收到。")
		})
	})
}
