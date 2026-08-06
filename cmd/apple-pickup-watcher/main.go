// Command apple-pickup-watcher 轮询 Apple 直营店的到店取货库存，有货时提醒用户。
//
// 这个文件只做组装：把客户端、目录、引擎、设置、通知渠道拼起来交给界面层。
// 组装过程中的每一步失败都是「少一项能力」，不是「程序不能跑」 —— 音频设备
// 不可用、配置目录不可写、字体缺失，都降级继续并把原因显示在界面上。整个入口
// 里没有 panic，也没有除参数无效之外的 os.Exit。
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"fyne.io/fyne/v2/app"

	assets "github.com/ENCHIGO/apple-pickup-watcher"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/catalog"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/config"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/notify"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/ui"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/watcher"
)

// appID 是 Fyne 用来隔离偏好设置与系统通知身份的标识，与 Makefile 里打包用的
// APP_ID 保持一致，否则 .app 与 go run 起来的会被系统当成两个不同的应用。
const appID = "io.github.enchigo.apple-pickup-watcher"

// barkTimeout 是 Bark 推送的超时。
const barkTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		// 走到这一步说明连窗口都建不出来，界面自然也没法用来报错，
		// 只能退回标准错误输出。
		log.Printf("启动失败：%v", err)
		os.Exit(1)
	}
}

func run() error {
	fyneApp := app.NewWithID(appID)

	// warnings 收集组装过程中的降级说明，界面打开后一次性写进日志区。
	// 这些信息不能只写到 stderr：用户是双击图标启动的，看不到终端。
	var warnings []string

	if theme, err := ui.NewChineseTheme("font.ttf", assets.FontTTF); err != nil {
		warnings = append(warnings, "中文字体不可用，界面中文可能显示为方框："+err.Error())
	} else {
		fyneApp.Settings().SetTheme(theme)
	}

	// 客户端全进程只建一个。上游每查一次就 gorequest.New() 一个新的
	// （services/listen.go:221），连接池完全无法复用，空闲连接与相关 goroutine
	// 持续堆积，内存能涨到十几 GB。
	client := apple.NewClient()
	products := catalog.New()
	engine := watcher.New(client)

	store, err := config.NewStore()
	if err != nil {
		warnings = append(warnings, "配置目录不可用，本次运行的设置不会被保存："+err.Error())
		store = nil
	}

	settings := config.Default()
	if store != nil {
		loaded, err := store.Load()
		if err != nil {
			// 读到一半失败（权限、IO 错误、JSON 损坏）时必须放弃写盘能力。
			//
			// 否则接下来的后果是不可逆的：界面在 build 阶段会调用
			// regionRadio.SetSelected 和 soundCheck.SetChecked，这两个控件的
			// 初值与要设的值不同，必定触发 OnChanged，进而 scheduleSave，
			// 启动后几毫秒内就把仅存的那份配置原子替换成一份空的默认配置，
			// 用户的整个监控列表就此消失，且没有备份。
			//
			// 这也正是本项目的一贯立场：无法判定的事不要当成已经判定。
			// 「读不到旧配置」不等于「用户没有配置」。
			backup, backupErr := store.PreserveCorrupted()
			switch {
			case backupErr != nil:
				warnings = append(warnings, "读取设置失败，且无法备份原文件："+backupErr.Error())
			case backup != "":
				warnings = append(warnings, "读取设置失败，原文件已备份到 "+backup)
			}
			warnings = append(warnings,
				"读取设置失败，已回退到默认设置，并且本次运行不会覆盖磁盘上的配置："+err.Error())
			store = nil
		} else {
			settings = loaded
		}
	}
	settings.Normalize()

	// 恢复上次的监控目标与查询间隔。两个方法都是并发安全的，此刻引擎还没启动，
	// 界面拿到的第一份 Snapshot 就已经是上次的列表了。
	engine.SetInterval(settings.Interval())
	engine.SetTargets(settings.Targets)

	// 音频设备的初始化推迟到第一次播放时才做（notify.Sound.prepare），
	// 那时失败会以 error 的形式出现在界面日志里，而不是像上游那样在 main 里
	// 无条件 speaker.Init 且不看返回值，设备不可用时后续行为完全未定义。
	var sound notify.Notifier
	if len(assets.AlertMP3) == 0 {
		warnings = append(warnings, "提示音资源缺失，有货时不会响铃。")
	} else {
		sound = notify.NewSound(assets.AlertMP3)
	}

	window, err := ui.New(ui.Deps{
		App:     fyneApp,
		Catalog: products,
		Engine:  engine,
		Store:   store,
		// apple.Client 同时实现了在线抓取商品目录的能力，直接复用它的限速与重试。
		Fetcher: client,
		Sound:   sound,
		// Bark 与库存查询走的是完全不同的主机，共用 apple.Client 的连接池没有意义，
		// 但 Bark 自己这一份必须复用，否则每次推送都新建一个连接池。
		HTTPClient: &http.Client{Timeout: barkTimeout},
		Settings:   settings,
		Warnings:   warnings,
	})
	if err != nil {
		return err
	}

	// Run 内部已经保证窗口关闭与主循环退出时都会停掉引擎并落盘设置。
	window.Run()
	return nil
}
