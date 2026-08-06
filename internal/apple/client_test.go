package apple_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/apple"
	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// 测试用的门店号与零件号，取真实格式，便于对照抓包结果。
const (
	testStore = "R683"
	partA     = "MG724CH/A"
	partB     = "MG834CH/A"
)

// newTestClient 构造一个专供测试的客户端。
//
// 默认的 500ms 全局限速和 1s/2s 指数退避是给真实网络准备的，直接用会让整个
// 测试套件白白慢上几十秒。所以这里默认关掉限速、关掉重试；需要验证退避重试
// 行为的用例再自己用 WithMaxRetries 打开。
func newTestClient(opts ...apple.Option) *apple.Client {
	base := []apple.Option{apple.WithMinInterval(0), apple.WithMaxRetries(0)}
	return apple.NewClient(append(base, opts...)...)
}

// testRegion 让 Region.PickupMessageURL() 指向本地 httptest 服务器。
//
// 测试绝不能真的去请求 apple.com：既慢又不可复现，还会平白给 Apple 侧的风控
// 画像添一笔。上游一个测试都没有，接口失效大半年无人察觉，正是因为没有这一层
// 可离线复现的响应样本。
func testRegion(srv *httptest.Server) model.Region {
	return model.Region{Title: "测试地区", Locale: "zh_CN", BaseURL: srv.URL}
}

func startServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// startJSONServer 让服务器对任何请求都返回同一段 JSON。
func startJSONServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return startServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

// pickupJSON 按 /shop/retail/pickup-message 的真实响应结构生成响应体。
//
// 字段布局照抄实测结果：body.stores[].partsAvailability.<零件号>.pickupDisplay，
// messageTypes 下只有 regular（没有 compact）。把它固化成测试夹具，Apple 哪天
// 再改结构，这里的用例会立刻变红，而不是等用户来报「一直显示无货」。
func pickupJSON(t *testing.T, storeNumber, storeName string, parts map[string]string) string {
	t.Helper()
	return storesJSON(t, storeJSON(t, storeNumber, storeName, parts))
}

func storeJSON(t *testing.T, storeNumber, storeName string, parts map[string]string) string {
	t.Helper()
	items := make([]string, 0, len(parts))
	for part, display := range parts {
		items = append(items, fmt.Sprintf(`%q:{
			"partNumber":%q,
			"pickupDisplay":%q,
			"messageTypes":{
				"regular":{
					"storePickupProductTitle":"iPhone 17 512GB 黑色",
					"storeSelectionEnabled":true
				}
			}
		}`, part, part, display))
	}
	return fmt.Sprintf(`{
		"storeNumber":%q,
		"storeName":%q,
		"partsAvailability":{%s}
	}`, storeNumber, storeName, strings.Join(items, ","))
}

func storesJSON(t *testing.T, stores ...string) string {
	t.Helper()
	body := fmt.Sprintf(`{
		"head":{"status":"200"},
		"body":{"stores":[%s]}
	}`, strings.Join(stores, ","))
	// 夹具本身写错了 JSON 会让用例失败在莫名其妙的地方，先自检一次。
	if !json.Valid([]byte(body)) {
		t.Fatalf("测试夹具不是合法 JSON:\n%s", body)
	}
	return body
}

func TestPickupMessageMapsPickupDisplay(t *testing.T) {
	cases := []struct {
		display string
		want    model.Availability
	}{
		{"available", model.InStock},
		{"unavailable", model.OutOfStock},
		{"ineligible", model.OutOfStock},
		// Apple 各地区站点的大小写并不完全统一，翻译前会做 TrimSpace + ToLower。
		{"AVAILABLE", model.InStock},
		{"  Unavailable  ", model.OutOfStock},
	}

	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.display), func(t *testing.T) {
			srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{partA: tc.display}))
			c := newTestClient()

			got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
			if err != nil {
				t.Fatalf("PickupMessage 返回错误: %v", err)
			}
			if got.StoreNumber != testStore {
				t.Errorf("StoreNumber = %q, 期望 %q", got.StoreNumber, testStore)
			}
			if got.StoreName != "环球港" {
				t.Errorf("StoreName = %q, 期望 %q", got.StoreName, "环球港")
			}
			status, ok := got.Parts[partA]
			if !ok {
				t.Fatalf("结果中没有零件号 %s，实际为 %+v", partA, got.Parts)
			}
			if status.Availability != tc.want {
				t.Errorf("pickupDisplay=%q 解析为 %v, 期望 %v", tc.display, status.Availability, tc.want)
			}
			// 原始字段必须原样保留，否则接口一改就没法排查了。
			if status.PickupDisplay != tc.display {
				t.Errorf("PickupDisplay = %q, 期望原样保留 %q", status.PickupDisplay, tc.display)
			}
			if status.PartNumber != partA {
				t.Errorf("PartNumber = %q, 期望 %q", status.PartNumber, partA)
			}
			if status.ProductTitle != "iPhone 17 512GB 黑色" {
				t.Errorf("ProductTitle = %q, 期望取自 messageTypes.regular", status.ProductTitle)
			}
		})
	}
}

// TestPickupMessageUnknownDisplayNeverBecomesOutOfStock 守护本项目的核心不变量。
//
// 上游把任何非 available 的情况都折叠成「无货」，于是 Apple 一改字段取值，
// 用户看到的就是一屏永远不会变的「无货」。猜错成「无货」会让用户错过机会，
// 猜错成「未知」只是让用户多看一眼——这条不变量一旦破了，整个程序就退化成
// 上游那个样子，所以必须有测试盯着。
func TestPickupMessageUnknownDisplayNeverBecomesOutOfStock(t *testing.T) {
	displays := []string{
		"",
		"unknown",
		"comingSoon",
		"backordered",
		"AVAILABLE_SOON",
		"available-later",
		"0",
	}

	for _, display := range displays {
		name := display
		if name == "" {
			name = "空字符串"
		}
		t.Run(name, func(t *testing.T) {
			srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{partA: display}))
			c := newTestClient()

			got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
			if err != nil {
				t.Fatalf("PickupMessage 返回错误: %v", err)
			}
			status := got.Parts[partA]
			if status.Availability == model.OutOfStock {
				t.Fatalf("未知取值 %q 被解析成了 OutOfStock，这正是上游的致命缺陷", display)
			}
			if status.Availability != model.Unknown {
				t.Fatalf("未知取值 %q 解析为 %v, 期望 Unknown", display, status.Availability)
			}
		})
	}
}

func TestPickupMessageMultiplePartsInOneRequest(t *testing.T) {
	srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{
		partA: "available",
		partB: "unavailable",
	}))
	c := newTestClient()

	got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA, partB})
	if err != nil {
		t.Fatalf("PickupMessage 返回错误: %v", err)
	}
	if len(got.Parts) != 2 {
		t.Fatalf("Parts 数量 = %d, 期望 2: %+v", len(got.Parts), got.Parts)
	}
	if got.Parts[partA].Availability != model.InStock {
		t.Errorf("%s = %v, 期望 InStock", partA, got.Parts[partA].Availability)
	}
	if got.Parts[partB].Availability != model.OutOfStock {
		t.Errorf("%s = %v, 期望 OutOfStock", partB, got.Parts[partB].Availability)
	}
}

// TestPickupMessageQueryCarriesAllParts 验证多零件号确实以 parts.N 的形式带上了。
//
// 按门店聚合、一次请求带多个零件号，是把请求量压到最低的关键；如果 query 拼错，
// 表现是「只有第一个型号有结果」，很容易被误当成「其他型号无货」。
func TestPickupMessageQueryCarriesAllParts(t *testing.T) {
	type recorded struct {
		path  string
		query url.Values
		hdr   http.Header
	}
	reqs := make(chan recorded, 4)

	srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
		reqs <- recorded{path: r.URL.Path, query: r.URL.Query(), hdr: r.Header.Clone()}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, pickupJSON(t, testStore, "环球港", map[string]string{
			partA: "available",
			partB: "unavailable",
		}))
	})
	c := newTestClient()

	if _, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA, partB}); err != nil {
		t.Fatalf("PickupMessage 返回错误: %v", err)
	}

	select {
	case got := <-reqs:
		if got.path != "/shop/retail/pickup-message" {
			t.Errorf("请求路径 = %q, 期望 /shop/retail/pickup-message（上游用的 /shop/fulfillment-messages 已恒返回 541）", got.path)
		}
		if v := got.query.Get("parts.0"); v != partA {
			t.Errorf("parts.0 = %q, 期望 %q", v, partA)
		}
		if v := got.query.Get("parts.1"); v != partB {
			t.Errorf("parts.1 = %q, 期望 %q", v, partB)
		}
		if v := got.query.Get("store"); v != testStore {
			t.Errorf("store = %q, 期望 %q", v, testStore)
		}
		if v := got.query.Get("pl"); v != "true" {
			t.Errorf("pl = %q, 期望 true", v)
		}
		// messageTypes 下只有 regular，mts.0 必须是 regular，写成 compact 会拿不到数据。
		if v := got.query.Get("mts.0"); v != "regular" {
			t.Errorf("mts.0 = %q, 期望 regular", v)
		}
		if ua := got.hdr.Get("User-Agent"); ua == "" || strings.Contains(ua, "Chrome/94") {
			t.Errorf("User-Agent = %q, 不应为空也不应沿用上游 2021 年的 Chrome/94", ua)
		}
		if al := got.hdr.Get("Accept-Language"); !strings.HasPrefix(al, "zh-CN") {
			t.Errorf("Accept-Language = %q, 期望按地区 locale 给出 zh-CN", al)
		}
	default:
		t.Fatal("服务器没有收到任何请求")
	}
}

// TestPickupMessageBlocked 覆盖 Apple 的两种拦截形态。
//
// 上游把这两种情况都当成了「无货」，这是它彻底失效却毫无提示的直接原因。
func TestPickupMessageBlocked(t *testing.T) {
	t.Run("HTTP 541 拦截页", func(t *testing.T) {
		srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			// 541 是 Apple 自定义状态码，实测响应体是一个 128KB 的 Page Not Found 页面。
			w.WriteHeader(541)
			_, _ = io.WriteString(w, "<html><head><title>Page Not Found</title></head><body></body></html>")
		})
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrBlocked) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrBlocked)", err)
		}
	})

	t.Run("HTTP 200 但返回 HTML", func(t *testing.T) {
		srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!DOCTYPE html><html><body>Page Not Found</body></html>")
		})
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrBlocked) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrBlocked)", err)
		}
	})

	t.Run("HTTP 200 但响应体为空", func(t *testing.T) {
		// 空响应体会走到 body[:min(len(body),64)] 这条路径，顺带守住不越界。
		srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
		})
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrBlocked) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrBlocked)", err)
		}
	})

	t.Run("HTTP 403", func(t *testing.T) {
		srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrBlocked) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrBlocked)", err)
		}
	})
}

// TestPickupMessageRetriesRecoverableErrors 验证限流/拦截确实触发了重试。
//
// 用 WithMaxRetries(1) 是因为退避是 1s、2s 的指数序列，重试次数一多测试就要跑好几秒。
func TestPickupMessageRetriesRecoverableErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"HTTP 429 限流", http.StatusTooManyRequests, apple.ErrRateLimited},
		{"HTTP 500", http.StatusInternalServerError, apple.ErrRateLimited},
		{"HTTP 503 服务不可用", http.StatusServiceUnavailable, apple.ErrRateLimited},
		{"HTTP 541 拦截", 541, apple.ErrBlocked},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var hits atomic.Int32
			srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tc.status)
			})
			c := newTestClient(apple.WithMaxRetries(1))

			_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, 期望满足 errors.Is(err, %v)", err, tc.wantErr)
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("服务器收到 %d 次请求, 期望 2 次（首次 + 1 次重试）", got)
			}
		})
	}
}

// TestPickupMessageRetrySucceeds 验证瞬时故障能被重试救回来，而不是直接判成查询失败。
func TestPickupMessageRetrySucceeds(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, pickupJSON(t, testStore, "环球港", map[string]string{partA: "available"}))
	})
	c := newTestClient(apple.WithMaxRetries(2))

	got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
	if err != nil {
		t.Fatalf("重试后仍然失败: %v", err)
	}
	if got.Parts[partA].Availability != model.InStock {
		t.Errorf("Availability = %v, 期望 InStock", got.Parts[partA].Availability)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("服务器收到 %d 次请求, 期望 2 次", n)
	}
}

// TestPickupMessageDoesNotRetryUnrecoverable 验证不会对没救的错误做无谓重试。
//
// 结构不符、404 这类错误重试多少次结果都一样，重试只是徒增请求量、把风控喂得更快。
func TestPickupMessageDoesNotRetryUnrecoverable(t *testing.T) {
	t.Run("HTTP 404", func(t *testing.T) {
		var hits atomic.Int32
		srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		})
		c := newTestClient(apple.WithMaxRetries(3))

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if err == nil {
			t.Fatal("期望返回错误")
		}
		if errors.Is(err, apple.ErrBlocked) || errors.Is(err, apple.ErrRateLimited) {
			t.Errorf("404 被归类成了 %v，期望是未分类错误", err)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("服务器收到 %d 次请求, 期望 1 次（不重试）", n)
		}
	})

	t.Run("结构不符", func(t *testing.T) {
		var hits atomic.Int32
		srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"head":{"status":"200"},"body":{"stores":[]}}`)
		})
		c := newTestClient(apple.WithMaxRetries(3))

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrUnexpectedSchema) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", err)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("服务器收到 %d 次请求, 期望 1 次（不重试）", n)
		}
	})
}

// TestPickupMessageUnexpectedSchema 覆盖各种「能解析成 JSON 但结构不对」的情况。
//
// 这些都必须报错，绝不能静默降级成「无货」——Apple 改接口时用户要第一时间知道。
func TestPickupMessageUnexpectedSchema(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"没有 stores 字段", `{"head":{"status":"200"},"body":{}}`},
		{"stores 为空数组", `{"head":{"status":"200"},"body":{"stores":[]}}`},
		{"畸形 JSON", `{"head":{"status":"200"`},
		{"响应是 JSON 数组", `[1,2,3]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startJSONServer(t, tc.body)
			c := newTestClient()

			_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
			if !errors.Is(err, apple.ErrUnexpectedSchema) {
				t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", err)
			}
		})
	}

	t.Run("缺少目标门店", func(t *testing.T) {
		srv := startJSONServer(t, pickupJSON(t, "R999", "别的店", map[string]string{partA: "available"}))
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrUnexpectedSchema) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", err)
		}
		if !strings.Contains(err.Error(), testStore) {
			t.Errorf("错误信息 %q 里应当带上找不到的门店号 %s", err, testStore)
		}
	})

	t.Run("门店没有 partsAvailability", func(t *testing.T) {
		srv := startJSONServer(t, storesJSON(t, fmt.Sprintf(`{"storeNumber":%q,"storeName":"环球港"}`, testStore)))
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrUnexpectedSchema) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", err)
		}
	})

	t.Run("partsAvailability 为空对象", func(t *testing.T) {
		srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", nil))
		c := newTestClient()

		_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if !errors.Is(err, apple.ErrUnexpectedSchema) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", err)
		}
	})
}

// TestPickupMessageStoreNumberMismatch 验证不会把别家门店的库存错认成目标门店的。
//
// Apple 带了 store 参数时通常只返回该门店，但「通常」不是保证。一旦认错门店，
// 用户会跑去一家其实没货的店，这种错误比漏报更糟。
func TestPickupMessageStoreNumberMismatch(t *testing.T) {
	t.Run("只返回了别的门店", func(t *testing.T) {
		srv := startJSONServer(t, pickupJSON(t, "R999", "别的店", map[string]string{partA: "available"}))
		c := newTestClient()

		got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if err == nil {
			t.Fatalf("门店号不匹配却返回了结果: %+v", got)
		}
		if !errors.Is(err, apple.ErrUnexpectedSchema) {
			t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrUnexpectedSchema)", err)
		}
	})

	t.Run("返回多个门店时取正确的那个", func(t *testing.T) {
		srv := startJSONServer(t, storesJSON(t,
			storeJSON(t, "R999", "别的店", map[string]string{partA: "available"}),
			storeJSON(t, testStore, "环球港", map[string]string{partA: "unavailable"}),
		))
		c := newTestClient()

		got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
		if err != nil {
			t.Fatalf("PickupMessage 返回错误: %v", err)
		}
		if got.StoreNumber != testStore {
			t.Fatalf("StoreNumber = %q, 期望 %q", got.StoreNumber, testStore)
		}
		if got.Parts[partA].Availability != model.OutOfStock {
			t.Errorf("Availability = %v, 期望取目标门店的 unavailable 而不是别家的 available",
				got.Parts[partA].Availability)
		}
	})
}

// TestPickupMessageAppleErrorMessage 验证 Apple 明确给出的业务错误被单独归类。
func TestPickupMessageAppleErrorMessage(t *testing.T) {
	srv := startJSONServer(t, `{"head":{"status":"200"},"body":{"stores":[],"errorMessage":"零件号无效"}}`)
	c := newTestClient()

	_, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
	if !errors.Is(err, apple.ErrAppleError) {
		t.Fatalf("err = %v, 期望满足 errors.Is(err, ErrAppleError)", err)
	}
	if !strings.Contains(err.Error(), "零件号无效") {
		t.Errorf("错误信息 %q 里应当带上 Apple 原始提示", err)
	}
}

// TestPickupMessageLegacyContentShape 覆盖旧版嵌套结构的兜底解析路径。
func TestPickupMessageLegacyContentShape(t *testing.T) {
	body := fmt.Sprintf(`{
		"head":{"status":"200"},
		"body":{"content":{"pickupMessage":{"stores":[%s]}}}
	}`, storeJSON(t, testStore, "环球港", map[string]string{partA: "available"}))
	srv := startJSONServer(t, body)
	c := newTestClient()

	got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA})
	if err != nil {
		t.Fatalf("PickupMessage 返回错误: %v", err)
	}
	if got.Parts[partA].Availability != model.InStock {
		t.Errorf("Availability = %v, 期望 InStock", got.Parts[partA].Availability)
	}
}

// TestPickupMessageMissingRequestedPart 验证响应少了某个零件号时不会凭空造出状态。
//
// 客户端只如实返回收到的部分，「请求了但没回」由调度层判成 Unknown。
func TestPickupMessageMissingRequestedPart(t *testing.T) {
	srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{partA: "available"}))
	c := newTestClient()

	got, err := c.PickupMessage(context.Background(), testRegion(srv), testStore, []string{partA, partB})
	if err != nil {
		t.Fatalf("PickupMessage 返回错误: %v", err)
	}
	if _, ok := got.Parts[partB]; ok {
		t.Errorf("响应中没有 %s，结果里却出现了: %+v", partB, got.Parts[partB])
	}
	if len(got.Parts) != 1 {
		t.Errorf("Parts 数量 = %d, 期望 1", len(got.Parts))
	}
}

func TestPickupMessageRejectsEmptyArguments(t *testing.T) {
	var hits atomic.Int32
	srv := startServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	})
	c := newTestClient()
	region := testRegion(srv)

	if _, err := c.PickupMessage(context.Background(), region, "", []string{partA}); err == nil {
		t.Error("门店号为空时期望返回错误")
	}
	if _, err := c.PickupMessage(context.Background(), region, testStore, nil); err == nil {
		t.Error("零件号列表为空时期望返回错误")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("参数非法时不应发出请求，实际发出 %d 次", n)
	}
}

// TestPickupMessageContextCancelled 验证取消能立刻生效，而不是卡满 15s 超时。
func TestPickupMessageContextCancelled(t *testing.T) {
	srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{partA: "available"}))
	c := newTestClient()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.PickupMessage(ctx, testRegion(srv), testStore, []string{partA})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, 期望满足 errors.Is(err, context.Canceled)", err)
	}
}

// TestClientThrottlesOutboundRequests 验证全局最小间隔真的在限速。
//
// 上游 500ms 一轮猛冲是触发风控的直接原因；限速失效不会有任何显式症状，
// 只会在某天变成一片 541，所以必须由测试守住。
func TestClientThrottlesOutboundRequests(t *testing.T) {
	t.Parallel()

	const minInterval = 40 * time.Millisecond
	srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{partA: "available"}))
	c := newTestClient(apple.WithMinInterval(minInterval))
	region := testRegion(srv)

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.PickupMessage(context.Background(), region, testStore, []string{partA}); err != nil {
			t.Fatalf("第 %d 次 PickupMessage 失败: %v", i+1, err)
		}
	}
	// 3 次请求之间有 2 个间隔。
	if elapsed := time.Since(start); elapsed < 2*minInterval {
		t.Fatalf("3 次请求耗时 %v, 期望至少 %v（限速未生效）", elapsed, 2*minInterval)
	}
}

// TestClientConcurrentUse 验证同一个 Client 可以被多个 goroutine 共用。
//
// 必须复用单个实例：上游每次查询都新建 gorequest（services/listen.go:221），
// 连接池无从复用，空闲连接与 goroutine 持续堆积。共用的前提是并发安全。
func TestClientConcurrentUse(t *testing.T) {
	srv := startJSONServer(t, pickupJSON(t, testStore, "环球港", map[string]string{partA: "available"}))
	c := newTestClient()
	region := testRegion(srv)

	const workers = 8
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			_, err := c.PickupMessage(context.Background(), region, testStore, []string{partA})
			errCh <- err
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("并发查询失败: %v", err)
		}
	}
}
