package apple

// 本文件给 Client 增加「在线抓取商品目录」的能力。
//
// 上游把 productSelectionData 手工从浏览器开发者工具里复制成
// products_<locale>.json 提交进仓库（见上游 model/area.go:14-17 的说明），
// 于是每次 Apple 发新机都必须等作者手动更新并发版；作者停止维护之后，
// 这份目录就永远停在了旧机型上。这里改成直接从购买页现抓，内置 JSON
// 只作为离线兜底。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// maxPageBytes 是购买页 HTML 的读取上限。
//
// 购买页本身在 2 MB 量级，留一倍余量即可。设上限的目的和 doOnce 一样：
// 万一被换成了别的巨大页面，不至于把它整个读进内存。
const maxPageBytes = 8 << 20

// bootstrapMarker 是购买页里承载商品数据的全局变量名。
//
// 页面中的原文形如：
//
//	window.PRODUCT_SELECTION_BOOTSTRAP = {
//	        productSelectionData: {...}
//
// 注意 productSelectionData 这个键没有引号，它是 JS 对象字面量而非 JSON，
// 因此整段不能直接 json.Unmarshal，只能定位到冒号后的第一个 { 再做花括号
// 配对，把其中那个「本身是合法 JSON」的子对象截出来。
//
// 不过「没有引号」只是当前打包器的选择，不是契约，所以解析时带引号的写法也要认。
const (
	bootstrapMarker     = "PRODUCT_SELECTION_BOOTSTRAP"
	productSelectionKey = "productSelectionData"
)

// FetchProducts 抓取 region 站点上 family 机型的全部可购买配置。
//
// family 是购买页 slug（如 iphone-17），与 model.Region.Families 中的取值一致。
func (c *Client) FetchProducts(ctx context.Context, region model.Region, family string) ([]model.Product, error) {
	if strings.TrimSpace(family) == "" {
		return nil, errors.New("机型 slug 为空")
	}

	page, err := c.getPage(ctx, region.BuyPageURL(family), region)
	if err != nil {
		return nil, fmt.Errorf("抓取 %s 购买页失败: %w", family, err)
	}

	raw, err := extractProductSelectionData(page)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 购买页失败: %w", family, err)
	}

	products, err := ParseProductSelection(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 购买页失败: %w", family, err)
	}
	if len(products) == 0 {
		return nil, fmt.Errorf("%w: %s 购买页没有解析出任何商品", ErrUnexpectedSchema, family)
	}
	return products, nil
}

// getPage 抓取一个 HTML 页面，重试与退避策略同 get。
//
// 之所以不能直接复用 get：doOnce 写死了 Accept: application/json 和
// X-Requested-With: XMLHttpRequest（client.go:234、:237），那是查询接口的
// 请求特征，用它去取一个普通网页反而更像脚本。但底层 http.Client、UA 和
// 全局限速仍然共用同一份 —— 绝不能像上游 services/listen.go:221 那样
// 每次请求都 gorequest.New() 造一个新的 Transport。
func (c *Client) getPage(ctx context.Context, endpoint string, region model.Region) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		if err := c.throttle(ctx); err != nil {
			return nil, err
		}

		body, err := c.doPageOnce(ctx, endpoint, region)
		if err == nil {
			return body, nil
		}
		lastErr = err

		if !errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrBlocked) {
			return nil, err
		}
	}
	return nil, lastErr
}

// doPageOnce 执行单次取页请求，错误分类与 doOnce 保持一致。
func (c *Client) doPageOnce(ctx context.Context, endpoint string, region model.Region) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", acceptLanguage(region.Locale))
	req.Header.Set("Referer", region.BaseURL+"/")
	// 刻意不设置 Accept-Encoding：交给 Transport 自动协商 gzip 并透明解压，
	// 手动指定反而会拿到一坨没人解压的压缩字节。

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		// 读干净再关闭，连接才能归还给连接池。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, fmt.Errorf("%w: HTTP 200 但响应体为空", ErrBlocked)
		}
		// 这里不校验内容是不是真的商品页：拦截页同样是 HTML，靠 Content-Type
		// 分不出来。真假交给后面的解析步骤判定，失败会归到 ErrUnexpectedSchema。
		return body, nil
	case resp.StatusCode == 541:
		return nil, fmt.Errorf("%w: HTTP 541", ErrBlocked)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: HTTP 429", ErrRateLimited)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: HTTP 403", ErrBlocked)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", ErrRateLimited, resp.StatusCode)
	default:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// extractProductSelectionData 从购买页 HTML 中截出 productSelectionData 对象。
func extractProductSelectionData(page []byte) ([]byte, error) {
	// 先定位到承载数据的全局变量，避免命中页面里别处同名的键。
	// 万一 Apple 改了变量名，退化成全页搜索这个键，多撑一阵子。
	search := page
	offset := 0
	if i := bytes.Index(page, []byte(bootstrapMarker)); i >= 0 {
		search = page[i:]
		offset = i
	}

	// 必须把所有同名位置都试一遍，而不是只认第一个。
	// 原来只做一次 bytes.Index：页面里只要先出现一段含有 productSelectionData 文本的
	// 字符串字面量（埋点参数、提示文案里出现这种字面量再正常不过），定位就停在那里，
	// 发现后面不是冒号便直接判整页失败，真正的属性再也没机会被看到。表现是购买页
	// 明明带着完整数据，程序却一口咬定「结构不符」，用户对着空目录无从下手。
	var lastErr error
	for from := 0; from < len(search); {
		rel := bytes.Index(search[from:], []byte(productSelectionKey))
		if rel < 0 {
			break
		}
		k := from + rel
		from = k + len(productSelectionKey)

		raw, err := selectionValueAt(search, k, offset)
		if err != nil {
			// 单个候选不成立只说明这里不是那个属性，接着找下一个。
			lastErr = err
			continue
		}
		return raw, nil
	}

	// 全部候选都不成立时，报最后一个候选的具体原因；一个候选都没有才是「找不到」。
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: 页面中找不到 %s", ErrUnexpectedSchema, productSelectionKey)
}

// selectionValueAt 判断 search[k:] 处的 productSelectionKey 是不是一个真的属性名，
// 是则把它的值原样截出来。offset 只用于把出错位置换算回整页偏移，方便排查。
func selectionValueAt(search []byte, k, offset int) ([]byte, error) {
	// 前后都得是标识符边界。否则 legacy_productSelectionData、productSelectionDataV2
	// 这类把目标名包在里面的更长标识符也会算命中，进而把一份旧数据当成商品目录 ——
	// 那比报错还糟：界面上会摆出一批早已下架的型号，用户守着永远不会有货的零件号。
	if k > 0 && isIdentByte(search[k-1]) {
		return nil, fmt.Errorf("%w: 偏移 %d 处的 %s 只是更长标识符的一部分",
			ErrUnexpectedSchema, offset+k, productSelectionKey)
	}
	pos := k + len(productSelectionKey)
	if pos < len(search) && isIdentByte(search[pos]) {
		return nil, fmt.Errorf("%w: 偏移 %d 处的 %s 只是更长标识符的一部分",
			ErrUnexpectedSchema, offset+k, productSelectionKey)
	}

	pos = skipSpace(search, pos)
	// 键带不带引号都要认：JS 对象字面量里 productSelectionData: 和
	// "productSelectionData": 一样合法。原来只认不带引号那种，Apple 哪天顺手加上引号
	// （比如换个打包器），整页数据就会栽在「之后不是冒号」上。
	if pos < len(search) && (search[pos] == '"' || search[pos] == '\'') {
		pos = skipSpace(search, pos+1)
	}
	if pos >= len(search) || search[pos] != ':' {
		return nil, fmt.Errorf("%w: %s 之后不是冒号", ErrUnexpectedSchema, productSelectionKey)
	}
	pos = skipSpace(search, pos+1)
	if pos >= len(search) || search[pos] != '{' {
		return nil, fmt.Errorf("%w: %s 的值不是对象", ErrUnexpectedSchema, productSelectionKey)
	}

	end, err := matchObject(search, pos)
	if err != nil {
		return nil, fmt.Errorf("%w: 截取 %s 失败（起始偏移 %d）: %v",
			ErrUnexpectedSchema, productSelectionKey, offset+pos, err)
	}

	raw := search[pos:end]
	// 花括号配平不等于内容合法。补这一道校验，才能把「命中的其实是某段文案里的同名
	// 文本」这类假阳性挡在门外 —— 挡掉之后循环还能继续去找真正的属性，而不是抱着
	// 一段垃圾往下走，最后以「一个商品都解析不出来」收场。
	if err := json.Unmarshal(raw, new(json.RawMessage)); err != nil {
		return nil, fmt.Errorf("%w: %s 的值不是合法 JSON: %v",
			ErrUnexpectedSchema, productSelectionKey, err)
	}
	return raw, nil
}

// isIdentByte 判断一个字节能否出现在 JS 标识符中间。
func isIdentByte(b byte) bool {
	return b == '_' || b == '$' || isASCIIDigit(b) ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// matchObject 从 start 处的 { 开始做花括号配对，返回配对的 } 之后一位的下标。
//
// 必须跳过字符串字面量，否则商品数据里那些含有 { 的 HTML 片段
// （颜色的 image 字段就是整段 HTML）会把配对算错。按字节扫描对 UTF-8 是安全的：
// 多字节序列的每个字节最高位都是 1，永远不会等于这里关心的 ASCII 定界符。
func matchObject(b []byte, start int) (int, error) {
	depth := 0
	inString := false
	var quote byte
	escaped := false

	for i := start; i < len(b); i++ {
		ch := b[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == quote:
				inString = false
			}
			continue
		}
		switch ch {
		case '"', '\'':
			inString = true
			quote = ch
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		}
	}
	return 0, errors.New("花括号未闭合")
}

// productSelection 是购买页商品数据中用得到的那部分字段。
type productSelection struct {
	Products []struct {
		PartNumber        string `json:"partNumber"`
		FamilyType        string `json:"familyType"`
		DimensionCapacity string `json:"dimensionCapacity"`
		DimensionColor    string `json:"dimensionColor"`
	} `json:"products"`

	DisplayValues struct {
		// 用 RawMessage 是因为这张表里混着非颜色的条目：title 是
		// {"singleVariantDisplayTitle": ...}，variantOrder 干脆是个数组。
		// 直接映射成结构体会让整个对象反序列化失败。
		DimensionColor map[string]json.RawMessage `json:"dimensionColor"`
	} `json:"displayValues"`
}

// ParseProductSelection 把一个 productSelectionData 对象解析成商品列表。
//
// 导出是为了让 catalog 包用同一份实现去解析内置的离线快照 —— 那些
// products_<locale>.json 的元素就是这里的同一种结构，没有理由维护两套解析。
func ParseProductSelection(raw []byte) ([]model.Product, error) {
	var data productSelection
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnexpectedSchema, err)
	}

	colors := make(map[string]string, len(data.DisplayValues.DimensionColor))
	for key, rawValue := range data.DisplayValues.DimensionColor {
		var entry struct {
			Value string `json:"value"`
		}
		// 解不出 value 的条目就是上面说的 title / variantOrder，跳过即可。
		if err := json.Unmarshal(rawValue, &entry); err != nil || entry.Value == "" {
			continue
		}
		colors[key] = entry.Value
	}

	products := make([]model.Product, 0, len(data.Products))
	seen := make(map[string]struct{}, len(data.Products))

	for _, item := range data.Products {
		if item.PartNumber == "" {
			continue
		}
		// 同一零件号会重复出现：日本站把同一台机器按运营商（SOFTBANK_IPHONE17）
		// 和无锁版（UNLOCKED_JP）各列了一遍，零件号完全相同。查库存只认零件号，
		// 留一条就够，否则界面上会出现一模一样的两行。
		if _, dup := seen[item.PartNumber]; dup {
			continue
		}
		seen[item.PartNumber] = struct{}{}

		colorName := colors[item.DimensionColor]
		if colorName == "" {
			colorName = item.DimensionColor
		}

		products = append(products, model.Product{
			PartNumber: item.PartNumber,
			Family:     item.FamilyType,
			Capacity:   item.DimensionCapacity,
			Color:      item.DimensionColor,
			Title: joinNonEmpty(" ",
				FamilyDisplayName(item.FamilyType),
				NormalizeCapacity(item.DimensionCapacity),
				colorName,
			),
		})
	}
	return products, nil
}

func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// NormalizeCapacity 把 Apple 的容量标识规范成展示形式，如 512gb → 512GB。
func NormalizeCapacity(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, unit := range []string{"tb", "gb", "mb"} {
		if strings.HasSuffix(lower, unit) {
			return strings.TrimSpace(lower[:len(lower)-len(unit)]) + strings.ToUpper(unit)
		}
	}
	return trimmed
}

// familyWords 是机型标识里可识别的词，按顺序尝试匹配。
//
// 拆成单词而不是穷举全部机型，是为了新机发布时不必改代码：
// iphone17promax 会被拆成 pro + max，未来的 iphone18pro 同样成立。
var familyWords = []struct{ token, display string }{
	{"pro", "Pro"},
	{"max", "Max"},
	{"plus", "Plus"},
	{"mini", "mini"},
	{"air", "Air"},
	{"se", "SE"},
	{"e", "e"},
}

// FamilyDisplayName 把 familyType 转成人类可读的机型名，如 iphone17promax → iPhone 17 Pro Max。
func FamilyDisplayName(familyType string) string {
	lower := strings.ToLower(strings.TrimSpace(familyType))
	if lower == "" {
		return ""
	}
	if !strings.HasPrefix(lower, "iphone") {
		// 不认识的前缀原样返回，总比拼出个错名字强。
		return strings.TrimSpace(familyType)
	}

	rest := lower[len("iphone"):]
	tokens := []string{"iPhone"}

	for i := 0; i < len(rest); {
		if isASCIIDigit(rest[i]) {
			j := i
			for j < len(rest) && isASCIIDigit(rest[j]) {
				j++
			}
			tokens = append(tokens, rest[i:j])
			i = j
			continue
		}

		matched := false
		for _, w := range familyWords {
			if !strings.HasPrefix(rest[i:], w.token) {
				continue
			}
			// 「16e」这类后缀是紧贴数字的，中间不加空格。
			if w.token == "e" && len(tokens) > 1 && isASCIIDigit(tokens[len(tokens)-1][0]) {
				tokens[len(tokens)-1] += w.display
			} else {
				tokens = append(tokens, w.display)
			}
			i += len(w.token)
			matched = true
			break
		}
		if matched {
			continue
		}

		// 遇到没登记的词，整段保留并首字母大写，至少不丢信息。
		j := i
		for j < len(rest) && !isASCIIDigit(rest[j]) {
			j++
		}
		tokens = append(tokens, strings.ToUpper(rest[i:i+1])+rest[i+1:j])
		i = j
	}

	return strings.Join(tokens, " ")
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }
