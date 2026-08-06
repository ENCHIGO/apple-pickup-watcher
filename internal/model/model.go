// Package model 定义跨包共享的核心类型。
//
// 这个包不依赖任何 UI 框架，也不发起网络请求，因此可以被
// apple / catalog / watcher / notify / ui 各层自由引用而不产生循环依赖。
package model

import "fmt"

// Availability 表示某个型号在某个门店的可取货状态。
//
// 这里刻意做成三态。上游项目只有「有货 / 无货」两种状态，请求失败时
// 会退化成「无货」（services/listen.go:226-230 出错返回空 map，:147 把
// 查不到的一律标成无货）。结果是 Apple 把接口拦掉之后，界面上是满屏
// 「无货」，用户盯着一个早已失效的程序干等。把 Unknown 提升成一等状态，
// 「查不到」和「确实没货」在类型层面就无法混淆。
type Availability uint8

const (
	// Unknown 表示尚未查询，或最近一次查询失败。它不代表没有货。
	Unknown Availability = iota
	// OutOfStock 表示 Apple 明确回答了「此门店该型号不可取货」。
	OutOfStock
	// InStock 表示 Apple 明确回答了「此门店该型号可取货」。
	InStock
)

// String 返回用于界面展示的中文描述。
func (a Availability) String() string {
	switch a {
	case InStock:
		return "有货"
	case OutOfStock:
		return "无货"
	default:
		return "未知"
	}
}

// Region 是一个 Apple 在线商店的地区站点。
//
// BaseURL 必须是该地区商店的真实站点前缀，拼接 /shop/... 后可直接访问。
// 注意中国大陆用的是独立域名 www.apple.com.cn，而不是 www.apple.com/cn
// —— 上游项目按 www.apple.com/{shortCode} 统一拼接（services/listen.go:189-193），
// 对中国大陆是错的。
type Region struct {
	// Title 是界面上展示的地区名。
	Title string
	// Locale 是 Apple 的语言地区标识，如 zh_CN，同时用作内置目录数据的文件名后缀。
	Locale string
	// BaseURL 是该地区商店站点前缀，不含结尾斜杠。
	BaseURL string
	// Families 是该地区需要抓取的 iPhone 购买页 slug，用于刷新商品目录。
	Families []string
}

// PickupMessageURL 返回该地区取货状态查询接口的完整地址前缀。
func (r Region) PickupMessageURL() string {
	return r.BaseURL + "/shop/retail/pickup-message"
}

// BagURL 返回该地区购物袋页面地址。
func (r Region) BagURL() string {
	return r.BaseURL + "/shop/bag"
}

// BuyPageURL 返回某个机型购买页地址，用于在线刷新商品目录。
func (r Region) BuyPageURL(family string) string {
	return fmt.Sprintf("%s/shop/buy-iphone/%s", r.BaseURL, family)
}

// Regions 是内置的地区表。
//
// 每个 BaseURL 都经过实际请求验证：以 /shop/retail/pickup-message 查询
// 该地区的真实门店号与零件号，均返回 HTTP 200 且带有 partsAvailability。
var Regions = []Region{
	{Title: "中国大陆", Locale: "zh_CN", BaseURL: "https://www.apple.com.cn", Families: defaultFamilies},
	{Title: "中国香港", Locale: "zh_HK", BaseURL: "https://www.apple.com/hk-zh", Families: defaultFamilies},
	{Title: "中国台湾", Locale: "zh_TW", BaseURL: "https://www.apple.com/tw", Families: defaultFamilies},
	{Title: "日本", Locale: "ja_JP", BaseURL: "https://www.apple.com/jp", Families: defaultFamilies},
	{Title: "Singapore", Locale: "en_SG", BaseURL: "https://www.apple.com/sg", Families: defaultFamilies},
	{Title: "Australia", Locale: "en_AU", BaseURL: "https://www.apple.com/au", Families: defaultFamilies},
	{Title: "Malaysia", Locale: "en_MY", BaseURL: "https://www.apple.com/my", Families: defaultFamilies},
}

// defaultFamilies 是当前在售的 iPhone 购买页 slug。
//
// 新机型发布后只需在这里追加一行，商品目录会自动从 Apple 官网抓取，
// 不需要再像上游那样手工从开发者工具里复制 productSelectionData
// （见上游 model/area.go:14-17 的说明）。
var defaultFamilies = []string{"iphone-17", "iphone-17-pro", "iphone-air"}

// RegionByLocale 按 locale 查找地区，找不到时第二个返回值为 false。
func RegionByLocale(locale string) (Region, bool) {
	for _, r := range Regions {
		if r.Locale == locale {
			return r, true
		}
	}
	return Region{}, false
}

// RegionByTitle 按界面标题查找地区，找不到时第二个返回值为 false。
func RegionByTitle(title string) (Region, bool) {
	for _, r := range Regions {
		if r.Title == title {
			return r, true
		}
	}
	return Region{}, false
}

// RegionTitles 返回全部地区标题，用于界面选择器。
func RegionTitles() []string {
	titles := make([]string, 0, len(Regions))
	for _, r := range Regions {
		titles = append(titles, r.Title)
	}
	return titles
}

// Product 是一个具体可购买的 iPhone 配置（机型 + 容量 + 颜色）。
type Product struct {
	// PartNumber 是 Apple 零件号，如 MG724CH/A，查询库存时的唯一标识。
	PartNumber string
	// Family 是机型系列，如 iphone17。
	Family string
	// Capacity 是存储容量，如 512gb。
	Capacity string
	// Color 是颜色的内部标识，如 black。
	Color string
	// Title 是界面展示名，如「iPhone 17 512GB 黑色」。
	Title string
}

// Store 是一家 Apple 直营店。
type Store struct {
	// Number 是门店编号，如 R683，查询库存时的唯一标识。
	Number string
	// Name 是门店名，如「环球港」。
	Name string
	// City 是所在城市；部分地区的数据源用 state 字段代替。
	City string
	// Title 是界面展示名，如「上海-环球港」。
	Title string
}

// Target 是一条监控目标：在某地区的某门店盯某个型号。
type Target struct {
	Locale      string `json:"locale"`
	StoreNumber string `json:"store_number"`
	StoreTitle  string `json:"store_title"`
	PartNumber  string `json:"part_number"`
	ProductName string `json:"product_name"`
}

// Key 返回该目标的唯一键，用于去重与状态索引。
func (t Target) Key() string {
	return t.Locale + "|" + t.StoreNumber + "|" + t.PartNumber
}
