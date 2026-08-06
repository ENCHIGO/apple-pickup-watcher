// Package catalog 提供商品与门店目录。
//
// 目录有两个来源：内嵌的离线快照，以及运行时从 Apple 购买页抓来的最新数据。
// 内嵌快照保证程序在断网或被拦截时仍然可用，在线抓取保证新机发布当天就能盯，
// 不必等一个新版本。上游只有前者，且那份 JSON 要手工从浏览器里复制出来
// （上游 model/area.go:14-17），作者停更之后目录就永远停在旧机型上了。
package catalog

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// embedded 是随二进制一起分发的离线目录快照。
//
// 上游是运行时按相对路径去读工作目录下的 config/*.json（上游
// config/config.go 的 ReadConfigFile），打包成 macOS .app 之后工作目录
// 未必是仓库目录，读不到文件就在 services/store.go:22 直接 panic 崩掉。
// 用 //go:embed 把数据编进二进制，这个失败模式从根上就不存在了。
//
//go:embed data/*.json
var embedded embed.FS

// ProductFetcher 抽象出在线抓取能力。
//
// 声明成接口而不是直接依赖 *apple.Client，是为了在测试里能塞一个假实现，
// 不必真的去请求 Apple。
type ProductFetcher interface {
	FetchProducts(ctx context.Context, region model.Region, family string) ([]model.Product, error)
}

// Catalog 持有商品与门店目录，所有导出方法均并发安全。
type Catalog struct {
	// mu 保护下面全部字段。目录会被 UI 线程读、被刷新 goroutine 写，
	// 上游那种无锁共享 map 的写法（services/store.go:56 在后台写 s.stores，
	// 界面同时在读）在 Go 里会触发 fatal error: concurrent map read and
	// map write，且 recover 捕获不到。
	mu sync.RWMutex

	// online 是按地区缓存的在线刷新结果，优先于 offlineProducts。
	online map[string][]model.Product
	// offlineProducts 是内嵌快照的解析缓存，按地区惰性解析。
	offlineProducts map[string][]model.Product

	storesLoaded bool
	storesErr    error
	stores       map[string][]model.Store
}

// New 构造一个空的 Catalog，内嵌数据在首次访问时才解析。
func New() *Catalog {
	return &Catalog{
		online:          map[string][]model.Product{},
		offlineProducts: map[string][]model.Product{},
	}
}

// Products 返回某地区的全部商品。
//
// 有在线刷新结果时优先返回它，否则回退到内嵌快照。
func (c *Catalog) Products(locale string) ([]model.Product, error) {
	c.mu.RLock()
	cached, hit := c.online[locale]
	if !hit || len(cached) == 0 {
		cached, hit = c.offlineProducts[locale]
	}
	c.mu.RUnlock()
	if hit {
		return append([]model.Product(nil), cached...), nil
	}

	// 解析放在锁外做，避免几百 KB 的 JSON 反序列化把界面线程一起堵住。
	parsed, err := loadEmbeddedProducts(locale)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.offlineProducts[locale] = parsed
	// 解析期间可能刚好有一次刷新落地，此时应当以在线结果为准。
	if products, ok := c.online[locale]; ok && len(products) > 0 {
		parsed = products
	}
	return append([]model.Product(nil), parsed...), nil
}

// Stores 返回某地区的全部直营店。
//
// 门店变动远慢于商品，没有在线刷新，只用内嵌快照。
func (c *Catalog) Stores(locale string) ([]model.Store, error) {
	c.mu.RLock()
	loaded := c.storesLoaded
	c.mu.RUnlock()

	if !loaded {
		parsed, err := loadEmbeddedStores()

		c.mu.Lock()
		if !c.storesLoaded {
			c.stores, c.storesErr, c.storesLoaded = parsed, err, true
		}
		c.mu.Unlock()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.storesErr != nil {
		return nil, c.storesErr
	}
	stores, ok := c.stores[locale]
	if !ok {
		return nil, fmt.Errorf("内置门店数据中没有地区 %s", locale)
	}
	return append([]model.Store(nil), stores...), nil
}

// RefreshProducts 从 Apple 购买页刷新某地区的商品目录。
//
// 逐个机型抓取并合并。某个机型失败时保留其余机型的结果并返回聚合错误 ——
// iPhone Air 抓不到不该导致连 iPhone 17 都选不了。
func (c *Catalog) RefreshProducts(ctx context.Context, region model.Region, f ProductFetcher) error {
	if f == nil {
		return errors.New("商品抓取器为空")
	}
	if len(region.Families) == 0 {
		return fmt.Errorf("地区 %s 没有配置任何机型", region.Locale)
	}

	var (
		merged []model.Product
		errs   []error
	)
	seen := map[string]struct{}{}

	for _, family := range region.Families {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}

		items, err := f.FetchProducts(ctx, region, family)
		if err != nil {
			errs = append(errs, fmt.Errorf("机型 %s: %w", family, err))
			continue
		}

		for _, p := range items {
			if p.PartNumber == "" {
				continue
			}
			// 不同机型页之间也可能返回同一零件号（如 pro 与 pro max 共页）。
			if _, dup := seen[p.PartNumber]; dup {
				continue
			}
			seen[p.PartNumber] = struct{}{}
			merged = append(merged, p)
		}
	}

	if len(merged) > 0 {
		sortProducts(merged)
		c.mu.Lock()
		c.online[region.Locale] = merged
		c.mu.Unlock()
	}

	if len(errs) > 0 {
		return fmt.Errorf("地区 %s 有 %d/%d 个机型刷新失败: %w",
			region.Locale, len(errs), len(region.Families), errors.Join(errs...))
	}
	return nil
}

// ProductByTitle 按展示名查找商品。
//
// 返回 (值, bool) 而不是像上游 services/product.go:19 那样
// funk.Find(...).(model.Product) —— 那个类型断言在找不到时对 nil 接口
// 做断言，直接 panic 掉整个程序。用户选完商品后正好赶上目录刷新、
// 旧标题不复存在，就足以触发。
func (c *Catalog) ProductByTitle(locale, title string) (model.Product, bool) {
	products, err := c.Products(locale)
	if err != nil {
		return model.Product{}, false
	}
	for _, p := range products {
		if p.Title == title {
			return p, true
		}
	}
	return model.Product{}, false
}

// StoreByTitle 按展示名查找门店，语义同 ProductByTitle
// （对应上游 services/store.go:74 的同类 panic）。
func (c *Catalog) StoreByTitle(locale, title string) (model.Store, bool) {
	stores, err := c.Stores(locale)
	if err != nil {
		return model.Store{}, false
	}
	for _, s := range stores {
		if s.Title == title {
			return s, true
		}
	}
	return model.Store{}, false
}

// ProductTitles 返回某地区全部商品的展示名，供界面选择器使用。
func (c *Catalog) ProductTitles(locale string) ([]string, error) {
	products, err := c.Products(locale)
	if err != nil {
		return nil, err
	}
	titles := make([]string, 0, len(products))
	for _, p := range products {
		titles = append(titles, p.Title)
	}
	return titles, nil
}

// StoreTitles 返回某地区全部门店的展示名，供界面选择器使用。
func (c *Catalog) StoreTitles(locale string) ([]string, error) {
	stores, err := c.Stores(locale)
	if err != nil {
		return nil, err
	}
	titles := make([]string, 0, len(stores))
	for _, s := range stores {
		titles = append(titles, s.Title)
	}
	return titles, nil
}

// loadEmbeddedProducts 解析某地区的内嵌商品快照。
//
// 文件顶层是数组，每个元素都是一份 productSelectionData，与在线抓取时
// 从购买页里截出来的对象结构完全一致，因此复用 apple 包的解析实现。
func loadEmbeddedProducts(locale string) ([]model.Product, error) {
	name := "data/products_" + locale + ".json"
	raw, err := embedded.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("内置商品数据中没有地区 %s: %w", locale, err)
	}

	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, fmt.Errorf("%w: 解析 %s 失败: %v", apple.ErrUnexpectedSchema, name, err)
	}

	var products []model.Product
	seen := map[string]struct{}{}

	for i, group := range groups {
		items, err := apple.ParseProductSelection(group)
		if err != nil {
			return nil, fmt.Errorf("%w: 解析 %s 第 %d 段失败: %v", apple.ErrUnexpectedSchema, name, i, err)
		}
		for _, p := range items {
			if _, dup := seen[p.PartNumber]; dup {
				continue
			}
			seen[p.PartNumber] = struct{}{}
			products = append(products, p)
		}
	}

	if len(products) == 0 {
		return nil, fmt.Errorf("%w: %s 中没有任何商品", apple.ErrUnexpectedSchema, name)
	}

	sortProducts(products)
	return products, nil
}

// sortProducts 让商品排列稳定且符合直觉：先按机型，再按容量从小到大，最后按展示名。
//
// 原始数据里的顺序是乱的（同一机型下 512GB 可能排在 256GB 前面），直接丢进
// 下拉框很难找。按机型标识排序而不是按数据里的出现顺序，是为了让离线快照与
// 在线抓取（抓取顺序取决于 Region.Families 的写法）得到同一份排列。
func sortProducts(products []model.Product) {
	sort.SliceStable(products, func(i, j int) bool {
		a, b := products[i], products[j]
		if a.Family != b.Family {
			return a.Family < b.Family
		}
		ca, cb := capacityRank(a.Capacity), capacityRank(b.Capacity)
		if ca != cb {
			return ca < cb
		}
		return a.Title < b.Title
	})
}

// capacityRank 把 256gb / 1tb 之类的容量标识换算成可比较的数值（单位 GB）。
// 直接按字符串排会把 1tb 排在 256gb 前面。
func capacityRank(capacity string) int {
	lower := strings.ToLower(strings.TrimSpace(capacity))
	multiplier := 1
	switch {
	case strings.HasSuffix(lower, "tb"):
		multiplier = 1024
		lower = strings.TrimSuffix(lower, "tb")
	case strings.HasSuffix(lower, "gb"):
		lower = strings.TrimSuffix(lower, "gb")
	default:
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(lower))
	if err != nil {
		return 0
	}
	return n * multiplier
}

// rawStoreRegion 是 stores.json 顶层数组的元素。
//
// 各地区的结构不统一：hasStates 为 true 时门店挂在 state[].store[] 下，
// 否则直接挂在 store[] 下。两种都要认。
type rawStoreRegion struct {
	Locale    string          `json:"locale"`
	HasStates bool            `json:"hasStates"`
	States    []rawStoreState `json:"state"`
	Stores    []rawStore      `json:"store"`
}

type rawStoreState struct {
	Name   string     `json:"name"`
	Stores []rawStore `json:"store"`
}

type rawStore struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address struct {
		City      string `json:"city"`
		StateName string `json:"stateName"`
	} `json:"address"`
}

// loadEmbeddedStores 解析内嵌门店快照，返回按地区分组的门店表。
//
// 返回 error 而不是像上游 services/store.go:22 那样 panic：目录数据读不到
// 是「这个地区暂时选不了门店」，不是「整个程序应当立刻死掉」。
func loadEmbeddedStores() (map[string][]model.Store, error) {
	raw, err := embedded.ReadFile("data/stores.json")
	if err != nil {
		return nil, fmt.Errorf("读取内置门店数据失败: %w", err)
	}

	var regions []rawStoreRegion
	if err := json.Unmarshal(raw, &regions); err != nil {
		return nil, fmt.Errorf("解析内置门店数据失败: %w", err)
	}

	result := make(map[string][]model.Store, len(regions))
	for _, region := range regions {
		if region.Locale == "" {
			continue
		}

		var stores []model.Store
		seen := map[string]struct{}{}

		appendStore := func(s rawStore, stateName string) {
			if s.ID == "" {
				return
			}
			if _, dup := seen[s.ID]; dup {
				return
			}
			seen[s.ID] = struct{}{}

			// 有 state 层级的地区（中国大陆、日本、澳大利亚等）用 stateName，
			// 否则用 city。香港、新加坡这类城市站根本没有 stateName 字段。
			city := s.Address.City
			if region.HasStates {
				if s.Address.StateName != "" {
					city = s.Address.StateName
				} else if stateName != "" {
					city = stateName
				}
			}

			title := s.Name
			if city != "" {
				title = city + "-" + s.Name
			}

			stores = append(stores, model.Store{
				Number: s.ID,
				Name:   s.Name,
				City:   city,
				Title:  title,
			})
		}

		for _, state := range region.States {
			for _, s := range state.Stores {
				appendStore(s, state.Name)
			}
		}
		for _, s := range region.Stores {
			appendStore(s, "")
		}

		sort.Slice(stores, func(i, j int) bool {
			if stores[i].Title != stores[j].Title {
				return stores[i].Title < stores[j].Title
			}
			return stores[i].Number < stores[j].Number
		})
		result[region.Locale] = stores
	}

	if len(result) == 0 {
		return nil, errors.New("内置门店数据为空")
	}
	return result, nil
}
