package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/config"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// newStore 在用例私有的临时目录里建一个 Store。
//
// 绝不能碰 os.UserConfigDir()：那会污染开发者本机的真实配置，
// 也让测试结果取决于机器上原本存了什么。
func newStore(t *testing.T) (*config.Store, string) {
	t.Helper()
	dir := t.TempDir()
	return config.NewStoreAt(filepath.Join(dir, "settings.json")), dir
}

func sampleTarget(store, part string) model.Target {
	return model.Target{
		Locale:      "zh_CN",
		StoreNumber: store,
		StoreTitle:  "上海-" + store,
		PartNumber:  part,
		ProductName: "iPhone 17 " + part,
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	store, _ := newStore(t)

	want := config.Settings{
		Locale: "ja_JP",
		Targets: []model.Target{
			sampleTarget("R683", "MG724CH/A"),
			sampleTarget("R409", "MG834CH/A"),
		},
		IntervalSeconds: 45,
		BarkURL:         "https://api.day.app/xxxxx",
		SoundEnabled:    false,
		OpenBagOnHit:    false,
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("回读的设置与写入的不一致:\n got = %+v\nwant = %+v", got, want)
	}
}

// TestLoadMissingFileReturnsDefault 验证首次启动（还没有配置文件）不报错。
func TestLoadMissingFileReturnsDefault(t *testing.T) {
	store, dir := newStore(t)

	got, err := store.Load()
	if err != nil {
		t.Fatalf("文件不存在时 Load 返回了错误: %v", err)
	}
	if !reflect.DeepEqual(got, config.Default()) {
		t.Fatalf("Load = %+v, 期望等于 Default() = %+v", got, config.Default())
	}

	// Load 不应该顺手把文件创建出来。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Load 之后目录里多出了 %d 个文件: %v", len(entries), entries)
	}
}

// TestLoadCorruptFileReturnsDefaultWithError 验证配置损坏时程序还能起来。
//
// 半截 JSON 是断电或强杀进程的常见产物。这时候宁可回到默认设置继续跑，
// 也不能让用户面对一个打不开的程序；但必须带上 error 让界面能提示。
func TestLoadCorruptFileReturnsDefaultWithError(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"完全不是 JSON", "这不是 JSON"},
		{"截断的 JSON", `{"locale":"zh_CN","targets":[{"store_`},
		{"字段类型不对", `{"interval_seconds":"三十"}`},
		{"targets 不是数组", `{"targets":42}`},
		{"空文件", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newStore(t)
			if err := os.WriteFile(store.Path(), []byte(tc.content), 0o600); err != nil {
				t.Fatalf("写入损坏的配置失败: %v", err)
			}

			got, err := store.Load()
			if err == nil {
				t.Fatal("配置损坏时 Load 没有返回错误")
			}
			if !reflect.DeepEqual(got, config.Default()) {
				t.Fatalf("Load = %+v, 期望回退到 Default() = %+v", got, config.Default())
			}
		})
	}
}

// TestLoadUnreadablePathReturnsDefault 验证读不动配置文件时也只是报错而非崩溃。
func TestLoadUnreadablePathReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	// 把一个目录当成配置文件路径，os.ReadFile 会失败但不是 ErrNotExist。
	blocked := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	store := config.NewStoreAt(blocked)

	got, err := store.Load()
	if err == nil {
		t.Fatal("路径不可读时 Load 没有返回错误")
	}
	if !reflect.DeepEqual(got, config.Default()) {
		t.Fatalf("Load = %+v, 期望回退到 Default()", got)
	}
}

// TestLoadKeepsDefaultsForMissingFields 验证旧版本写下的配置升级后仍然可用。
//
// settings 以 Default() 为底再 Unmarshal，所以新增字段在老配置里缺失时
// 会拿到默认值，而不是 Go 的零值（那会让 SoundEnabled 变成 false、
// IntervalSeconds 变成 0）。
func TestLoadKeepsDefaultsForMissingFields(t *testing.T) {
	store, _ := newStore(t)
	if err := os.WriteFile(store.Path(), []byte(`{"locale":"ja_JP"}`), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.Locale != "ja_JP" {
		t.Errorf("Locale = %q, 期望 ja_JP", got.Locale)
	}
	if got.IntervalSeconds != config.DefaultIntervalSeconds {
		t.Errorf("IntervalSeconds = %d, 期望沿用默认值 %d", got.IntervalSeconds, config.DefaultIntervalSeconds)
	}
	if !got.SoundEnabled {
		t.Error("SoundEnabled = false, 期望沿用默认值 true")
	}
}

// TestLoadNormalizesStoredSettings 验证手工改坏的配置在读取时被修正。
func TestLoadNormalizesStoredSettings(t *testing.T) {
	store, _ := newStore(t)
	raw := `{
		"locale":"xx_YY",
		"interval_seconds":1,
		"targets":[
			{"locale":"zh_CN","store_number":"R683","part_number":"MG724CH/A"},
			{"locale":"zh_CN","store_number":"R683","part_number":"MG724CH/A"}
		]
	}`
	if err := os.WriteFile(store.Path(), []byte(raw), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.Locale != model.Regions[0].Locale {
		t.Errorf("Locale = %q, 期望被修正为 %q", got.Locale, model.Regions[0].Locale)
	}
	if got.IntervalSeconds != config.DefaultIntervalSeconds {
		t.Errorf("IntervalSeconds = %d, 期望被修正为 %d", got.IntervalSeconds, config.DefaultIntervalSeconds)
	}
	if len(got.Targets) != 1 {
		t.Errorf("Targets 数量 = %d, 期望去重后为 1", len(got.Targets))
	}
}

func TestNormalizeInterval(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		// 上游写死 500ms 一轮，是它反复吃到 503/541 的直接原因，
		// 所以这里守住下限：低于 MinIntervalSeconds 一律拉回默认值。
		{"零值", 0, config.DefaultIntervalSeconds},
		{"负数", -10, config.DefaultIntervalSeconds},
		{"低于下限", config.MinIntervalSeconds - 1, config.DefaultIntervalSeconds},
		{"正好等于下限", config.MinIntervalSeconds, config.MinIntervalSeconds},
		{"合法值原样保留", 120, 120},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := config.Settings{Locale: "zh_CN", IntervalSeconds: tc.in}
			s.Normalize()
			if s.IntervalSeconds != tc.want {
				t.Fatalf("IntervalSeconds = %d, 期望 %d", s.IntervalSeconds, tc.want)
			}
			if got := s.Interval(); got != time.Duration(tc.want)*time.Second {
				t.Errorf("Interval() = %v, 期望 %v", got, time.Duration(tc.want)*time.Second)
			}
		})
	}
}

func TestNormalizeLocale(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空字符串", "", model.Regions[0].Locale},
		{"地区表里没有的 locale", "xx_YY", model.Regions[0].Locale},
		{"大小写不匹配", "zh_cn", model.Regions[0].Locale},
		{"合法 locale 原样保留", "en_AU", "en_AU"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := config.Settings{Locale: tc.in, IntervalSeconds: config.DefaultIntervalSeconds}
			s.Normalize()
			if s.Locale != tc.want {
				t.Fatalf("Locale = %q, 期望 %q", s.Locale, tc.want)
			}
			if _, ok := model.RegionByLocale(s.Locale); !ok {
				t.Fatalf("修正后的 locale %q 仍然不在地区表里", s.Locale)
			}
		})
	}
}

// TestNormalizeDeduplicatesTargets 验证重复目标被去掉。
//
// 同一门店同一型号被登记两次，就意味着每轮多发一次请求、有货时多响一次，
// 纯粹是在给风控送素材。
func TestNormalizeDeduplicatesTargets(t *testing.T) {
	a := sampleTarget("R683", "MG724CH/A")
	b := sampleTarget("R683", "MG834CH/A")
	c := sampleTarget("R409", "MG724CH/A")

	// 同一个 Key 但展示名不同，也算重复：Key 由 locale/门店号/零件号决定。
	aRenamed := a
	aRenamed.ProductName = "改了个名字"

	s := config.Settings{
		Locale:          "zh_CN",
		IntervalSeconds: config.DefaultIntervalSeconds,
		Targets:         []model.Target{a, b, a, c, aRenamed, b},
	}
	s.Normalize()

	want := []model.Target{a, b, c}
	if !reflect.DeepEqual(s.Targets, want) {
		t.Fatalf("去重结果 = %+v\n期望 = %+v", s.Targets, want)
	}
}

func TestNormalizeDropsIncompleteTargets(t *testing.T) {
	good := sampleTarget("R683", "MG724CH/A")
	noStore := sampleTarget("", "MG834CH/A")
	noPart := sampleTarget("R409", "")

	s := config.Settings{
		Locale:          "zh_CN",
		IntervalSeconds: config.DefaultIntervalSeconds,
		Targets:         []model.Target{noStore, good, noPart},
	}
	s.Normalize()

	if len(s.Targets) != 1 || s.Targets[0] != good {
		t.Fatalf("Targets = %+v, 期望只剩下 %+v", s.Targets, good)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	s := config.Settings{
		Locale:          "xx_YY",
		IntervalSeconds: 1,
		Targets: []model.Target{
			sampleTarget("R683", "MG724CH/A"),
			sampleTarget("R683", "MG724CH/A"),
		},
	}
	s.Normalize()
	once := s
	once.Targets = append([]model.Target(nil), s.Targets...)

	s.Normalize()
	if !reflect.DeepEqual(s, once) {
		t.Fatalf("二次 Normalize 结果发生变化:\n first = %+v\nsecond = %+v", once, s)
	}
}

func TestDefaultIsAlreadyNormalized(t *testing.T) {
	s := config.Default()
	before := s
	s.Normalize()
	if !reflect.DeepEqual(s, before) {
		t.Fatalf("Default() 不是规范化的: %+v -> %+v", before, s)
	}
	if _, ok := model.RegionByLocale(s.Locale); !ok {
		t.Errorf("Default().Locale = %q 不在地区表里", s.Locale)
	}
	if s.IntervalSeconds < config.MinIntervalSeconds {
		t.Errorf("Default().IntervalSeconds = %d, 低于下限 %d", s.IntervalSeconds, config.MinIntervalSeconds)
	}
}

// TestSaveIsAtomic 验证写入是「先临时文件再改名」，且不留垃圾。
//
// 直接覆盖写会在崩溃时留下半截 JSON，下次启动就读不出配置了。
// 临时文件如果没清干净，配置目录会被一堆 .settings-*.json 塞满。
func TestSaveIsAtomic(t *testing.T) {
	store, dir := newStore(t)

	for i := 0; i < 3; i++ {
		s := config.Default()
		s.IntervalSeconds = 30 + i
		if err := store.Save(s); err != nil {
			t.Fatalf("第 %d 次 Save 失败: %v", i+1, err)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读取目录失败: %v", err)
		}
		if len(entries) != 1 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("第 %d 次 Save 之后目录里有 %d 个文件: %v，期望只有 settings.json",
				i+1, len(entries), names)
		}
		if name := entries[0].Name(); name != "settings.json" {
			t.Fatalf("目录里的文件是 %q, 期望 settings.json", name)
		}
		if strings.HasPrefix(entries[0].Name(), ".settings-") {
			t.Fatalf("残留了临时文件 %q", entries[0].Name())
		}
	}

	// 落盘内容必须是完整合法的 JSON。
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("读取设置文件失败: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("设置文件不是合法 JSON:\n%s", data)
	}
}

// TestSaveNormalizesBeforeWriting 验证非法值不会被写进文件。
func TestSaveNormalizesBeforeWriting(t *testing.T) {
	store, _ := newStore(t)

	err := store.Save(config.Settings{
		Locale:          "xx_YY",
		IntervalSeconds: 1,
		Targets: []model.Target{
			sampleTarget("R683", "MG724CH/A"),
			sampleTarget("R683", "MG724CH/A"),
		},
	})
	if err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.Locale != model.Regions[0].Locale {
		t.Errorf("Locale = %q, 期望写入前被修正为 %q", got.Locale, model.Regions[0].Locale)
	}
	if got.IntervalSeconds != config.DefaultIntervalSeconds {
		t.Errorf("IntervalSeconds = %d, 期望写入前被修正为 %d", got.IntervalSeconds, config.DefaultIntervalSeconds)
	}
	if len(got.Targets) != 1 {
		t.Errorf("Targets 数量 = %d, 期望写入前去重为 1", len(got.Targets))
	}
}

// TestSaveToMissingDirectoryFails 验证目录不存在时老老实实返回 error。
func TestSaveToMissingDirectoryFails(t *testing.T) {
	store := config.NewStoreAt(filepath.Join(t.TempDir(), "不存在的子目录", "settings.json"))

	if err := store.Save(config.Default()); err == nil {
		t.Fatal("目录不存在时 Save 没有返回错误")
	}
}

func TestClear(t *testing.T) {
	store, _ := newStore(t)

	// 文件本就不存在时 Clear 不应报错。
	if err := store.Clear(); err != nil {
		t.Fatalf("文件不存在时 Clear 返回错误: %v", err)
	}

	if err := store.Save(config.Default()); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatalf("Clear 之后文件仍然存在: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Clear 之后 Load 返回错误: %v", err)
	}
	if !reflect.DeepEqual(got, config.Default()) {
		t.Errorf("Clear 之后 Load = %+v, 期望 Default()", got)
	}
}

func TestPathReportsFileLocation(t *testing.T) {
	want := filepath.Join(t.TempDir(), "settings.json")
	if got := config.NewStoreAt(want).Path(); got != want {
		t.Fatalf("Path() = %q, 期望 %q", got, want)
	}
}

// TestConcurrentSaveAndLoad 验证界面上连续点保存的同时后台在读，不会读到半截文件。
func TestConcurrentSaveAndLoad(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save(config.Default()); err != nil {
		t.Fatalf("初始 Save 失败: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 64)

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s := config.Default()
				s.IntervalSeconds = 30 + i
				s.Targets = []model.Target{sampleTarget("R683", "MG724CH/A")}
				if err := store.Save(s); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := store.Load(); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("并发读写出错: %v", err)
	}
}

// TestSaveDoesNotMutateCallerTargets 钉住：Save 不得改写调用方持有的切片。
//
// Settings 是按值传给 Save 的，但 Targets 与调用方共用同一块底层数组，
// Normalize 一旦就地压缩就会把去重结果写回去：调用方持有 [A, A, B] 时数组被压成
// [A, B, B]，而它的切片长度仍是 3，看到的内容就此变了样。界面上
// 「改完设置 → Save → engine.SetTargets(s.Targets)」这条路径会因此多出一条重复行。
func TestSaveDoesNotMutateCallerTargets(t *testing.T) {
	store, _ := newStore(t)
	a := sampleTarget("R683", "MG724CH/A")
	b := sampleTarget("R409", "MG834CH/A")

	callerTargets := []model.Target{a, a, b}
	before := append([]model.Target(nil), callerTargets...)

	if err := store.Save(config.Settings{
		Locale:          "zh_CN",
		IntervalSeconds: config.DefaultIntervalSeconds,
		Targets:         callerTargets,
	}); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	if !reflect.DeepEqual(callerTargets, before) {
		t.Fatalf("Save 改写了调用方的切片:\n after = %+v\nbefore = %+v", callerTargets, before)
	}
}

// TestNormalizeDoesNotMutateSharedTargets 从 Normalize 这一层钉住同一件事。
//
// Save 只是最常见的触发路径；任何人拿到一份 Settings 的副本再 Normalize，
// 都不该影响原主。去重必须发生在新切片上，而不是就地压缩共用的底层数组。
func TestNormalizeDoesNotMutateSharedTargets(t *testing.T) {
	a := sampleTarget("R683", "MG724CH/A")
	b := sampleTarget("R409", "MG834CH/A")

	original := config.Settings{
		Locale:          "zh_CN",
		IntervalSeconds: config.DefaultIntervalSeconds,
		Targets:         []model.Target{a, a, b},
	}
	before := append([]model.Target(nil), original.Targets...)

	copied := original
	copied.Normalize()

	if !reflect.DeepEqual(original.Targets, before) {
		t.Fatalf("Normalize 改写了共用底层数组:\n after = %+v\nbefore = %+v", original.Targets, before)
	}
	if want := []model.Target{a, b}; !reflect.DeepEqual(copied.Targets, want) {
		t.Fatalf("去重结果 = %+v, 期望 %+v", copied.Targets, want)
	}
}

// paddedSettings 返回一份长度恰好为 size 字节的合法设置 JSON。
//
// 用尾部空白凑长度：这里只关心 Load 对字节数的判定，JSON 的内容多复杂无关紧要。
func paddedSettings(t *testing.T, size int) []byte {
	t.Helper()
	body := `{"locale":"ja_JP","interval_seconds":45}`
	if size < len(body) {
		t.Fatalf("size = %d 小于最短合法 JSON 的 %d 字节", size, len(body))
	}
	return []byte(body + strings.Repeat("\n", size-len(body)))
}

// TestLoadRejectsOversizedFile 验证超大的设置文件被拒绝，而不是整个读进内存。
//
// 磁盘损坏、别的程序写错文件、或有人刻意构造一个几 GB 的 settings.json 时，
// 原先的 os.ReadFile 会在任何错误处理之前就把内存吃光，进程直接被杀，
// 用户连提示都看不到。现在必须返回错误并退回默认设置。
func TestLoadRejectsOversizedFile(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"刚好超过上限一个字节", config.MaxSettingsBytes + 1},
		{"远超上限", config.MaxSettingsBytes * 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newStore(t)
			if err := os.WriteFile(store.Path(), paddedSettings(t, tc.size), 0o600); err != nil {
				t.Fatalf("写入超大配置失败: %v", err)
			}

			got, err := store.Load()
			if err == nil {
				t.Fatal("文件超过上限时 Load 没有返回错误")
			}
			if !errors.Is(err, config.ErrTooLarge) {
				t.Errorf("err = %v, 期望能被 errors.Is 识别为 config.ErrTooLarge", err)
			}
			if !reflect.DeepEqual(got, config.Default()) {
				t.Errorf("Load = %+v, 期望回退到 Default()", got)
			}
		})
	}
}

// TestLoadHugeSparseFileDoesNotReadItAll 用一个远大于内存预算的稀疏文件确认上限真的生效。
//
// os.Truncate 造出的稀疏文件几乎不占磁盘，但 os.ReadFile 会老老实实分配这么多内存。
// 只要 Load 仍然按上限截断，这个用例就能瞬间返回 ErrTooLarge。
func TestLoadHugeSparseFileDoesNotReadItAll(t *testing.T) {
	store, _ := newStore(t)
	f, err := os.Create(store.Path())
	if err != nil {
		t.Fatalf("创建配置文件失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭配置文件失败: %v", err)
	}
	const size = 2 << 30 // 2 GiB
	if err := os.Truncate(store.Path(), size); err != nil {
		t.Skipf("当前文件系统不支持稀疏文件，跳过: %v", err)
	}

	got, err := store.Load()
	if !errors.Is(err, config.ErrTooLarge) {
		t.Fatalf("err = %v, 期望 config.ErrTooLarge", err)
	}
	if !reflect.DeepEqual(got, config.Default()) {
		t.Errorf("Load = %+v, 期望回退到 Default()", got)
	}
}

// TestLoadAcceptsFileAtSizeLimit 验证上限附近的正常文件仍然读得出来。
//
// 上限是防御异常文件的，不能顺手把边界上的正常配置也判死。
func TestLoadAcceptsFileAtSizeLimit(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"正好等于上限", config.MaxSettingsBytes},
		{"比上限少一个字节", config.MaxSettingsBytes - 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newStore(t)
			if err := os.WriteFile(store.Path(), paddedSettings(t, tc.size), 0o600); err != nil {
				t.Fatalf("写入配置失败: %v", err)
			}

			got, err := store.Load()
			if err != nil {
				t.Fatalf("上限之内的文件 Load 失败: %v", err)
			}
			if got.Locale != "ja_JP" {
				t.Errorf("Locale = %q, 期望 ja_JP", got.Locale)
			}
			if got.IntervalSeconds != 45 {
				t.Errorf("IntervalSeconds = %d, 期望 45", got.IntervalSeconds)
			}
		})
	}
}

// TestPreserveCorruptedAfterOversizedLoad 验证超大文件走的是与损坏文件相同的善后流程。
//
// main.go 在 Load 失败时会放弃写盘并把原文件改名留存。「文件超大」同样属于
// 「读不到旧配置」，绝不能被当成「用户没有配置」而被一份默认配置覆盖掉 ——
// 那会让用户的整个监控列表消失且无从找回。
func TestPreserveCorruptedAfterOversizedLoad(t *testing.T) {
	store, dir := newStore(t)
	if err := os.WriteFile(store.Path(), paddedSettings(t, config.MaxSettingsBytes+1), 0o600); err != nil {
		t.Fatalf("写入超大配置失败: %v", err)
	}

	if _, err := store.Load(); !errors.Is(err, config.ErrTooLarge) {
		t.Fatalf("err = %v, 期望 config.ErrTooLarge", err)
	}

	backup, err := store.PreserveCorrupted()
	if err != nil {
		t.Fatalf("PreserveCorrupted 失败: %v", err)
	}
	if backup == "" {
		t.Fatal("PreserveCorrupted 没有留下备份")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Errorf("原设置文件仍在原处: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("目录里有 %d 个文件, 期望只剩备份", len(entries))
	}
}
