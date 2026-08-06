package catalog

// 这些测试全部离线运行：商品与门店数据来自 //go:embed 的离线快照，
// 在线刷新用假的 ProductFetcher，绝不请求 apple.com。
//
// 测试重点放在两类容易悄悄退化的行为上：
//  1. 查不到东西时必须返回 (零值, false) 或 error，而不是 panic
//     —— 上游 services/product.go:19 与 services/store.go:22、:74 正是在这里崩的；
//  2. 部分刷新失败时必须保留成功的那部分，并且聚合错误仍能被 errors.Is 拆开，
//     调用方才有机会区分「被拦截」和「解析不了」。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// errFetchFailed 代表一类「底层抓取失败」的哨兵错误，用来验证聚合错误没有把
// 原始错误类型丢掉（丢掉之后调用方就没法判断该不该重试了）。
var errFetchFailed = errors.New("模拟的抓取失败")

// stubResult 是 stubFetcher 对某个机型的预设应答。
type stubResult struct {
	products []model.Product
	err      error
}

// stubFetcher 是 ProductFetcher 的假实现。
//
// 它本身要并发安全：并发测试会从多个 goroutine 同时调 RefreshProducts。
type stubFetcher struct {
	mu       sync.Mutex
	calls    []string
	byFamily map[string]stubResult
}

func (f *stubFetcher) FetchProducts(ctx context.Context, region model.Region, family string) ([]model.Product, error) {
	f.mu.Lock()
	f.calls = append(f.calls, family)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, ok := f.byFamily[family]
	if !ok {
		return nil, fmt.Errorf("stub 没有为机型 %s 准备应答", family)
	}
	// 返回副本，避免被测代码就地改动测试数据后影响后续断言。
	return append([]model.Product(nil), r.products...), r.err
}

func (f *stubFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// testRegion 借用 zh_CN 这个 locale，好让「在线结果」与「内嵌快照」可以直接对比：
// 只要 Products 返回的条数不是内嵌的那 43 条，就说明在线结果确实生效了。
func testRegion() model.Region {
	return model.Region{
		Title:    "测试地区",
		Locale:   "zh_CN",
		BaseURL:  "https://example.invalid",
		Families: []string{"iphone-17", "iphone-17-pro", "iphone-air"},
	}
}

func stubProduct(part, family, capacity, color string) model.Product {
	return model.Product{
		PartNumber: part,
		Family:     family,
		Capacity:   capacity,
		Color:      color,
		Title:      strings.TrimSpace(apple.FamilyDisplayName(family) + " " + apple.NormalizeCapacity(capacity) + " " + color),
	}
}

func partNumbers(products []model.Product) []string {
	out := make([]string, 0, len(products))
	for _, p := range products {
		out = append(out, p.PartNumber)
	}
	return out
}

// ---------------------------------------------------------------------------
// 内嵌快照
// ---------------------------------------------------------------------------

// TestEmbeddedCatalogCoversEveryRegion 保证 model.Regions 里的每个地区都能
// 从内嵌数据里读出商品和门店。少一个文件就意味着那个地区的用户打开程序后
// 下拉框是空的，而这在编译期没有任何提示 —— 只有这个测试能挡住。
func TestEmbeddedCatalogCoversEveryRegion(t *testing.T) {
	if len(model.Regions) != 7 {
		t.Fatalf("地区数量为 %d，与预期的 7 个不符；请同步更新内嵌数据和本测试", len(model.Regions))
	}

	for _, region := range model.Regions {
		t.Run(region.Locale, func(t *testing.T) {
			t.Parallel()
			c := New()

			products, err := c.Products(region.Locale)
			if err != nil {
				t.Fatalf("Products(%s) 返回错误: %v", region.Locale, err)
			}
			if len(products) == 0 {
				t.Fatalf("Products(%s) 返回空列表", region.Locale)
			}

			seenPart := map[string]bool{}
			seenTitle := map[string]bool{}
			for _, p := range products {
				if p.PartNumber == "" {
					t.Errorf("%s: 存在零件号为空的商品 %+v", region.Locale, p)
				}
				if p.Title == "" {
					t.Errorf("%s: 零件号 %s 的展示名为空", region.Locale, p.PartNumber)
				}
				if p.Family == "" {
					t.Errorf("%s: 零件号 %s 没有机型标识", region.Locale, p.PartNumber)
				}
				if seenPart[p.PartNumber] {
					t.Errorf("%s: 零件号 %s 出现多次，去重失效", region.Locale, p.PartNumber)
				}
				seenPart[p.PartNumber] = true
				// 展示名是界面上的唯一键（ProductByTitle 按它反查），重名会让
				// 用户选中的和实际监控的不是同一台机器。
				if seenTitle[p.Title] {
					t.Errorf("%s: 展示名 %q 出现多次", region.Locale, p.Title)
				}
				seenTitle[p.Title] = true
			}

			titles, err := c.ProductTitles(region.Locale)
			if err != nil {
				t.Fatalf("ProductTitles(%s) 返回错误: %v", region.Locale, err)
			}
			if len(titles) != len(products) {
				t.Errorf("ProductTitles(%s) 返回 %d 条，Products 返回 %d 条", region.Locale, len(titles), len(products))
			}

			stores, err := c.Stores(region.Locale)
			if err != nil {
				t.Fatalf("Stores(%s) 返回错误: %v", region.Locale, err)
			}
			if len(stores) == 0 {
				t.Fatalf("Stores(%s) 返回空列表", region.Locale)
			}
			seenNumber := map[string]bool{}
			for _, s := range stores {
				if s.Number == "" || s.Name == "" || s.Title == "" {
					t.Errorf("%s: 门店字段不完整: %+v", region.Locale, s)
				}
				if seenNumber[s.Number] {
					t.Errorf("%s: 门店编号 %s 出现多次，去重失效", region.Locale, s.Number)
				}
				seenNumber[s.Number] = true
			}

			storeTitles, err := c.StoreTitles(region.Locale)
			if err != nil {
				t.Fatalf("StoreTitles(%s) 返回错误: %v", region.Locale, err)
			}
			if len(storeTitles) != len(stores) {
				t.Errorf("StoreTitles(%s) 返回 %d 条，Stores 返回 %d 条", region.Locale, len(storeTitles), len(stores))
			}
		})
	}
}

// TestEmbeddedProductsSortedByFamilyThenCapacity 验证下拉框里的排列可预期：
// 先按机型，再按容量从小到大。直接按字符串排会把 1tb 排在 256gb 前面。
func TestEmbeddedProductsSortedByFamilyThenCapacity(t *testing.T) {
	for _, region := range model.Regions {
		t.Run(region.Locale, func(t *testing.T) {
			t.Parallel()
			products, err := New().Products(region.Locale)
			if err != nil {
				t.Fatalf("Products(%s) 返回错误: %v", region.Locale, err)
			}
			for i := 1; i < len(products); i++ {
				a, b := products[i-1], products[i]
				if a.Family != b.Family {
					if a.Family > b.Family {
						t.Fatalf("机型顺序错乱: %q 排在 %q 之前", a.Family, b.Family)
					}
					continue
				}
				ra, rb := capacityRank(a.Capacity), capacityRank(b.Capacity)
				if ra > rb {
					t.Fatalf("同机型 %s 下容量顺序错乱: %q(%d) 排在 %q(%d) 之前",
						a.Family, a.Capacity, ra, b.Capacity, rb)
				}
				if ra == rb && a.Title > b.Title {
					t.Fatalf("同机型同容量下展示名顺序错乱: %q 排在 %q 之前", a.Title, b.Title)
				}
			}
		})
	}
}

func TestCapacityRank(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"256gb", 256},
		{"512GB", 512},
		{" 1tb ", 1024},
		{"2tb", 2048},
		{"", 0},
		{"unknown", 0},
		{"gb", 0},    // 去掉单位后不是数字
		{"1.5tb", 0}, // 小数解析不了，退化成 0 而不是 panic
	}
	for _, tc := range cases {
		if got := capacityRank(tc.in); got != tc.want {
			t.Errorf("capacityRank(%q) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}

// TestJapanCarrierDuplicatesCollapse 针对日本站的特殊形状：同一台机器按
// 运营商（SOFTBANK_*）和无锁版（UNLOCKED_JP）各列了一遍，零件号完全相同。
// 查库存只认零件号，界面上出现一模一样的两行毫无意义。
//
// 测试先从内嵌原始 JSON 里确认「确实存在重复」，再断言解析结果已经去重，
// 这样即便将来 Apple 不再按运营商拆行，测试也会立刻提示前提已变。
func TestJapanCarrierDuplicatesCollapse(t *testing.T) {
	raw, err := embedded.ReadFile("data/products_ja_JP.json")
	if err != nil {
		t.Fatalf("读取内嵌 ja_JP 数据失败: %v", err)
	}

	// 用测试自己的结构解析，不复用被测代码的 productSelection。
	var groups []struct {
		Products []struct {
			PartNumber        string `json:"partNumber"`
			CarrierModel      string `json:"carrierModel"`
			DimensionCapacity string `json:"dimensionCapacity"`
			DimensionColor    string `json:"dimensionColor"`
		} `json:"products"`
	}
	if err := json.Unmarshal(raw, &groups); err != nil {
		t.Fatalf("解析内嵌 ja_JP 数据失败: %v", err)
	}

	type variant struct{ carrier, capacity, color string }
	rawByPart := map[string][]variant{}
	rawTotal := 0
	for _, g := range groups {
		for _, p := range g.Products {
			rawTotal++
			rawByPart[p.PartNumber] = append(rawByPart[p.PartNumber],
				variant{p.CarrierModel, p.DimensionCapacity, p.DimensionColor})
		}
	}

	duplicated := 0
	for part, variants := range rawByPart {
		if len(variants) < 2 {
			continue
		}
		duplicated++
		carriers := map[string]bool{}
		for _, v := range variants {
			carriers[v.carrier] = true
			// 同一零件号的两行必须只是运营商不同；容量或颜色不同就说明
			// 「按零件号去重」这个前提本身站不住。
			if v.capacity != variants[0].capacity || v.color != variants[0].color {
				t.Errorf("零件号 %s 的重复行规格不一致: %+v", part, variants)
			}
		}
		if len(carriers) < 2 {
			t.Errorf("零件号 %s 重复了 %d 次但运营商只有 %d 种: %+v",
				part, len(variants), len(carriers), variants)
		}
	}
	if duplicated == 0 {
		t.Fatalf("内嵌 ja_JP 数据里没有任何重复零件号，本测试失去意义；请确认数据是否已更新")
	}
	if rawTotal <= len(rawByPart) {
		t.Fatalf("原始条目 %d 条、去重后 %d 条，数据里没有重复", rawTotal, len(rawByPart))
	}

	products, err := New().Products("ja_JP")
	if err != nil {
		t.Fatalf("Products(ja_JP) 返回错误: %v", err)
	}
	if len(products) != len(rawByPart) {
		t.Fatalf("Products(ja_JP) 返回 %d 条，原始数据去重后应为 %d 条", len(products), len(rawByPart))
	}
	seen := map[string]bool{}
	for _, p := range products {
		if seen[p.PartNumber] {
			t.Errorf("零件号 %s 在结果中出现多次", p.PartNumber)
		}
		seen[p.PartNumber] = true
		if _, ok := rawByPart[p.PartNumber]; !ok {
			t.Errorf("结果中的零件号 %s 不在原始数据里", p.PartNumber)
		}
	}
}

// ---------------------------------------------------------------------------
// 门店 Title 的构造
// ---------------------------------------------------------------------------

// snapStore / snapRegion 是测试自行解析 stores.json 用的结构。刻意不复用被测
// 代码里的 rawStore/rawStoreRegion：共用一份 tag 的话，字段名写错时测试会跟着
// 一起错，等于没测。
type snapStore struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address struct {
		City      string `json:"city"`
		StateName string `json:"stateName"`
	} `json:"address"`
}

type snapRegion struct {
	Locale    string `json:"locale"`
	HasStates bool   `json:"hasStates"`
	States    []struct {
		Name   string      `json:"name"`
		Stores []snapStore `json:"store"`
	} `json:"state"`
	Stores []snapStore `json:"store"`
}

func readStoreSnapshot(t *testing.T) []snapRegion {
	t.Helper()
	raw, err := embedded.ReadFile("data/stores.json")
	if err != nil {
		t.Fatalf("读取内嵌门店数据失败: %v", err)
	}
	var regions []snapRegion
	if err := json.Unmarshal(raw, &regions); err != nil {
		t.Fatalf("解析内嵌门店数据失败: %v", err)
	}
	if len(regions) == 0 {
		t.Fatal("内嵌门店数据为空")
	}
	return regions
}

// TestStoreTitleUsesStateNameWhenRegionHasStates 用写死的期望值验证 Title 规则。
//
// 挑的都是「city 与 stateName 不同」的门店，这样断言才有区分度：如果实现
// 误用了 city，Tokyo-Marunouchi 会变成 Chiyoda-ku-Marunouchi，一眼就能看出来。
func TestStoreTitleUsesStateNameWhenRegionHasStates(t *testing.T) {
	wantTitles := map[string]map[string]string{
		// hasStates=true：用 stateName，而不是门店所在的区/市。
		"ja_JP": {
			"R718": "Tokyo-Marunouchi", // city 是 Chiyoda-ku
			"R119": "Tokyo-Shibuya",    // city 是 Shibuya-ku
			"R091": "Osaka-Shinsaibashi",
		},
		"en_AU": {
			"R440": "New South Wales-Penrith", // city 是 Penrith
			"R483": "Australian Capital Territory-Canberra",
		},
		"zh_CN": {
			"R683": "上海-环球港",
		},
		// hasStates=false：没有 stateName，用 city。
		"zh_HK": {
			"R428": "香港-ifc mall",
		},
		"en_SG": {
			"R736": "Singapore-Jewel Changi Airport",
		},
	}

	// 先确认「city 与 stateName 不同」这个前提在数据里成立，否则上面的断言
	// 换成 city 也能通过，测不出任何东西。
	snapshot := readStoreSnapshot(t)
	discriminating := 0
	for _, r := range snapshot {
		if !r.HasStates {
			continue
		}
		for _, st := range r.States {
			for _, s := range st.Stores {
				if s.Address.StateName != "" && s.Address.City != "" && s.Address.StateName != s.Address.City {
					discriminating++
				}
			}
		}
	}
	if discriminating == 0 {
		t.Fatal("数据里没有任何 city != stateName 的门店，Title 规则无法被区分验证")
	}

	c := New()
	for locale, want := range wantTitles {
		stores, err := c.Stores(locale)
		if err != nil {
			t.Fatalf("Stores(%s) 返回错误: %v", locale, err)
		}
		byNumber := map[string]model.Store{}
		for _, s := range stores {
			byNumber[s.Number] = s
		}
		for number, title := range want {
			got, ok := byNumber[number]
			if !ok {
				t.Errorf("%s: 找不到门店 %s", locale, number)
				continue
			}
			if got.Title != title {
				t.Errorf("%s 门店 %s 的 Title = %q，期望 %q", locale, number, got.Title, title)
			}
			// City 字段要与 Title 前缀一致，界面上按城市分组才不会错位。
			if got.Title != got.City+"-"+got.Name {
				t.Errorf("%s 门店 %s 的 Title(%q) 不等于 City(%q)+\"-\"+Name(%q)",
					locale, number, got.Title, got.City, got.Name)
			}
		}
	}
}

// TestStoresDeduplicateAndSortStably 覆盖去重与顺序：
// 结果按门店编号去重，按 (Title, Number) 升序排列，且多次调用完全一致。
func TestStoresDeduplicateAndSortStably(t *testing.T) {
	snapshot := readStoreSnapshot(t)

	// 独立统计每个地区的唯一门店编号数，用来确认既没漏也没重。
	wantCount := map[string]int{}
	for _, r := range snapshot {
		if r.Locale == "" {
			continue
		}
		ids := map[string]bool{}
		for _, st := range r.States {
			for _, s := range st.Stores {
				if s.ID != "" {
					ids[s.ID] = true
				}
			}
		}
		for _, s := range r.Stores {
			if s.ID != "" {
				ids[s.ID] = true
			}
		}
		wantCount[r.Locale] = len(ids)
	}

	first := New()
	second := New()
	for locale, want := range wantCount {
		a, err := first.Stores(locale)
		if err != nil {
			t.Fatalf("Stores(%s) 返回错误: %v", locale, err)
		}
		if len(a) != want {
			t.Errorf("%s: 返回 %d 家门店，原始数据去重后应为 %d 家", locale, len(a), want)
		}

		for i := 1; i < len(a); i++ {
			prev, cur := a[i-1], a[i]
			if prev.Title > cur.Title || (prev.Title == cur.Title && prev.Number >= cur.Number) {
				t.Errorf("%s: 排序错乱，%+v 排在 %+v 之前", locale, prev, cur)
			}
		}

		// 同一实例重复调用、以及另一个全新实例，顺序都必须一致。
		b, err := first.Stores(locale)
		if err != nil {
			t.Fatalf("Stores(%s) 第二次调用返回错误: %v", locale, err)
		}
		cFresh, err := second.Stores(locale)
		if err != nil {
			t.Fatalf("新实例 Stores(%s) 返回错误: %v", locale, err)
		}
		for i := range a {
			if a[i] != b[i] || a[i] != cFresh[i] {
				t.Fatalf("%s: 第 %d 家门店在多次调用间不一致: %+v / %+v / %+v", locale, i, a[i], b[i], cFresh[i])
			}
		}
	}
}

// TestReturnedSlicesAreCopies 保证调用方拿到的是副本。
// 界面代码会对返回值做排序、截断之类的操作，直接暴露内部切片的话，
// 一次界面刷新就能把整份目录改坏。
func TestReturnedSlicesAreCopies(t *testing.T) {
	c := New()

	products, err := c.Products("zh_CN")
	if err != nil {
		t.Fatalf("Products 返回错误: %v", err)
	}
	if len(products) == 0 {
		t.Fatal("Products 返回空列表")
	}
	original := products[0]
	products[0] = model.Product{PartNumber: "TAMPERED", Title: "TAMPERED"}

	again, err := c.Products("zh_CN")
	if err != nil {
		t.Fatalf("Products 第二次返回错误: %v", err)
	}
	if again[0] != original {
		t.Errorf("修改返回值影响了目录内部状态: %+v", again[0])
	}

	stores, err := c.Stores("zh_CN")
	if err != nil {
		t.Fatalf("Stores 返回错误: %v", err)
	}
	originalStore := stores[0]
	stores[0] = model.Store{Number: "TAMPERED"}

	storesAgain, err := c.Stores("zh_CN")
	if err != nil {
		t.Fatalf("Stores 第二次返回错误: %v", err)
	}
	if storesAgain[0] != originalStore {
		t.Errorf("修改返回值影响了门店内部状态: %+v", storesAgain[0])
	}
}

// ---------------------------------------------------------------------------
// 查找：找不到时返回 (零值, false)，绝不 panic
// ---------------------------------------------------------------------------

// TestByTitleLookups 覆盖 ProductByTitle / StoreByTitle 的正反两路。
//
// 上游 services/product.go:19 与 services/store.go:74 用
// funk.Find(...).(model.Product) 做类型断言，找不到时是对 nil 接口做断言，
// 直接 panic 掉整个程序 —— 用户选完商品后赶上一次目录刷新就足以触发。
// 这里必须是 (零值, false)。
func TestByTitleLookups(t *testing.T) {
	c := New()

	products, err := c.Products("zh_CN")
	if err != nil {
		t.Fatalf("Products 返回错误: %v", err)
	}
	want := products[len(products)-1]
	got, ok := c.ProductByTitle("zh_CN", want.Title)
	if !ok {
		t.Fatalf("ProductByTitle(zh_CN, %q) 没找到", want.Title)
	}
	if got != want {
		t.Errorf("ProductByTitle 返回 %+v，期望 %+v", got, want)
	}

	stores, err := c.Stores("zh_CN")
	if err != nil {
		t.Fatalf("Stores 返回错误: %v", err)
	}
	wantStore := stores[0]
	gotStore, ok := c.StoreByTitle("zh_CN", wantStore.Title)
	if !ok {
		t.Fatalf("StoreByTitle(zh_CN, %q) 没找到", wantStore.Title)
	}
	if gotStore != wantStore {
		t.Errorf("StoreByTitle 返回 %+v，期望 %+v", gotStore, wantStore)
	}

	missing := []struct {
		name, locale, title string
	}{
		{"展示名不存在", "zh_CN", "iPhone 99 Pro Max 8TB 幻想色"},
		{"展示名为空", "zh_CN", ""},
		{"大小写不匹配", "zh_CN", strings.ToUpper(want.Title)},
		{"首尾有空格", "zh_CN", " " + want.Title + " "},
		{"地区不存在", "xx_YY", want.Title},
		{"地区为空", "", want.Title},
	}
	for _, tc := range missing {
		t.Run("ProductByTitle/"+tc.name, func(t *testing.T) {
			got, ok := c.ProductByTitle(tc.locale, tc.title)
			if ok {
				t.Fatalf("ProductByTitle(%q, %q) 意外命中: %+v", tc.locale, tc.title, got)
			}
			if got != (model.Product{}) {
				t.Errorf("未命中时应返回零值，实际 %+v", got)
			}
		})
		t.Run("StoreByTitle/"+tc.name, func(t *testing.T) {
			got, ok := c.StoreByTitle(tc.locale, tc.title)
			if ok {
				t.Fatalf("StoreByTitle(%q, %q) 意外命中: %+v", tc.locale, tc.title, got)
			}
			if got != (model.Store{}) {
				t.Errorf("未命中时应返回零值，实际 %+v", got)
			}
		})
	}
}

// TestUnknownLocaleReturnsErrorNotPanic 覆盖未知地区。
//
// 内嵌数据是按 locale 命名的文件，配置文件里存着上一版写下的 locale、
// 而新版删掉了这个地区，就会走到这条路径上。必须是 error，不能是 panic
// （对应上游 services/store.go:22 读不到文件直接 panic 的写法）。
func TestUnknownLocaleReturnsErrorNotPanic(t *testing.T) {
	locales := []string{"xx_YY", "", "zh", "../stores", "data/../products_zh_CN"}
	c := New()

	for _, locale := range locales {
		t.Run(fmt.Sprintf("%q", locale), func(t *testing.T) {
			if products, err := c.Products(locale); err == nil {
				t.Errorf("Products(%q) 应当返回错误，实际返回 %d 条", locale, len(products))
			}
			if titles, err := c.ProductTitles(locale); err == nil {
				t.Errorf("ProductTitles(%q) 应当返回错误，实际返回 %d 条", locale, len(titles))
			}
			if stores, err := c.Stores(locale); err == nil {
				t.Errorf("Stores(%q) 应当返回错误，实际返回 %d 条", locale, len(stores))
			}
			if titles, err := c.StoreTitles(locale); err == nil {
				t.Errorf("StoreTitles(%q) 应当返回错误，实际返回 %d 条", locale, len(titles))
			}
		})
	}

	// 未知地区的失败不该污染已知地区：错误不能被缓存成「这个 Catalog 废了」。
	if products, err := c.Products("zh_CN"); err != nil || len(products) == 0 {
		t.Errorf("查询未知地区之后 Products(zh_CN) 失效: %d 条, err=%v", len(products), err)
	}
	if stores, err := c.Stores("zh_CN"); err != nil || len(stores) == 0 {
		t.Errorf("查询未知地区之后 Stores(zh_CN) 失效: %d 家, err=%v", len(stores), err)
	}
}

// ---------------------------------------------------------------------------
// 在线刷新
// ---------------------------------------------------------------------------

// TestRefreshProductsKeepsSuccessfulFamilies 是这个包最重要的一条：
// 一个机型抓不到，不该连带其他机型一起选不了。
//
// 同时验证聚合错误没有把底层错误类型吃掉 —— watcher 层要靠 errors.Is 区分
// ErrBlocked（被拦截，需要退避）和别的错误，包成一个纯字符串就没法判断了。
func TestRefreshProductsKeepsSuccessfulFamilies(t *testing.T) {
	region := testRegion()
	ok17a := stubProduct("MTEST1/A", "iphone17", "256gb", "black")
	ok17b := stubProduct("MTEST2/A", "iphone17", "512gb", "black")

	f := &stubFetcher{byFamily: map[string]stubResult{
		"iphone-17":     {products: []model.Product{ok17a, ok17b}},
		"iphone-17-pro": {err: fmt.Errorf("购买页取不到: %w", apple.ErrBlocked)},
		"iphone-air":    {err: errFetchFailed},
	}}

	c := New()
	err := c.RefreshProducts(context.Background(), region, f)
	if err == nil {
		t.Fatal("两个机型失败时 RefreshProducts 应当返回错误")
	}
	if !errors.Is(err, apple.ErrBlocked) {
		t.Errorf("聚合错误丢失了 ErrBlocked: %v", err)
	}
	if !errors.Is(err, errFetchFailed) {
		t.Errorf("聚合错误丢失了底层哨兵错误: %v", err)
	}
	if !strings.Contains(err.Error(), "2/3") {
		t.Errorf("错误信息未说明失败比例，实际: %v", err)
	}
	if n := f.callCount(); n != len(region.Families) {
		t.Errorf("抓取器被调用 %d 次，期望 %d 次（失败后不应提前退出）", n, len(region.Families))
	}

	// 成功的那部分必须落地：Products 现在应当返回在线结果（2 条），
	// 而不是内嵌快照的 43 条。
	products, perr := c.Products(region.Locale)
	if perr != nil {
		t.Fatalf("Products 返回错误: %v", perr)
	}
	if got, want := partNumbers(products), []string{ok17a.PartNumber, ok17b.PartNumber}; !equalStrings(got, want) {
		t.Fatalf("Products 返回 %v，期望仅保留成功的 %v", got, want)
	}

	// 查找也要跟着走新目录。
	if _, ok := c.ProductByTitle(region.Locale, ok17a.Title); !ok {
		t.Errorf("刷新后 ProductByTitle 找不到 %q", ok17a.Title)
	}
	if _, ok := c.ProductByTitle(region.Locale, "iPhone 17 256GB 黑色"); ok {
		t.Errorf("刷新后仍能查到已被替换掉的内嵌商品")
	}
}

// TestRefreshProductsAllFamiliesFailKeepsPreviousData 验证全军覆没时的行为：
// 不覆盖已有目录，界面上仍然有东西可选，只是拿不到新数据。
func TestRefreshProductsAllFamiliesFailKeepsPreviousData(t *testing.T) {
	region := testRegion()

	// 第一步：全部失败，应当回退到内嵌快照。
	allFail := &stubFetcher{byFamily: map[string]stubResult{
		"iphone-17":     {err: errFetchFailed},
		"iphone-17-pro": {err: errFetchFailed},
		"iphone-air":    {err: apple.ErrUnexpectedSchema},
	}}
	c := New()
	if err := c.RefreshProducts(context.Background(), region, allFail); err == nil {
		t.Fatal("全部机型失败时应当返回错误")
	}
	offline, err := c.Products(region.Locale)
	if err != nil {
		t.Fatalf("Products 返回错误: %v", err)
	}
	if len(offline) == 0 {
		t.Fatal("刷新全失败后应当回退到内嵌快照，实际为空")
	}

	// 第二步：一次成功的刷新。
	fresh := stubProduct("MFRESH1/A", "iphone17", "256gb", "black")
	good := &stubFetcher{byFamily: map[string]stubResult{
		"iphone-17":     {products: []model.Product{fresh}},
		"iphone-17-pro": {products: nil},
		"iphone-air":    {products: nil},
	}}
	if err := c.RefreshProducts(context.Background(), region, good); err != nil {
		t.Fatalf("全部成功时不应返回错误: %v", err)
	}

	// 第三步：再次全部失败，之前刷到的结果必须还在。
	if err := c.RefreshProducts(context.Background(), region, allFail); err == nil {
		t.Fatal("全部机型失败时应当返回错误")
	}
	after, err := c.Products(region.Locale)
	if err != nil {
		t.Fatalf("Products 返回错误: %v", err)
	}
	if got, want := partNumbers(after), []string{fresh.PartNumber}; !equalStrings(got, want) {
		t.Errorf("刷新失败后目录变成 %v，期望保留上一次成功的 %v", got, want)
	}
}

// TestRefreshProductsMergesAndDeduplicates 覆盖合并逻辑：
// 跨机型页的重复零件号只留一条，零件号为空的条目丢弃，结果按统一规则排序。
func TestRefreshProductsMergesAndDeduplicates(t *testing.T) {
	region := testRegion()
	shared := stubProduct("MSHARED/A", "iphone17pro", "1tb", "silver")

	f := &stubFetcher{byFamily: map[string]stubResult{
		"iphone-17": {products: []model.Product{
			stubProduct("MB/A", "iphone17", "512gb", "black"),
			stubProduct("MA/A", "iphone17", "256gb", "black"),
			{PartNumber: "", Family: "iphone17", Title: "没有零件号"},
		}},
		// pro 与 pro max 共页，同一零件号会在两个机型页里各出现一次。
		"iphone-17-pro": {products: []model.Product{shared, shared}},
		"iphone-air":    {products: []model.Product{shared, stubProduct("MC/A", "iphoneair", "256gb", "black")}},
	}}

	c := New()
	if err := c.RefreshProducts(context.Background(), region, f); err != nil {
		t.Fatalf("RefreshProducts 返回错误: %v", err)
	}

	products, err := c.Products(region.Locale)
	if err != nil {
		t.Fatalf("Products 返回错误: %v", err)
	}
	want := []string{"MA/A", "MB/A", "MSHARED/A", "MC/A"} // iphone17(256,512) < iphone17pro < iphoneair
	if got := partNumbers(products); !equalStrings(got, want) {
		t.Fatalf("合并结果为 %v，期望 %v", got, want)
	}
}

func TestRefreshProductsRejectsBadArguments(t *testing.T) {
	c := New()

	if err := c.RefreshProducts(context.Background(), testRegion(), nil); err == nil {
		t.Error("抓取器为 nil 时应当返回错误")
	}

	empty := testRegion()
	empty.Families = nil
	if err := c.RefreshProducts(context.Background(), empty, &stubFetcher{}); err == nil {
		t.Error("地区没有配置机型时应当返回错误")
	}

	// 参数不合法不该动到已有目录。
	products, err := c.Products("zh_CN")
	if err != nil || len(products) == 0 {
		t.Errorf("非法参数调用之后 Products(zh_CN) 失效: %d 条, err=%v", len(products), err)
	}
}

// TestRefreshProductsContextCancelled 验证取消能被识别：
// 上层用 context 控制退出，聚合错误必须让 errors.Is(err, context.Canceled) 成立，
// 否则会被误当成「Apple 出问题了」而触发无谓的退避重试。
func TestRefreshProductsContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &stubFetcher{byFamily: map[string]stubResult{
		"iphone-17": {products: []model.Product{stubProduct("MX/A", "iphone17", "256gb", "black")}},
	}}
	c := New()
	err := c.RefreshProducts(ctx, testRegion(), f)
	if err == nil {
		t.Fatal("context 已取消时应当返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("错误中不含 context.Canceled: %v", err)
	}
	if n := f.callCount(); n != 0 {
		t.Errorf("context 已取消时不应发起抓取，实际调用了 %d 次", n)
	}
	// 取消不该破坏内嵌快照的可用性。
	if products, perr := c.Products("zh_CN"); perr != nil || len(products) == 0 {
		t.Errorf("取消刷新之后 Products(zh_CN) 失效: %d 条, err=%v", len(products), perr)
	}
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

// TestConcurrentAccessIsRaceFree 在 -race 下并发读写同一个 Catalog。
//
// 目录会被界面线程读、被刷新 goroutine 写。上游那种无锁共享 map 的写法
// （services/store.go:56 在后台写 s.stores，界面同时在读）在 Go 里会触发
// fatal error: concurrent map read and map write，recover 都捕获不到。
func TestConcurrentAccessIsRaceFree(t *testing.T) {
	c := New()
	region := testRegion()
	f := &stubFetcher{byFamily: map[string]stubResult{
		"iphone-17":     {products: []model.Product{stubProduct("MR1/A", "iphone17", "256gb", "black")}},
		"iphone-17-pro": {err: apple.ErrBlocked}, // 部分失败，让写入路径和错误路径都跑到
		"iphone-air":    {products: []model.Product{stubProduct("MR2/A", "iphoneair", "512gb", "black")}},
	}}

	locales := []string{"zh_CN", "ja_JP", "en_AU", "xx_YY"}

	var wg sync.WaitGroup
	const workers = 12
	const rounds = 25

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				locale := locales[(w+r)%len(locales)]
				known := locale != "xx_YY"

				switch (w + r) % 7 {
				case 0:
					products, err := c.Products(locale)
					if known && (err != nil || len(products) == 0) {
						t.Errorf("Products(%s) = %d 条, err=%v", locale, len(products), err)
					}
				case 1:
					stores, err := c.Stores(locale)
					if known && (err != nil || len(stores) == 0) {
						t.Errorf("Stores(%s) = %d 家, err=%v", locale, len(stores), err)
					}
				case 2:
					if _, err := c.ProductTitles(locale); known && err != nil {
						t.Errorf("ProductTitles(%s) 出错: %v", locale, err)
					}
				case 3:
					if _, err := c.StoreTitles(locale); known && err != nil {
						t.Errorf("StoreTitles(%s) 出错: %v", locale, err)
					}
				case 4:
					// 一定找不到，只验证不 panic、不返回垃圾。
					if p, ok := c.ProductByTitle(locale, "不存在的商品"); ok || p != (model.Product{}) {
						t.Errorf("ProductByTitle 意外命中: %+v", p)
					}
				case 5:
					if s, ok := c.StoreByTitle(locale, "不存在的门店"); ok || s != (model.Store{}) {
						t.Errorf("StoreByTitle 意外命中: %+v", s)
					}
				case 6:
					// 这个刷新总会带一个失败机型，错误可以忽略，重点是并发写。
					_ = c.RefreshProducts(context.Background(), region, f)
				}
			}
		}(w)
	}
	wg.Wait()

	// 并发跑完之后目录仍然自洽：zh_CN 应当是刷新后的在线结果。
	products, err := c.Products(region.Locale)
	if err != nil {
		t.Fatalf("并发结束后 Products 出错: %v", err)
	}
	if got, want := partNumbers(products), []string{"MR1/A", "MR2/A"}; !equalStrings(got, want) {
		t.Errorf("并发结束后 zh_CN 目录为 %v，期望 %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
