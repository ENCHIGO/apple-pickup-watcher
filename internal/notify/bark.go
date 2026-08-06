package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// barkDefaultTimeout 是自建客户端的超时。
//
// 上游用 http.Get（services/listen.go:300），走的是 http.DefaultClient，
// 而 DefaultClient 没有任何超时：Bark 服务器只连上不回包时，那个 goroutine
// 会一直挂着不释放。
const barkDefaultTimeout = 10 * time.Second

// Bark 通过 Bark 服务器向 iOS 设备推送通知。
type Bark struct {
	baseURL string
	hc      *http.Client
}

// NewBark 构造 Bark 渠道。
//
// baseURL 是 Bark App 里给出的推送地址，形如 https://api.day.app/<设备key>，
// 也可以是自建服务器地址；为空表示用户没有配置，此时 Notify 直接返回 nil。
// hc 应当传入与其他出站请求共用的 *http.Client 以复用连接池；传 nil 时回退到
// 一个带超时的独立客户端。
func NewBark(baseURL string, hc *http.Client) *Bark {
	if hc == nil {
		hc = &http.Client{Timeout: barkDefaultTimeout}
	}
	return &Bark{baseURL: strings.TrimSpace(baseURL), hc: hc}
}

// Name 实现 Notifier。
func (b *Bark) Name() string { return "Bark" }

// Notify 发送一条 Bark 推送。
func (b *Bark) Notify(ctx context.Context, n Notification) error {
	if b == nil || b.baseURL == "" {
		return nil
	}

	endpoint, err := b.buildURL(n)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("构造 Bark 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.hc.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Bark 服务器失败: %w", err)
	}
	defer func() {
		// 必须读干净再关闭，否则这条连接无法归还给连接池复用。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	// 错误信息通常很短，读几 KB 足够；同时避免异常响应把内存吃掉。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return fmt.Errorf("读取 Bark 响应失败: %w", err)
	}

	// 上游拿到响应后连状态码都不看（services/listen.go:300-304），设备 key 写错、
	// 服务器返回 400 时用户毫无察觉，一直以为推送生效了，真到货那天才发现没收到。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Bark 服务器返回 HTTP %d: %s", resp.StatusCode, summarize(body))
	}

	// 部分 Bark 部署在 HTTP 200 的响应体里用 code 字段报错，一并检查。
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Code != 0 && (payload.Code < 200 || payload.Code >= 300) {
		return fmt.Errorf("Bark 服务器返回错误 %d: %s", payload.Code, payload.Message)
	}
	return nil
}

// buildURL 组装推送地址。
//
// 上游用 fmt.Sprintf("%s/%s/%s?url=%s", ...) 把标题和正文直接拼进路径
// （services/listen.go:298），中文、空格、斜杠一概不转义 —— 拼出来的 URL 本身
// 就是非法的，标题里只要有个「/」整条路径的语义就变了；末尾的购物袋地址也没做
// query 转义，其中的 & 会把参数截断。这里路径段一律走 url.PathEscape，查询参数
// 交给 url.Values 编码。
func (b *Bark) buildURL(n Notification) (string, error) {
	u, err := url.Parse(b.baseURL)
	if err != nil {
		return "", fmt.Errorf("Bark 地址无法解析: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("Bark 地址必须是以 http:// 或 https:// 开头的完整地址，当前为 %q", b.baseURL)
	}

	// Bark 的路径形式是 /<key>/<标题>/<正文>；只给一段时它被当作正文。
	// 因此空标题要整段跳过，而不是留下一个空路径段变成 //。
	segments := make([]string, 0, 2)
	for _, s := range []string{n.Title, n.Body} {
		if s = strings.TrimSpace(s); s != "" {
			segments = append(segments, s)
		}
	}
	if len(segments) == 0 {
		return "", errors.New("通知的标题和正文都为空")
	}
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}

	// Path 存未转义形式、RawPath 存转义形式，是 net/url 约定的配合方式：
	// 只设 Path 的话 URL.String 会按自己的规则重新转义，而它不会转义「/」，
	// 标题里的斜杠就会被当成路径分隔符。两个基准都要在改动 u.Path 之前取，
	// 因为 EscapedPath 是按当前 Path 现算的。
	basePath := strings.TrimRight(u.Path, "/")
	baseRawPath := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = basePath + "/" + strings.Join(segments, "/")
	u.RawPath = baseRawPath + "/" + strings.Join(escaped, "/")

	// 保留用户自己写在地址里的参数，例如 ?group=库存 或 ?sound=alarm。
	//
	// 这里不能用 u.Query()：它会把 ParseQuery 的错误直接吞掉并返回空集合。
	// 于是 ?group=a;sound=b（分号分隔，Go 1.17 起被拒绝）或 ?%zz=1（非法百分号
	// 转义）这类地址会让用户写的全部参数被静默丢弃 —— 推送照发，但分组、铃声、
	// 图标等设置无声失效，用户完全无从察觉。解析不了就原样保留，只把 url 追加
	// 上去：宁可多带一个可能重复的参数，也不要悄悄改掉用户的意图。
	link := strings.TrimSpace(n.URL)
	if q, err := url.ParseQuery(u.RawQuery); err == nil {
		if link != "" {
			q.Set("url", link)
		}
		u.RawQuery = q.Encode()
	} else if link != "" {
		appended := "url=" + url.QueryEscape(link)
		if u.RawQuery == "" {
			u.RawQuery = appended
		} else {
			u.RawQuery += "&" + appended
		}
	}
	return u.String(), nil
}

// summarize 把响应体压成一行短文本，用于错误信息。
//
// 服务端出错时可能返回整页 HTML，原样塞进 error 会让日志和界面都没法看。
func summarize(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if text == "" {
		return "(空响应)"
	}
	const limit = 120
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
