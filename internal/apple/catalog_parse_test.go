package apple

// 购买页解析的单元测试。全部用本地字符串或 httptest 服务器，绝不请求 apple.com。
//
// 用内部测试包（package apple）是因为最脆弱的一环 —— 从 JS 对象字面量里
// 按花括号配对截出 productSelectionData —— 是不导出的：颜色的 image 字段是
// 整段 HTML，里面既有花括号也有转义引号，配对一旦算错，截出来的既不是完整
// JSON，也不会立刻报错，而是变成「解析不出商品」。在这个项目里，
// 「解析不出商品」必须表现为错误，绝不能悄悄退化成一份空目录。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// normalSelection 是一段结构正常的 productSelectionData，字段取自真实购买页。
const normalSelection = `{
		"products": [
			{"partNumber": "MG724CH/A", "familyType": "iphone17", "dimensionCapacity": "512gb", "dimensionColor": "black"},
			{"partNumber": "MG914CH/A", "familyType": "iphone17pro", "dimensionCapacity": "1tb", "dimensionColor": "cosmicorange"},
			{"partNumber": "MG334CH/A", "familyType": "iphoneair", "dimensionCapacity": "256gb", "dimensionColor": "cloudwhite"},
			{"partNumber": "MG724CH/A", "familyType": "iphone17", "dimensionCapacity": "512gb", "dimensionColor": "black", "carrierModel": "SOFTBANK_IPHONE17"}
		],
		"displayValues": {
			"dimensionColor": {
				"title": {"singleVariantDisplayTitle": "颜色:", "multiVariantsDisplayTitle": "选择颜色:"},
				"variantOrder": ["black", "cosmicorange", "cloudwhite"],
				"variantSortOrder": ["202", "206", "228"],
				"black": {"value": "黑色", "image": "<em class=\"click-layer\"></em>"},
				"cosmicorange": {"value": "星宇橙色", "image": "<em class=\"click-layer\"></em>"},
				"cloudwhite": {"value": "云白色", "image": "<em class=\"click-layer\"></em>"}
			}
		}
	}`

// buildBuyPage 把一段 productSelectionData 包成购买页的样子。
//
// 注意 productSelectionData 这个键没有引号 —— 页面里是 JS 对象字面量而不是
// JSON，所以整段不能直接 json.Unmarshal。对象后面还跟着别的键，如果截取多截
// 或少截一个字节，后面的 ParseProductSelection 就会报错。
func buildBuyPage(selection string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><title>购买 iPhone</title></head><body>\n")
	b.WriteString("<script type=\"text/javascript\">\n")
	b.WriteString("window.PRODUCT_SELECTION_BOOTSTRAP = {\n")
	b.WriteString("        productSelectionData: ")
	b.WriteString(selection)
	b.WriteString(",\n        analyticsData: { \"page\": \"buy-iphone\", \"tpl\": \"{a}{b}\" }\n};\n")
	b.WriteString("</script>\n</body></html>\n")
	return []byte(b.String())
}

type wantProduct struct {
	part     string
	family   string
	capacity string
	color    string
	title    string
}

func checkProducts(t *testing.T, got []model.Product, want []wantProduct) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("解析出 %d 个商品，期望 %d 个: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.PartNumber != w.part || g.Family != w.family || g.Capacity != w.capacity ||
			g.Color != w.color || g.Title != w.title {
			t.Errorf("第 %d 个商品 = %+v，期望 {PartNumber:%s Family:%s Capacity:%s Color:%s Title:%s}",
				i, g, w.part, w.family, w.capacity, w.color, w.title)
		}
	}
}

// ---------------------------------------------------------------------------
// 正常页面
// ---------------------------------------------------------------------------

// TestExtractAndParseNormalPage 覆盖最常见的一条路径：
// 从购买页截出 productSelectionData，解析出零件号、规范化容量、本地化颜色名。
func TestExtractAndParseNormalPage(t *testing.T) {
	page := buildBuyPage(normalSelection)

	raw, err := extractProductSelectionData(page)
	if err != nil {
		t.Fatalf("extractProductSelectionData 返回错误: %v", err)
	}
	// 截出来的必须与原对象逐字节相同：多一个字节或少一个字节，
	// 后面的 json.Unmarshal 都会因为尾随内容而失败。
	if string(raw) != normalSelection {
		t.Fatalf("截取结果与原对象不一致:\n截取: %q\n原文: %q", string(raw), normalSelection)
	}

	products, err := ParseProductSelection(raw)
	if err != nil {
		t.Fatalf("ParseProductSelection 返回错误: %v", err)
	}

	checkProducts(t, products, []wantProduct{
		// 512gb → 512GB，black → 黑色（取自 displayValues.dimensionColor）
		{"MG724CH/A", "iphone17", "512gb", "black", "iPhone 17 512GB 黑色"},
		// 1tb → 1TB，不能写成 1024GB，也不能按字符串排到 256gb 前面
		{"MG914CH/A", "iphone17pro", "1tb", "cosmicorange", "iPhone 17 Pro 1TB 星宇橙色"},
		{"MG334CH/A", "iphoneair", "256gb", "cloudwhite", "iPhone Air 256GB 云白色"},
		// 第四条与第一条零件号相同（只是运营商不同），必须被去掉
	})
}

// TestParseProductSelectionDeduplicatesByPartNumber 单独盯住去重：
// 日本站把同一台机器按运营商和无锁版各列一遍，零件号完全相同。
// 查库存只认零件号，界面上出现一模一样的两行毫无意义。
func TestParseProductSelectionDeduplicatesByPartNumber(t *testing.T) {
	const selection = `{
		"products": [
			{"partNumber": "MG6A4J/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "lavender", "carrierModel": "SOFTBANK_IPHONE17"},
			{"partNumber": "MG6A4J/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "lavender", "carrierModel": "UNLOCKED_JP"},
			{"partNumber": "MG6C4J/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "sage", "carrierModel": "UNLOCKED_JP"}
		],
		"displayValues": {"dimensionColor": {"lavender": {"value": "ラベンダー"}, "sage": {"value": "セージ"}}}
	}`

	products, err := ParseProductSelection([]byte(selection))
	if err != nil {
		t.Fatalf("ParseProductSelection 返回错误: %v", err)
	}
	checkProducts(t, products, []wantProduct{
		{"MG6A4J/A", "iphone17", "256gb", "lavender", "iPhone 17 256GB ラベンダー"},
		{"MG6C4J/A", "iphone17", "256gb", "sage", "iPhone 17 256GB セージ"},
	})
}

// ---------------------------------------------------------------------------
// 花括号配对（解析器最脆弱的地方）
// ---------------------------------------------------------------------------

// bracesSelection 把各种能骗过朴素配对算法的内容都塞进字符串字面量里：
// 未配对的花括号、转义引号、以反斜杠结尾的字符串、单引号。
//
// prematureClose 那一行是刻意设计的：它出现在第 4 层嵌套里，正好带 4 个右
// 花括号。不跳过字符串的话，配对会在这里就以为对象结束了，截出来的是一段
// 半截 JSON —— 这正是「解析器悄悄返回空目录」的典型成因。
// unbalancedOpen 则是反方向：多出来的左花括号会让配对一路吃到页面结尾。
const bracesSelection = `{
		"products": [
			{"partNumber": "MTRICKY/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "black"}
		],
		"displayValues": {
			"dimensionColor": {
				"black": {
					"prematureClose": "四个右花括号 }}}} 只是文本，不是对象结束",
					"unbalancedOpen": "两个左花括号 {{ 同样只是文本",
					"value": "黑色",
					"image": "<em class=\"click-layer\"></em> <div class=\"effect black\" style=\"{ background: #000 }\">}}{{</div>",
					"nested": "{\"a\": {\"b\": \"}\"}}",
					"trailingBackslash": "路径以反斜杠结尾 \\",
					"escapedQuote": "含转义引号 \" 之后还有一个 {",
					"apostrophe": "it's fine { and }"
				}
			}
		}
	}`

// TestExtractHandlesBracesInsideStrings 是这一组里最关键的一条。
//
// 颜色的 image 字段是整段 HTML，里面既有花括号也有转义引号。配对时如果不跳过
// 字符串字面量，截出来的片段就会在错误的位置结束；而错误的位置往往仍是一段
// 「看起来像 JSON」的东西，失败会一路悄悄传下去，最后表现为「一个商品都没有」。
func TestExtractHandlesBracesInsideStrings(t *testing.T) {
	page := buildBuyPage(bracesSelection)

	raw, err := extractProductSelectionData(page)
	if err != nil {
		t.Fatalf("extractProductSelectionData 返回错误: %v", err)
	}
	if string(raw) != bracesSelection {
		t.Fatalf("截取位置算错了:\n截取: %q\n原文: %q", string(raw), bracesSelection)
	}

	products, err := ParseProductSelection(raw)
	if err != nil {
		t.Fatalf("ParseProductSelection 返回错误: %v", err)
	}
	checkProducts(t, products, []wantProduct{
		{"MTRICKY/A", "iphone17", "256gb", "black", "iPhone 17 256GB 黑色"},
	})
}

// TestMatchObjectSkipsStringLiterals 直接盯住配对函数本身。
func TestMatchObjectSkipsStringLiterals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // 期望截出的片段
	}{
		{"最简对象", `{}尾巴`, `{}`},
		{"嵌套对象", `{"a":{"b":{}}}尾巴`, `{"a":{"b":{}}}`},
		{"字符串里的左花括号", `{"a":"{{{"}尾巴`, `{"a":"{{{"}`},
		{"字符串里的右花括号", `{"a":"}}}"}尾巴`, `{"a":"}}}"}`},
		{"转义引号后仍在字符串内", `{"a":"x\"}y"}尾巴`, `{"a":"x\"}y"}`},
		{"以反斜杠结尾的字符串", `{"a":"x\\"}尾巴`, `{"a":"x\\"}`},
		{"数组里的对象", `{"a":[{"b":1},{"c":2}]}尾巴`, `{"a":[{"b":1},{"c":2}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, err := matchObject([]byte(tc.in), 0)
			if err != nil {
				t.Fatalf("matchObject 返回错误: %v", err)
			}
			if got := tc.in[:end]; got != tc.want {
				t.Errorf("截出 %q，期望 %q", got, tc.want)
			}
		})
	}

	if _, err := matchObject([]byte(`{"a":{"b":1}`), 0); err == nil {
		t.Error("花括号不闭合时应当返回错误")
	}
	if _, err := matchObject([]byte(`{"a":"未闭合的字符串}`), 0); err == nil {
		t.Error("字符串未闭合导致花括号配不上时应当返回错误")
	}
}

// TestExtractPrefersBootstrapMarker 验证定位是从 PRODUCT_SELECTION_BOOTSTRAP
// 开始的：页面别处出现同名的键时，不能把那份旧数据当成商品目录。
func TestExtractPrefersBootstrapMarker(t *testing.T) {
	decoy := `<script>window.LEGACY_SELECTION = { productSelectionData: {"products":[` +
		`{"partNumber":"DECOY/A","familyType":"iphone17","dimensionCapacity":"128gb","dimensionColor":"black"}` +
		`]} };</script>` + "\n"

	page := append([]byte(decoy), buildBuyPage(normalSelection)...)

	raw, err := extractProductSelectionData(page)
	if err != nil {
		t.Fatalf("extractProductSelectionData 返回错误: %v", err)
	}
	products, err := ParseProductSelection(raw)
	if err != nil {
		t.Fatalf("ParseProductSelection 返回错误: %v", err)
	}
	for _, p := range products {
		if p.PartNumber == "DECOY/A" {
			t.Fatalf("命中了 bootstrap 之前的同名键: %+v", products)
		}
	}
	if len(products) != 3 {
		t.Fatalf("解析出 %d 个商品，期望 3 个: %+v", len(products), products)
	}
}

// buildBuyPageWithDecoys 在真正的 productSelectionData 之前插入若干段干扰文本。
//
// 干扰段一律放在 PRODUCT_SELECTION_BOOTSTRAP 里面，才能真正考验候选扫描：
// 放在外面会被开头那次「先定位全局变量」的裁剪直接切掉，等于什么都没测。
func buildBuyPageWithDecoys(selection string, decoys ...string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><title>购买 iPhone</title></head><body>\n")
	b.WriteString("<script type=\"text/javascript\">\n")
	b.WriteString("window.PRODUCT_SELECTION_BOOTSTRAP = {\n")
	for _, d := range decoys {
		b.WriteString("        ")
		b.WriteString(d)
		b.WriteString("\n")
	}
	b.WriteString("        productSelectionData: ")
	b.WriteString(selection)
	b.WriteString(",\n        analyticsData: { \"page\": \"buy-iphone\" }\n};\n")
	b.WriteString("</script>\n</body></html>\n")
	return []byte(b.String())
}

// TestExtractSkipsDecoyOccurrences 覆盖「同名文本先出现在别处」。
//
// 原来只做一次 bytes.Index 取首个匹配：命中干扰文本后发现它后面不是冒号，就直接判
// 整页失败，真正的属性再也没机会被看到。表现是购买页明明带着完整数据，程序却一口
// 咬定「结构不符」—— 抓不到目录，用户连型号都选不出来。
func TestExtractSkipsDecoyOccurrences(t *testing.T) {
	cases := []struct {
		name  string
		decoy string
	}{
		// 最朴素的一种：字符串值恰好就是这个词。
		{"字符串值里的同名文本", `note: "productSelectionData",`},
		// 文案里连冒号带花括号一起出现，能骗过「找冒号 + 花括号配对」，
		// 只有最后那道 JSON 校验能把它挡下来。
		{"文案里带冒号和花括号", `hint: "productSelectionData: {不是 JSON 只是一句提示}",`},
		// 更长的标识符把目标名整个包在里面，且后面真的跟着一个合法 JSON 对象。
		// 没有边界检查的话，这份旧数据会被原封不动当成商品目录返回 —— 比报错更糟，
		// 因为界面上会显示一批早就下架的型号，用户守着一个永远不会有货的零件号。
		{"更长标识符里的同名文本", `legacy_productSelectionData: {"products":[` +
			`{"partNumber":"DECOY/A","familyType":"iphone17","dimensionCapacity":"128gb","dimensionColor":"black"}]},`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := buildBuyPageWithDecoys(normalSelection, tc.decoy)

			raw, err := extractProductSelectionData(page)
			if err != nil {
				t.Fatalf("extractProductSelectionData 返回错误: %v", err)
			}
			if string(raw) != normalSelection {
				t.Fatalf("截取的不是真正的 productSelectionData:\n截取: %q", string(raw))
			}

			products, err := ParseProductSelection(raw)
			if err != nil {
				t.Fatalf("ParseProductSelection 返回错误: %v", err)
			}
			for _, p := range products {
				if p.PartNumber == "DECOY/A" {
					t.Fatalf("命中了干扰数据: %+v", products)
				}
			}
			if len(products) != 3 {
				t.Fatalf("解析出 %d 个商品，期望 3 个: %+v", len(products), products)
			}
		})
	}

	// 三段干扰同时出现也要能穿过去。
	t.Run("多段干扰叠加", func(t *testing.T) {
		decoys := make([]string, 0, len(cases))
		for _, tc := range cases {
			decoys = append(decoys, tc.decoy)
		}
		raw, err := extractProductSelectionData(buildBuyPageWithDecoys(normalSelection, decoys...))
		if err != nil {
			t.Fatalf("extractProductSelectionData 返回错误: %v", err)
		}
		if string(raw) != normalSelection {
			t.Fatalf("截取的不是真正的 productSelectionData:\n截取: %q", string(raw))
		}
	})
}

// TestExtractAcceptsQuotedKey 覆盖「键被写成带引号的形式」。
//
// 页面里现在是 JS 对象字面量（键不带引号），但这只是打包器的选择，不是契约。
// 原来的写法在键后紧跟引号时就判「之后不是冒号」，Apple 换个打包器就会让整个
// 目录抓取失效 —— 又一次把「我们没适配」表现成「这个机型没有商品」。
func TestExtractAcceptsQuotedKey(t *testing.T) {
	cases := []struct{ name, key string }{
		{"不带引号", `productSelectionData:`},
		{"双引号", `"productSelectionData":`},
		{"双引号且冒号前有空格", `"productSelectionData" :`},
		{"单引号", `'productSelectionData':`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := []byte(`<script>window.PRODUCT_SELECTION_BOOTSTRAP = {` + "\n" +
				tc.key + " " + normalSelection + ",\n\"analyticsData\": {}};</script>")

			raw, err := extractProductSelectionData(page)
			if err != nil {
				t.Fatalf("extractProductSelectionData 返回错误: %v", err)
			}
			if string(raw) != normalSelection {
				t.Fatalf("截取结果与原对象不一致:\n截取: %q", string(raw))
			}
			products, err := ParseProductSelection(raw)
			if err != nil {
				t.Fatalf("ParseProductSelection 返回错误: %v", err)
			}
			if len(products) != 3 {
				t.Fatalf("解析出 %d 个商品，期望 3 个: %+v", len(products), products)
			}
		})
	}
}

// TestExtractAllCandidatesInvalid 覆盖「所有候选都不成立」。
//
// 扫描全部候选是为了不漏掉真正的属性，但绝不能反过来变成「凑合挑一个」：
// 一个都不成立时必须报 ErrUnexpectedSchema，让上层知道是结构变了。
func TestExtractAllCandidatesInvalid(t *testing.T) {
	page := []byte(`<script>window.PRODUCT_SELECTION_BOOTSTRAP = {
		note: "productSelectionData",
		hint: "productSelectionData = 别的东西",
		tip: "productSelectionData: {不是 JSON}",
		legacy_productSelectionData: {"products":[{"partNumber":"DECOY/A"}]}
	};</script>`)

	raw, err := extractProductSelectionData(page)
	if err == nil {
		t.Fatalf("应当返回错误，实际截出 %q", string(raw))
	}
	if !errors.Is(err, ErrUnexpectedSchema) {
		t.Errorf("错误没有携带 ErrUnexpectedSchema: %v", err)
	}
	if raw != nil {
		t.Errorf("出错时不应返回内容，实际 %q", string(raw))
	}
}

// ---------------------------------------------------------------------------
// 异常输入：必须返回带 ErrUnexpectedSchema 的错误，而不是 panic，也不是空目录
// ---------------------------------------------------------------------------

func TestExtractProductSelectionDataErrors(t *testing.T) {
	const prefix = `<script>window.PRODUCT_SELECTION_BOOTSTRAP = {`

	cases := []struct {
		name       string
		page       string
		wantSubstr string
	}{
		{"空页面", "", "找不到"},
		{"拦截页里没有任何标记", "<html><body><h1>Access Denied</h1></body></html>", "找不到"},
		{"有 bootstrap 但没有 productSelectionData", prefix + `"analyticsData": {"page": "buy"}};</script>`, "找不到"},
		{"键后面是等号而不是冒号", prefix + ` productSelectionData = {"products": []}};</script>`, "不是冒号"},
		{"键后面直接结束", prefix + ` productSelectionData`, "不是冒号"},
		{"值是字符串", prefix + ` productSelectionData: "unavailable"};</script>`, "不是对象"},
		{"值是 null", prefix + ` productSelectionData: null};</script>`, "不是对象"},
		{"值是数组", prefix + ` productSelectionData: []};</script>`, "不是对象"},
		{"冒号后直接结束", prefix + ` productSelectionData:`, "不是对象"},
		{"花括号配平但内容不是 JSON", prefix + ` productSelectionData: {不是 JSON}};</script>`, "不是合法 JSON"},
		{"响应被截断，花括号不闭合", prefix + ` productSelectionData: {"products": [{"partNumber": "MG724CH/A"`, "花括号未闭合"},
		{"字符串未闭合吞掉了收尾花括号", prefix + ` productSelectionData: {"a": "没有收尾引号}};</script>`, "花括号未闭合"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := extractProductSelectionData([]byte(tc.page))
			if err == nil {
				t.Fatalf("应当返回错误，实际截出 %q", string(raw))
			}
			// 这是最要紧的一条：所有解析失败都必须归到 ErrUnexpectedSchema。
			// 上层据此判断「这是结构变了」，而不是「这个机型确实没有商品」。
			if !errors.Is(err, ErrUnexpectedSchema) {
				t.Errorf("错误没有携带 ErrUnexpectedSchema: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("错误信息 %q 中不含 %q", err.Error(), tc.wantSubstr)
			}
			if raw != nil {
				t.Errorf("出错时不应返回内容，实际 %q", string(raw))
			}
		})
	}
}

func TestParseProductSelectionRejectsBrokenJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"空输入", ""},
		{"只有空白", "   \n\t"},
		{"花括号配平但 JSON 非法", `{"products": [ }`},
		{"JSON 被截断", `{"products": [{"partNumber": "MG724CH/A"}`},
		{"顶层是数组", `[{"partNumber": "MG724CH/A"}]`},
		{"顶层是字符串", `"unavailable"`},
		{"products 不是数组", `{"products": {"partNumber": "MG724CH/A"}}`},
		{"products 元素不是对象", `{"products": ["MG724CH/A"]}`},
		{"displayValues 不是对象", `{"products": [], "displayValues": []}`},
		{"HTML 拦截页", `<html><body>Access Denied</body></html>`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			products, err := ParseProductSelection([]byte(tc.raw))
			if err == nil {
				t.Fatalf("应当返回错误，实际解析出 %d 个商品", len(products))
			}
			if !errors.Is(err, ErrUnexpectedSchema) {
				t.Errorf("错误没有携带 ErrUnexpectedSchema: %v", err)
			}
			if products != nil {
				t.Errorf("出错时不应返回商品，实际 %+v", products)
			}
		})
	}
}

// TestParseProductSelectionEmptyButValid 覆盖「JSON 合法但没有商品」。
//
// 这种情况不该报错（结构没问题），但也绝不能被当成一份可用的目录：
// 调用方（FetchProducts）会把空结果升级成 ErrUnexpectedSchema，见下面的测试。
func TestParseProductSelectionEmptyButValid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"空对象", `{}`},
		{"JSON null", `null`},
		{"products 为 null", `{"products": null}`},
		{"products 为空数组", `{"products": []}`},
		{"零件号为空的条目被丢弃", `{"products": [{"partNumber": "", "familyType": "iphone17"}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			products, err := ParseProductSelection([]byte(tc.raw))
			if err != nil {
				t.Fatalf("结构合法时不应返回错误: %v", err)
			}
			if len(products) != 0 {
				t.Fatalf("期望 0 个商品，实际 %+v", products)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// displayValues.dimensionColor 里的非颜色条目
// ---------------------------------------------------------------------------

// TestParseProductSelectionIgnoresNonColorDisplayValues 覆盖颜色表里混着的杂项。
//
// 这张表并不是「颜色 → 名称」的纯映射：title 是
// {"singleVariantDisplayTitle": ...}，variantOrder 干脆是个数组。
// 把它整体映射成结构体会让整个对象反序列化失败，进而丢掉全部商品。
func TestParseProductSelectionIgnoresNonColorDisplayValues(t *testing.T) {
	const selection = `{
		"products": [
			{"partNumber": "MA/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "black"},
			{"partNumber": "MB/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "sage"},
			{"partNumber": "MC/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "mistblue"},
			{"partNumber": "MD/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "brandnewcolor"}
		],
		"displayValues": {
			"dimensionColor": {
				"title": {"singleVariantDisplayTitle": "颜色:", "multiVariantsDisplayTitle": "选择颜色:"},
				"variantOrder": ["black", "sage", "mistblue"],
				"variantSortOrder": ["202", "206", "228"],
				"someFlag": true,
				"someCount": 7,
				"someNull": null,
				"someString": "直接是字符串而不是对象",
				"black": {"value": "黑色", "image": "<em class=\"click-layer\"></em>"},
				"sage": {"value": "鼠尾草绿色"},
				"mistblue": {"value": "", "image": "<em></em>"}
			}
		}
	}`

	products, err := ParseProductSelection([]byte(selection))
	if err != nil {
		t.Fatalf("ParseProductSelection 返回错误: %v", err)
	}

	checkProducts(t, products, []wantProduct{
		{"MA/A", "iphone17", "256gb", "black", "iPhone 17 256GB 黑色"},
		{"MB/A", "iphone17", "256gb", "sage", "iPhone 17 256GB 鼠尾草绿色"},
		// value 为空串时退回内部标识，总比展示名里少一截强
		{"MC/A", "iphone17", "256gb", "mistblue", "iPhone 17 256GB mistblue"},
		// 表里根本没有的颜色同样退回内部标识
		{"MD/A", "iphone17", "256gb", "brandnewcolor", "iPhone 17 256GB brandnewcolor"},
	})
}

// TestParseProductSelectionWithoutDisplayValues 覆盖整段 displayValues 缺失。
func TestParseProductSelectionWithoutDisplayValues(t *testing.T) {
	const selection = `{"products": [
		{"partNumber": "MA/A", "familyType": "iphone17", "dimensionCapacity": "256gb", "dimensionColor": "black"},
		{"partNumber": "MB/A", "familyType": "iphone17", "dimensionCapacity": "", "dimensionColor": ""},
		{"partNumber": "MC/A", "familyType": "", "dimensionCapacity": "512gb", "dimensionColor": "black"}
	]}`

	products, err := ParseProductSelection([]byte(selection))
	if err != nil {
		t.Fatalf("ParseProductSelection 返回错误: %v", err)
	}
	checkProducts(t, products, []wantProduct{
		{"MA/A", "iphone17", "256gb", "black", "iPhone 17 256GB black"},
		// 容量和颜色都缺失时不能留下多余空格
		{"MB/A", "iphone17", "", "", "iPhone 17"},
		{"MC/A", "", "512gb", "black", "512GB black"},
	})
}

// ---------------------------------------------------------------------------
// 展示名拼装
// ---------------------------------------------------------------------------

func TestNormalizeCapacity(t *testing.T) {
	cases := []struct{ in, want string }{
		{"512gb", "512GB"},
		{"256GB", "256GB"},
		{"1tb", "1TB"},
		{"2TB", "2TB"},
		{" 128gb ", "128GB"},
		{"512 gb", "512GB"},
		{"64mb", "64MB"},
		{"", ""},
		{"   ", ""},
		{"unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := NormalizeCapacity(tc.in); got != tc.want {
			t.Errorf("NormalizeCapacity(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestFamilyDisplayName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"iphone17", "iPhone 17"},
		{"iphone17pro", "iPhone 17 Pro"},
		{"iphone17promax", "iPhone 17 Pro Max"},
		{"iphoneair", "iPhone Air"},
		{"iphone16plus", "iPhone 16 Plus"},
		{"iphone16e", "iPhone 16e"},
		{"iphone13mini", "iPhone 13 mini"},
		{"iphonese", "iPhone SE"},
		{"IPHONE17PRO", "iPhone 17 Pro"},
		{"  iphone17  ", "iPhone 17"},
		{"", ""},
		// 不认识的前缀原样返回，总比拼出个错名字强
		{"ipadpro", "ipadpro"},
		{"Watch Ultra", "Watch Ultra"},
	}
	for _, tc := range cases {
		if got := FamilyDisplayName(tc.in); got != tc.want {
			t.Errorf("FamilyDisplayName(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// FetchProducts：把取页与解析串起来，全程指向本地 httptest 服务器
// ---------------------------------------------------------------------------

func newPageClient() *Client {
	// 默认的全局限速与指数退避是给真实网络准备的，测试里一律关掉。
	return NewClient(WithMinInterval(0), WithMaxRetries(0))
}

func startPageServer(t *testing.T, h http.HandlerFunc) model.Region {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return model.Region{
		Title:    "测试地区",
		Locale:   "zh_CN",
		BaseURL:  srv.URL,
		Families: []string{"iphone-17"},
	}
}

func TestFetchProductsParsesBuyPage(t *testing.T) {
	var gotPath string
	region := startPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buildBuyPage(normalSelection))
	})

	products, err := newPageClient().FetchProducts(context.Background(), region, "iphone-17")
	if err != nil {
		t.Fatalf("FetchProducts 返回错误: %v", err)
	}
	if want := "/shop/buy-iphone/iphone-17"; gotPath != want {
		t.Errorf("请求路径为 %q，期望 %q", gotPath, want)
	}
	checkProducts(t, products, []wantProduct{
		{"MG724CH/A", "iphone17", "512gb", "black", "iPhone 17 512GB 黑色"},
		{"MG914CH/A", "iphone17pro", "1tb", "cosmicorange", "iPhone 17 Pro 1TB 星宇橙色"},
		{"MG334CH/A", "iphoneair", "256gb", "cloudwhite", "iPhone Air 256GB 云白色"},
	})
}

func TestFetchProductsRejectsEmptyFamily(t *testing.T) {
	region := startPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("机型为空时不应发起请求")
	})
	for _, family := range []string{"", "   ", "\t\n"} {
		if _, err := newPageClient().FetchProducts(context.Background(), region, family); err == nil {
			t.Errorf("机型 %q 为空时应当返回错误", family)
		}
	}
}

// TestFetchProductsFailureNeverYieldsEmptyCatalog 是这个包的核心不变量在
// 目录抓取上的体现：抓不到、解析不了，都必须是错误。
//
// 一旦「失败」被折叠成「空目录」，上层就会以为这个机型下架了，用户对着一个
// 空下拉框无从判断到底是没货还是程序坏了 —— 这正是上游把请求失败当成「无货」
// 之后发生的事。
func TestFetchProductsFailureNeverYieldsEmptyCatalog(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantErr error
	}{
		{
			name: "HTTP 541 拦截页",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(541)
				_, _ = io.WriteString(w, "<html><body>blocked</body></html>")
			},
			wantErr: ErrBlocked,
		},
		{
			name: "HTTP 200 但是拦截页，没有商品数据",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, "<html><body><h1>Access Denied</h1></body></html>")
			},
			wantErr: ErrUnexpectedSchema,
		},
		{
			name: "HTTP 200 但响应体为空",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			},
			wantErr: ErrBlocked,
		},
		{
			name: "页面结构正常但一个商品都没有",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(buildBuyPage(`{"products": [], "displayValues": {"dimensionColor": {}}}`))
			},
			wantErr: ErrUnexpectedSchema,
		},
		{
			name: "页面被中途截断",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				full := buildBuyPage(normalSelection)
				_, _ = w.Write(full[:len(full)/2])
			},
			wantErr: ErrUnexpectedSchema,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			region := startPageServer(t, tc.handler)
			products, err := newPageClient().FetchProducts(context.Background(), region, "iphone-17")
			if err == nil {
				t.Fatalf("应当返回错误，实际解析出 %d 个商品", len(products))
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("错误 %v 中不含 %v", err, tc.wantErr)
			}
			if products != nil {
				t.Errorf("出错时不应返回商品，实际 %+v", products)
			}
		})
	}
}

func TestFetchProductsContextCancelled(t *testing.T) {
	region := startPageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buildBuyPage(normalSelection))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	products, err := newPageClient().FetchProducts(ctx, region, "iphone-17")
	if err == nil {
		t.Fatalf("context 已取消时应当返回错误，实际解析出 %d 个商品", len(products))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("错误 %v 中不含 context.Canceled", err)
	}
}
