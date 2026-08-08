// Package config 负责用户设置的读写。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// appDir 是配置目录名。
const appDir = "apple-pickup-watcher"

// MaxSettingsBytes 是读取设置文件时接受的字节上限。
//
// 依据：一份正常配置只有几百字节；单条 Target 序列化后不到 200 字节，就算用户
// 登记上千个目标也才几百 KB，1 MiB 是数量级上的余量，正常使用永远碰不到。
// 之所以必须有上限，是因为 Load 原先用 os.ReadFile 整读：磁盘损坏、别的程序
// 写错了文件、或有人刻意构造出一个几 GB 的 settings.json 时，内存会在任何
// 错误处理之前就被吃光，进程直接被杀 —— 用户连「配置读不出来」这句提示都
// 看不到，更谈不上备份原文件。
const MaxSettingsBytes = 1 << 20

// ErrTooLarge 表示设置文件超过了 MaxSettingsBytes。
//
// 单独留一个哨兵值，是为了让调用方能用 errors.Is 把「文件大得离谱」与
// 「JSON 坏了」「读不动」区分开，给出的提示不一样；至于处置方式，三者是一致的：
// 都属于「读不到旧配置」，都必须放弃写盘并把原文件备份留存，绝不能当成
// 「用户没有配置」而用一份默认配置把它覆盖掉。
var ErrTooLarge = errors.New("设置文件超过大小上限")

// Settings 是持久化的用户设置。
type Settings struct {
	// Locale 是上次选择的地区。
	Locale string `json:"locale"`
	// Targets 是监控目标列表。
	Targets []model.Target `json:"targets"`
	// IntervalSeconds 是每轮查询之间的间隔秒数。
	IntervalSeconds int `json:"interval_seconds"`
	// BarkURL 为空表示不启用 Bark 推送。
	BarkURL string `json:"bark_url"`
	// SoundEnabled 控制有货时是否播放提示音。
	SoundEnabled bool `json:"sound_enabled"`
	// OpenBagOnHit 控制有货时是否自动打开购物袋页面。
	OpenBagOnHit bool `json:"open_bag_on_hit"`
}

// DefaultIntervalSeconds 是默认查询间隔。
//
// 上游写死 500 毫秒一轮（services/listen.go:154），即每个门店每秒两次请求。
// 这个频率对一个公开的商品查询接口来说过高，是触发风控的直接原因，也是
// 上游 issue 里 503 / 541 / Service Unavailable 反复出现的背景。30 秒足以
// 满足抢购场景，同时不会把自己送进黑名单。
const DefaultIntervalSeconds = 30

// MinIntervalSeconds 是允许设置的下限。
//
// 保留手动调低的余地，但不允许低到必然触发风控的程度。
const MinIntervalSeconds = 5

// Default 返回一份默认设置。
func Default() Settings {
	return Settings{
		Locale:          model.Regions[0].Locale,
		Targets:         nil,
		IntervalSeconds: DefaultIntervalSeconds,
		SoundEnabled:    true,
		OpenBagOnHit:    true,
	}
}

// Normalize 修正越界或缺失的字段，使设置始终可用。
func (s *Settings) Normalize() {
	if _, ok := model.RegionByLocale(s.Locale); !ok {
		s.Locale = model.Regions[0].Locale
	}
	if s.IntervalSeconds < MinIntervalSeconds {
		s.IntervalSeconds = DefaultIntervalSeconds
	}
	// 去掉重复目标，避免同一门店同一型号被重复查询。
	//
	// 这里必须新建切片，不能用 s.Targets[:0] 就地压缩：Save 虽然按值收 Settings，
	// 但 Targets 与调用方共用同一块底层数组，就地写回等于改掉了调用方的数据。
	// 调用方持有 [A, A, B] 时，去重结果 [A, B] 会把底层数组压成 [A, B, B]，
	// 而它的切片长度仍是 3 —— 内容悄悄从 [A, A, B] 变成了 [A, B, B]。
	// 界面上「改设置 → Save → engine.SetTargets(s.Targets)」这条路径因此会
	// 莫名多出一条重复的监控行。
	seen := make(map[string]struct{}, len(s.Targets))
	kept := make([]model.Target, 0, len(s.Targets))
	for _, t := range s.Targets {
		if t.StoreNumber == "" || t.PartNumber == "" {
			continue
		}
		if _, dup := seen[t.Key()]; dup {
			continue
		}
		seen[t.Key()] = struct{}{}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		// 没有目标时收敛回 nil，与 Default() 保持一致：空切片和 nil 在
		// reflect.DeepEqual 下不相等，留着空切片会让 Default() 不再是规范化的。
		kept = nil
	}
	s.Targets = kept
}

// Interval 返回查询间隔。
func (s Settings) Interval() time.Duration {
	return time.Duration(s.IntervalSeconds) * time.Second
}

// Store 读写位于用户配置目录下的设置文件，并发安全。
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore 返回一个指向标准用户配置目录的 Store。
//
// 上游把 user_settings.json 直接写在进程工作目录（services/setting.go:24）。
// 打包成 macOS .app 之后，工作目录取决于应用被如何启动，可能是 / 之类
// 不可写的位置，设置会静默丢失。这里改用系统约定的配置目录。
func NewStore() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("定位用户配置目录失败: %w", err)
	}
	full := filepath.Join(dir, appDir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		return nil, fmt.Errorf("创建配置目录 %s 失败: %w", full, err)
	}
	return &Store{path: filepath.Join(full, "settings.json")}, nil
}

// NewStoreAt 返回一个指定路径的 Store，供测试使用。
func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

// Path 返回设置文件的完整路径，便于在界面上告诉用户配置存在哪。
func (s *Store) Path() string {
	return s.path
}

// Load 读取设置。文件不存在时返回默认设置且不报错。
func (s *Store) Load() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("打开设置文件失败: %w", err)
	}
	defer f.Close()

	// 比上限多读一个字节：读满了就说明文件确实更大，从而在不整读的前提下判定超限。
	data, err := io.ReadAll(io.LimitReader(f, MaxSettingsBytes+1))
	if err != nil {
		return Default(), fmt.Errorf("读取设置失败: %w", err)
	}
	if len(data) > MaxSettingsBytes {
		return Default(), fmt.Errorf("设置文件超过 %d 字节上限: %w", MaxSettingsBytes, ErrTooLarge)
	}

	settings := Default()
	if err := json.Unmarshal(data, &settings); err != nil {
		// 配置损坏时退回默认值继续运行，而不是让程序起不来。
		return Default(), fmt.Errorf("解析设置失败: %w", err)
	}
	settings.Normalize()
	return settings, nil
}

// Save 原子地写入设置。
//
// 先写临时文件再改名，避免写入过程中崩溃或断电留下半截 JSON，
// 导致下次启动时配置无法解析。
func (s *Store) Save(settings Settings) error {
	settings.Normalize()

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化设置失败: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*.json")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 改名成功后这里会因文件已不存在而无副作用

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换设置文件失败: %w", err)
	}
	return nil
}

// PreserveCorrupted 把当前这份无法读取或无法解析的设置文件改名留存，
// 返回留存后的路径；原本就没有文件时返回空字符串且不报错。
//
// Load 失败之后调用方会放弃写盘，但仅仅不写还不够：用户下次可能想把里面的
// 监控列表捞回来，或者需要这份文件来判断到底哪里坏了。改名而不是复制，
// 是因为读失败的情形（权限、IO 错误）下未必读得动，而改名只需要目录的写权限。
func (s *Store) PreserveCorrupted() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("检查设置文件失败: %w", err)
	}

	backup := s.path + ".corrupt-" + time.Now().Format("20060102-150405")
	if err := os.Rename(s.path, backup); err != nil {
		return "", fmt.Errorf("备份损坏的设置文件失败: %w", err)
	}
	return backup, nil
}

// Clear 删除设置文件。文件本就不存在时不报错。
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除设置文件失败: %w", err)
	}
	return nil
}
