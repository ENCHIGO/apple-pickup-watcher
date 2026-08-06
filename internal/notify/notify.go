// Package notify 定义通知渠道的抽象，并提供把多个渠道并发组合起来的 Multi。
//
// 这里刻意不包含系统桌面通知：那需要 fyne.App，属于界面层，放进来会让整个包
// 无法脱离 GUI 单独测试，也会把 UI 依赖传染给调度层。
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Notification 是一条待发送的通知。
type Notification struct {
	// Title 是标题，如「到货提醒」。
	Title string
	// Body 是正文，如「上海-环球港 iPhone 17 512GB 黑色 有货」。
	Body string
	// URL 是点击通知后打开的地址，通常是该地区的购物袋页面；可以为空。
	URL string
}

// Notifier 是一个通知渠道。
//
// 实现必须遵守两条约定：ctx 取消时尽快返回；一切失败都以 error 返回，不允许
// panic。渠道未被用户配置时应当返回 nil —— 「没开这个功能」不是错误。
type Notifier interface {
	// Name 返回渠道名，只用于日志与错误信息。
	Name() string
	// Notify 发送一条通知。
	Notify(ctx context.Context, n Notification) error
}

// Multi 把多个渠道组合成一个渠道，本身也满足 Notifier，可以再被嵌套。
type Multi struct {
	notifiers []Notifier
}

// NewMulti 组合若干渠道。传入的 nil 会被忽略，因此界面层可以按用户设置写成
// NewMulti(bark, sound) 而不必先剔除没启用的那些。
func NewMulti(notifiers ...Notifier) *Multi {
	kept := make([]Notifier, 0, len(notifiers))
	for _, n := range notifiers {
		if n == nil {
			continue
		}
		kept = append(kept, n)
	}
	return &Multi{notifiers: kept}
}

// Name 返回组合内各渠道的名字，形如 multi(Bark, 提示音)。
func (m *Multi) Name() string {
	if m == nil || len(m.notifiers) == 0 {
		return "multi()"
	}
	names := make([]string, 0, len(m.notifiers))
	for _, n := range m.notifiers {
		names = append(names, safeName(n))
	}
	return "multi(" + strings.Join(names, ", ") + ")"
}

// Notify 并发调用全部渠道，等所有渠道结束后用 errors.Join 汇总错误。
//
// 并发而不是串行，是因为渠道之间没有依赖：Bark 服务器无响应要等到超时，
// 串行的话本地提示音就得跟着干等十几秒，而提示音恰恰是最需要立刻响的那个。
func (m *Multi) Notify(ctx context.Context, n Notification) error {
	if m == nil || len(m.notifiers) == 0 {
		return nil
	}

	// 定长切片按下标写入，各 goroutine 写各自的槽位，互不相干，不需要加锁；
	// 单个渠道失败只会落在自己的槽位上，不影响其他渠道继续执行。
	errs := make([]error, len(m.notifiers))
	var wg sync.WaitGroup
	for i, notifier := range m.notifiers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = safeNotify(ctx, notifier, n)
		}()
	}
	wg.Wait()

	// errors.Join 会忽略其中的 nil，全部成功时返回 nil。
	return errors.Join(errs...)
}

// safeNotify 调用单个渠道，并把 panic 兜成 error。
//
// 这是最后一道防线。渠道实现自身不该 panic，但通知总是在后台 goroutine 里发的，
// 一旦有渠道 panic 且无人 recover，Go 会直接终止整个进程 —— 上游的 Bark 推送
// 出错就 panic（services/listen.go:302），而它是被 go 起在后台的
// （services/listen.go:144），网络一抖程序就没了，即上游 issue #114「bark 闪退」。
func safeNotify(ctx context.Context, n Notifier, msg Notification) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("渠道 %s 内部错误已被拦截: %v", safeName(n), r)
		}
	}()
	if err := n.Notify(ctx, msg); err != nil {
		return fmt.Errorf("%s: %w", safeName(n), err)
	}
	return nil
}

// safeName 取渠道名。它被用在 recover 之后的错误信息里，所以自己也不能出事。
func safeName(n Notifier) (name string) {
	defer func() {
		if recover() != nil {
			name = "未知渠道"
		}
	}()
	if n == nil {
		return "未知渠道"
	}
	if name = n.Name(); name == "" {
		name = "未命名渠道"
	}
	return name
}
