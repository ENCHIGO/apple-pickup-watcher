// Package apple 封装对 Apple 在线商店公开接口的访问。
//
// 这里只做「发请求 + 解析响应 + 分类错误」三件事，不含任何调度或界面逻辑，
// 因此可以脱离 GUI 单独测试。
package apple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// 可被 errors.Is 判定的错误分类。调度层据此决定是退避、告警还是直接放弃。
var (
	// ErrBlocked 表示请求被 Apple 边缘节点拦截，而不是门店真的没货。
	//
	// 上游项目正是死在这里：它使用的 /shop/fulfillment-messages 接口现在
	// 对任意请求恒定返回 HTTP 541 加一个 128002 字节的「Page Not Found」
	// HTML 拦截页（中国大陆站与美国站响应完全一致，且同一时刻 apple.com.cn
	// 首页正常返回 200，可排除 IP 封禁）。上游把这个错误当成「无货」处理，
	// 于是所有用户看到的都是一屏永远不会变的「无货」。
	ErrBlocked = errors.New("请求被 Apple 拦截")

	// ErrRateLimited 表示触发了频率限制，应当退避后重试。
	ErrRateLimited = errors.New("请求过于频繁被限流")

	// ErrUnexpectedSchema 表示响应能解析成 JSON，但结构与预期不符，
	// 通常意味着 Apple 又改了接口。这种情况必须让用户看见，而不是静默当成无货。
	ErrUnexpectedSchema = errors.New("接口返回结构与预期不符")

	// ErrAppleError 表示 Apple 明确返回了一条业务错误信息。
	ErrAppleError = errors.New("Apple 返回错误")
)

// DefaultUserAgent 使用一个当前主流浏览器的 UA。
//
// 上游写死的是 Chrome/94（2021 年），这种年代久远的 UA 本身就是明显的
// 机器人特征。UA 不是被拦的唯一原因，但没有理由主动留下这个特征。
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// Option 用于定制 Client。
type Option func(*Client)

// WithHTTPClient 替换底层 http.Client，主要给测试用。
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

// WithUserAgent 覆盖默认 UA。
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithMinInterval 设置任意两次出站请求之间的最小间隔，用于全局限速。
func WithMinInterval(d time.Duration) Option {
	return func(c *Client) { c.minInterval = d }
}

// WithMaxRetries 设置单次调用内部的最大重试次数（不含首次请求）。
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// Client 是并发安全的 Apple 商店接口客户端。
//
// 必须复用同一个实例：上游为每次查询都调用 gorequest.New()
// （services/listen.go:221），每次都构造出全新的 http.Client 和 Transport，
// 连接池完全无法复用，空闲连接和相关 goroutine 持续堆积，配合它 500ms
// 一轮的轮询（services/listen.go:154），几小时就能涨到十几 GB 内存。
type Client struct {
	hc          *http.Client
	userAgent   string
	minInterval time.Duration
	maxRetries  int

	mu       sync.Mutex
	lastSent time.Time
}

// NewClient 构造一个 Client。返回的实例应当在整个进程生命周期内复用。
func NewClient(opts ...Option) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	c := &Client{
		hc: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
		},
		userAgent:   DefaultUserAgent,
		minInterval: 500 * time.Millisecond,
		maxRetries:  2,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// PartStatus 是单个零件号在单个门店的查询结果。
type PartStatus struct {
	PartNumber string
	// Availability 只有在 Apple 明确作答时才会是 InStock/OutOfStock。
	Availability model.Availability
	// ProductTitle 是 Apple 返回的商品名，可用于校验本地目录是否过期。
	ProductTitle string
	// PickupDisplay 是原始字段值（available / unavailable / ineligible 等），
	// 保留下来便于排查问题和适配未来新增的取值。
	PickupDisplay string
	// Recognized 表示 PickupDisplay 是我们认识的取值。
	//
	// 为假时 Availability 一定是 Unknown，但这个 Unknown 的含义是「Apple 换了词表」
	// 而不是「Apple 说不知道」。调用方必须把它当成接口漂移来处理：如果只看
	// Availability，一次静默的字段改名会被当作正常作答，界面上既没有错误原因，
	// 也不会触发告警，甚至会把之前亮起的告警条误判成「已恢复」。
	Recognized bool
}

// StoreAvailability 是单个门店的查询结果。
type StoreAvailability struct {
	StoreNumber string
	StoreName   string
	Parts       map[string]PartStatus
}

// PickupMessage 查询 storeNumber 门店中 parts 各型号的可取货状态。
//
// 一次请求可以携带多个零件号，Apple 会在同一响应里返回全部结果，因此
// 调用方应当按门店聚合后再调用，而不是每个型号发一次请求。
func (c *Client) PickupMessage(ctx context.Context, region model.Region, storeNumber string, parts []string) (*StoreAvailability, error) {
	if storeNumber == "" {
		return nil, errors.New("门店编号为空")
	}
	if len(parts) == 0 {
		return nil, errors.New("零件号列表为空")
	}

	q := url.Values{}
	q.Set("pl", "true")
	q.Set("mts.0", "regular")
	q.Set("store", storeNumber)
	for i, part := range parts {
		q.Set("parts."+strconv.Itoa(i), part)
	}
	endpoint := region.PickupMessageURL() + "?" + q.Encode()

	body, err := c.get(ctx, endpoint, region)
	if err != nil {
		return nil, err
	}
	return parsePickupMessage(body, storeNumber)
}

// get 执行一次带限速与退避重试的 GET，返回响应体。
func (c *Client) get(ctx context.Context, endpoint string, region model.Region) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避。被拦截或限流时继续以原频率猛冲只会让情况更糟。
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

		body, err := c.doOnce(ctx, endpoint, region)
		if err == nil {
			return body, nil
		}
		lastErr = err

		// 只有可能自愈的错误才值得重试。结构不符和业务错误重试多少次都一样。
		if !errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrBlocked) {
			return nil, err
		}
	}
	return nil, lastErr
}

// throttle 保证任意两次出站请求之间至少间隔 minInterval。
func (c *Client) throttle(ctx context.Context) error {
	c.mu.Lock()
	wait := time.Duration(0)
	now := time.Now()
	if !c.lastSent.IsZero() {
		if elapsed := now.Sub(c.lastSent); elapsed < c.minInterval {
			wait = c.minInterval - elapsed
		}
	}
	c.lastSent = now.Add(wait)
	c.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// doOnce 执行单次 HTTP 请求，并把 HTTP 层面的失败归类成本包定义的错误。
func (c *Client) doOnce(ctx context.Context, endpoint string, region model.Region) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", acceptLanguage(region.Locale))
	req.Header.Set("Referer", region.BaseURL+"/shop/buy-iphone")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		// 必须读干净再关闭，否则连接无法归还给连接池复用。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	// 响应体设上限，避免异常情况下把整个拦截页甚至更大的内容读进内存。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		// 状态码 200 也未必是 JSON：被拦截时可能返回 HTML。
		if !looksLikeJSON(resp.Header.Get("Content-Type"), body) {
			return nil, fmt.Errorf("%w: HTTP 200 但响应不是 JSON", ErrBlocked)
		}
		return body, nil
	case resp.StatusCode == 541:
		// 541 是 Apple 自定义的拦截状态码，不是标准 HTTP 状态码。
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

func acceptLanguage(locale string) string {
	switch locale {
	case "zh_CN":
		return "zh-CN,zh;q=0.9"
	case "zh_HK":
		return "zh-HK,zh;q=0.9"
	case "zh_TW":
		return "zh-TW,zh;q=0.9"
	case "ja_JP":
		return "ja-JP,ja;q=0.9"
	default:
		return "en-US,en;q=0.9"
	}
}

func looksLikeJSON(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	trimmed := strings.TrimLeft(string(body[:min(len(body), 64)]), " \t\r\n")
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// pickupResponse 对应 /shop/retail/pickup-message 的响应结构。
//
// 只声明用得到的字段。Apple 的响应有几十个字段，全部映射既无必要也更易随
// 接口调整而失配。
type pickupResponse struct {
	Head struct {
		Status string `json:"status"`
	} `json:"head"`
	Body struct {
		Stores       []pickupStore `json:"stores"`
		ErrorMessage *string       `json:"errorMessage"`
		// Content 是旧版 fulfillment-messages 的嵌套结构，留作兜底，
		// 以防 Apple 把数据挪回这个位置。
		Content struct {
			PickupMessage struct {
				Stores []pickupStore `json:"stores"`
			} `json:"pickupMessage"`
		} `json:"content"`
	} `json:"body"`
}

type pickupStore struct {
	StoreNumber       string                `json:"storeNumber"`
	StoreName         string                `json:"storeName"`
	PartsAvailability map[string]pickupPart `json:"partsAvailability"`
}

type pickupPart struct {
	PartNumber    string `json:"partNumber"`
	PickupDisplay string `json:"pickupDisplay"`
	MessageTypes  struct {
		Regular struct {
			StorePickupProductTitle string `json:"storePickupProductTitle"`
			StoreSelectionEnabled   bool   `json:"storeSelectionEnabled"`
		} `json:"regular"`
	} `json:"messageTypes"`
}

func parsePickupMessage(raw []byte, wantStore string) (*StoreAvailability, error) {
	var resp pickupResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnexpectedSchema, err)
	}

	stores := resp.Body.Stores
	if len(stores) == 0 {
		stores = resp.Body.Content.PickupMessage.Stores
	}
	if len(stores) == 0 {
		if msg := resp.Body.ErrorMessage; msg != nil && *msg != "" {
			return nil, fmt.Errorf("%w: %s", ErrAppleError, *msg)
		}
		return nil, fmt.Errorf("%w: 响应中没有门店数据", ErrUnexpectedSchema)
	}

	// 指定了 store 参数时 Apple 只返回该门店，但仍按编号核对，
	// 避免把别的门店的库存错认成目标门店的。
	var matched *pickupStore
	for i := range stores {
		if stores[i].StoreNumber == wantStore {
			matched = &stores[i]
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("%w: 响应中没有门店 %s", ErrUnexpectedSchema, wantStore)
	}
	if len(matched.PartsAvailability) == 0 {
		return nil, fmt.Errorf("%w: 门店 %s 没有返回任何型号状态", ErrUnexpectedSchema, wantStore)
	}

	result := &StoreAvailability{
		StoreNumber: matched.StoreNumber,
		StoreName:   matched.StoreName,
		Parts:       make(map[string]PartStatus, len(matched.PartsAvailability)),
	}
	for part, info := range matched.PartsAvailability {
		availability, recognized := availabilityFrom(info.PickupDisplay)
		result.Parts[part] = PartStatus{
			PartNumber:    part,
			Availability:  availability,
			ProductTitle:  info.MessageTypes.Regular.StorePickupProductTitle,
			PickupDisplay: info.PickupDisplay,
			Recognized:    recognized,
		}
	}
	return result, nil
}

// availabilityFrom 把 Apple 的 pickupDisplay 字段翻译成三态，
// 第二个返回值表示这个取值是否在我们已知的词表里。
//
// 已实际观测到的取值：available（可取货）、unavailable（不可取货）、
// ineligible（该型号在此门店不支持到店取货）。未知取值一律归为 Unknown
// 而非 OutOfStock —— 猜错成「无货」会让用户错过机会，猜错成「未知」只是
// 让用户多看一眼。
//
// 但「不认识」必须能被调用方区分出来。字段被改名或新增取值时，这里会安静地
// 退回 Unknown；若调用方把它当成一次正常作答，接口漂移就会以「一切正常，只是
// 状态未知」的面目出现，这跟上游那个「一切正常，只是全都无货」的失效方式
// 是同一类错误。
func availabilityFrom(pickupDisplay string) (model.Availability, bool) {
	switch strings.ToLower(strings.TrimSpace(pickupDisplay)) {
	case "available":
		return model.InStock, true
	case "unavailable", "ineligible":
		return model.OutOfStock, true
	default:
		return model.Unknown, false
	}
}
