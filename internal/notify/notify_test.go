package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// fakeNotifier 是一个可编程的渠道替身：name 决定 Name() 的返回值，
// notify 决定 Notify() 的行为（可以返回错误，也可以 panic）。
type fakeNotifier struct {
	name      string
	namePanic any
	notify    func(ctx context.Context, n Notification) error

	calls atomic.Int32
	got   atomic.Pointer[Notification]
}

func (f *fakeNotifier) Name() string {
	if f.namePanic != nil {
		panic(f.namePanic)
	}
	return f.name
}

func (f *fakeNotifier) Notify(ctx context.Context, n Notification) error {
	f.calls.Add(1)
	f.got.Store(&n)
	if f.notify == nil {
		return nil
	}
	return f.notify(ctx, n)
}

func okNotifier(name string) *fakeNotifier { return &fakeNotifier{name: name} }

func failNotifier(name string, err error) *fakeNotifier {
	return &fakeNotifier{
		name:   name,
		notify: func(context.Context, Notification) error { return err },
	}
}

func panicNotifier(name string, v any) *fakeNotifier {
	return &fakeNotifier{
		name:   name,
		notify: func(context.Context, Notification) error { panic(v) },
	}
}

// recordedRequest 保存服务端收到的原始请求信息。
// rawTarget 是未经 net/url 解析的请求行目标，用它才能验证转义是否真的发生。
type recordedRequest struct {
	method    string
	rawTarget string
	rawPath   string
	query     url.Values
	accept    string
}

type barkServer struct {
	*httptest.Server

	mu   sync.Mutex
	reqs []recordedRequest
}

// newBarkServer 启动一个记录请求的假 Bark 服务器。handler 为 nil 时返回 Bark 成功响应。
func newBarkServer(t *testing.T, handler http.HandlerFunc) *barkServer {
	t.Helper()
	bs := &barkServer{}
	bs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawPath, _, _ := strings.Cut(r.RequestURI, "?")
		bs.mu.Lock()
		bs.reqs = append(bs.reqs, recordedRequest{
			method:    r.Method,
			rawTarget: r.RequestURI,
			rawPath:   rawPath,
			query:     r.URL.Query(),
			accept:    r.Header.Get("Accept"),
		})
		bs.mu.Unlock()

		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","timestamp":1700000000}`))
	}))
	t.Cleanup(bs.Close)
	return bs
}

func (bs *barkServer) requests() []recordedRequest {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	out := make([]recordedRequest, len(bs.reqs))
	copy(out, bs.reqs)
	return out
}

func (bs *barkServer) only(t *testing.T) recordedRequest {
	t.Helper()
	reqs := bs.requests()
	if len(reqs) != 1 {
		t.Fatalf("期望服务端收到 1 个请求，实际 %d 个: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

// ---------------------------------------------------------------------------
// Bark：URL 组装与转义
// ---------------------------------------------------------------------------

// TestBarkNotifyEscapesPathSegments 是本文件里最重要的一个用例。
// 上游把标题正文直接 Sprintf 进路径，标题里只要出现「/」，路径语义就变了。
// 这里断言的是服务端收到的原始请求行：斜杠必须是 %2F，而不是真正的分隔符。
func TestBarkNotifyEscapesPathSegments(t *testing.T) {
	srv := newBarkServer(t, nil)

	const (
		title = "到货 提醒/警报"
		body  = "上海-环球港 iPhone 17 Pro/512GB 深墨蓝色 有货"
	)

	b := NewBark(srv.URL+"/device-key", srv.Client())
	if err := b.Notify(context.Background(), Notification{Title: title, Body: body}); err != nil {
		t.Fatalf("Notify 返回错误: %v", err)
	}

	req := srv.only(t)

	wantPath := "/device-key/" + url.PathEscape(title) + "/" + url.PathEscape(body)
	if req.rawPath != wantPath {
		t.Errorf("原始路径不符\n got: %s\nwant: %s", req.rawPath, wantPath)
	}

	// 斜杠必须被转义成 %2F。
	if !strings.Contains(req.rawPath, "%2F") {
		t.Errorf("标题/正文里的斜杠没有被转义成 %%2F: %s", req.rawPath)
	}
	// 路径分隔符只应该有 3 个：/device-key、/标题、/正文。
	// 如果标题里的斜杠泄漏成了分隔符，这里会变成 5 个。
	if got := strings.Count(req.rawPath, "/"); got != 3 {
		t.Errorf("路径分隔符数量为 %d，期望 3（说明有斜杠泄漏成了分隔符）: %s", got, req.rawPath)
	}
	// 空格必须转义，中文必须百分号编码。
	if strings.Contains(req.rawTarget, " ") {
		t.Errorf("请求目标里出现了未转义的空格: %q", req.rawTarget)
	}
	if strings.ContainsAny(req.rawPath, "上海到货") {
		t.Errorf("请求路径里出现了未转义的中文: %s", req.rawPath)
	}
	if !strings.Contains(req.rawPath, "%20") {
		t.Errorf("空格没有被转义成 %%20: %s", req.rawPath)
	}

	// 服务端反解出来的路径段应当等于原文。
	segs := strings.Split(strings.TrimPrefix(req.rawPath, "/"), "/")
	if len(segs) != 3 {
		t.Fatalf("路径段数量为 %d，期望 3: %v", len(segs), segs)
	}
	gotTitle, err := url.PathUnescape(segs[1])
	if err != nil {
		t.Fatalf("反转义标题失败: %v", err)
	}
	gotBody, err := url.PathUnescape(segs[2])
	if err != nil {
		t.Fatalf("反转义正文失败: %v", err)
	}
	if gotTitle != title || gotBody != body {
		t.Errorf("反转义结果不符\n title: %q want %q\n  body: %q want %q", gotTitle, title, gotBody, body)
	}

	if req.method != http.MethodGet {
		t.Errorf("请求方法为 %s，期望 GET", req.method)
	}
	if req.accept != "application/json" {
		t.Errorf("Accept 头为 %q，期望 application/json", req.accept)
	}
}

// TestBarkNotifyEscapesTrickyRunes 覆盖一批容易破坏 URL 结构的字符。
func TestBarkNotifyEscapesTrickyRunes(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
	}{
		{"问号与井号", "标题?a=1", "正文#锚点"},
		{"与号与等号", "a&b=c", "d&e=f"},
		{"百分号", "5%off", "已减 50%"},
		{"连续斜杠", "a//b", "c///d"},
		{"点段", "..", "."},
		{"字面量百分号编码", "a%2Fb", "c%20d"},
		{"emoji", "🎉 到货", "有货 ✅"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newBarkServer(t, nil)
			b := NewBark(srv.URL+"/k", srv.Client())
			if err := b.Notify(context.Background(), Notification{Title: tc.title, Body: tc.body}); err != nil {
				t.Fatalf("Notify 返回错误: %v", err)
			}
			req := srv.only(t)
			want := "/k/" + url.PathEscape(tc.title) + "/" + url.PathEscape(tc.body)
			if req.rawPath != want {
				t.Errorf("原始路径不符\n got: %s\nwant: %s", req.rawPath, want)
			}
			if got := strings.Count(req.rawPath, "/"); got != 3 {
				t.Errorf("路径分隔符数量为 %d，期望 3: %s", got, req.rawPath)
			}
			// 井号/问号泄漏进请求目标会把后面的内容截断或变成 query。
			if strings.ContainsAny(req.rawPath, "?#") {
				t.Errorf("路径里出现了未转义的 ? 或 #: %s", req.rawPath)
			}
		})
	}
}

// TestBarkNotifyKeepsBaseURLQuery 验证用户写在 baseURL 里的参数（?group=、?sound= 等）
// 不会因为附加 url 参数而丢失。
func TestBarkNotifyKeepsBaseURLQuery(t *testing.T) {
	t.Run("附加 url 参数后仍保留", func(t *testing.T) {
		srv := newBarkServer(t, nil)
		b := NewBark(srv.URL+"/k?group=库存&sound=alarm&level=timeSensitive", srv.Client())
		link := "https://www.apple.com.cn/shop/bag?product=MG0G4CH/A&count=1"
		if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货", URL: link}); err != nil {
			t.Fatalf("Notify 返回错误: %v", err)
		}
		req := srv.only(t)
		for k, want := range map[string]string{
			"group": "库存",
			"sound": "alarm",
			"level": "timeSensitive",
			"url":   link,
		} {
			if got := req.query.Get(k); got != want {
				t.Errorf("query[%s] = %q，期望 %q（完整目标: %s）", k, got, want, req.rawTarget)
			}
		}
	})

	t.Run("不给 url 时不凭空添加", func(t *testing.T) {
		srv := newBarkServer(t, nil)
		b := NewBark(srv.URL+"/k?group=库存", srv.Client())
		if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err != nil {
			t.Fatalf("Notify 返回错误: %v", err)
		}
		req := srv.only(t)
		if req.query.Get("group") != "库存" {
			t.Errorf("group 丢失: %s", req.rawTarget)
		}
		if _, ok := req.query["url"]; ok {
			t.Errorf("不该出现 url 参数: %s", req.rawTarget)
		}
	})

	t.Run("baseURL 自带的 url 参数会被通知里的覆盖", func(t *testing.T) {
		srv := newBarkServer(t, nil)
		b := NewBark(srv.URL+"/k?url=https://old.example.com/", srv.Client())
		if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货", URL: "https://new.example.com/bag"}); err != nil {
			t.Fatalf("Notify 返回错误: %v", err)
		}
		req := srv.only(t)
		if got := req.query["url"]; len(got) != 1 || got[0] != "https://new.example.com/bag" {
			t.Errorf("url 参数 = %v，期望仅有 https://new.example.com/bag", got)
		}
	})
}

// TestBarkNotifyEscapesURLParam 验证 url 参数里的 & = # 空格被 query 编码，
// 不会把后续参数截断。上游直接 Sprintf 拼接，购物袋地址里的 & 会截断参数。
func TestBarkNotifyEscapesURLParam(t *testing.T) {
	srv := newBarkServer(t, nil)
	link := "https://www.apple.com.cn/shop/bag?a=1&b=2#frag 空格"
	b := NewBark(srv.URL+"/k?group=库存", srv.Client())
	if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货", URL: link}); err != nil {
		t.Fatalf("Notify 返回错误: %v", err)
	}
	req := srv.only(t)
	if got := req.query.Get("url"); got != link {
		t.Errorf("url 参数 = %q，期望 %q（完整目标: %s）", got, link, req.rawTarget)
	}
	if req.query.Get("group") != "库存" {
		t.Errorf("url 里的 & 截断了后续参数: %s", req.rawTarget)
	}
	// 原始目标里 url 参数的值必须是编码过的。
	if strings.Contains(req.rawTarget, "url=https://www.apple.com.cn/shop/bag?a=1&b=2") {
		t.Errorf("url 参数没有被编码: %s", req.rawTarget)
	}
}

// TestBarkNotifyTrailingSlashBaseURL 验证 baseURL 末尾的斜杠不会造成空路径段。
func TestBarkNotifyTrailingSlashBaseURL(t *testing.T) {
	for _, suffix := range []string{"/k", "/k/", "/k///"} {
		t.Run(suffix, func(t *testing.T) {
			srv := newBarkServer(t, nil)
			b := NewBark(srv.URL+suffix, srv.Client())
			if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err != nil {
				t.Fatalf("Notify 返回错误: %v", err)
			}
			req := srv.only(t)
			want := "/k/" + url.PathEscape("到货") + "/" + url.PathEscape("有货")
			if req.rawPath != want {
				t.Errorf("原始路径不符\n got: %s\nwant: %s", req.rawPath, want)
			}
		})
	}
}

// TestBarkNotifyOnlyBodyWhenTitleEmpty 验证空标题被整段跳过，不留下 // 空段。
func TestBarkNotifyOnlyBodyWhenTitleEmpty(t *testing.T) {
	for _, title := range []string{"", "   ", "\t\n"} {
		t.Run(fmt.Sprintf("%q", title), func(t *testing.T) {
			srv := newBarkServer(t, nil)
			b := NewBark(srv.URL+"/k", srv.Client())
			if err := b.Notify(context.Background(), Notification{Title: title, Body: "有货"}); err != nil {
				t.Fatalf("Notify 返回错误: %v", err)
			}
			req := srv.only(t)
			want := "/k/" + url.PathEscape("有货")
			if req.rawPath != want {
				t.Errorf("原始路径不符\n got: %s\nwant: %s", req.rawPath, want)
			}
			if strings.Contains(req.rawPath, "//") {
				t.Errorf("出现了空路径段: %s", req.rawPath)
			}
		})
	}
}

// TestBarkNotifyEmptyTitleAndBody 验证标题正文都为空时返回错误且不发请求。
func TestBarkNotifyEmptyTitleAndBody(t *testing.T) {
	srv := newBarkServer(t, nil)
	b := NewBark(srv.URL+"/k", srv.Client())
	err := b.Notify(context.Background(), Notification{Title: "  ", Body: "\t"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if len(srv.requests()) != 0 {
		t.Errorf("不该发出任何请求，实际发了 %d 个", len(srv.requests()))
	}
}

// ---------------------------------------------------------------------------
// Bark：错误处理
// ---------------------------------------------------------------------------

// TestBarkNotifyHTTPErrorStatus 验证非 2xx 一律返回 error。
// 上游连状态码都不看，设备 key 写错时用户毫无察觉。
func TestBarkNotifyHTTPErrorStatus(t *testing.T) {
	tests := []struct {
		status int
		body   string
	}{
		{http.StatusBadRequest, `{"code":400,"message":"key 不存在"}`},
		{http.StatusUnauthorized, "unauthorized"},
		{http.StatusForbidden, ""},
		{http.StatusNotFound, "<html><body>404 not found</body></html>"},
		{http.StatusInternalServerError, "boom"},
		{http.StatusBadGateway, "bad gateway"},
		{http.StatusServiceUnavailable, "service unavailable"},
		{http.StatusTeapot, "teapot"},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			srv := newBarkServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			b := NewBark(srv.URL+"/k", srv.Client())
			err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
			if err == nil {
				t.Fatalf("HTTP %d 应当返回错误，实际为 nil", tc.status)
			}
			if !strings.Contains(err.Error(), fmt.Sprint(tc.status)) {
				t.Errorf("错误信息里看不出状态码 %d: %v", tc.status, err)
			}
		})
	}
}

// TestBarkNotify2xxIsSuccess 验证各种 2xx 都算成功。
func TestBarkNotify2xxIsSuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := newBarkServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			})
			b := NewBark(srv.URL+"/k", srv.Client())
			if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err != nil {
				t.Errorf("HTTP %d 应当算成功，实际返回: %v", status, err)
			}
		})
	}
}

// TestBarkNotifyJSONCodeFailure 验证 HTTP 200 但 body 里 code 表示失败时也返回 error。
func TestBarkNotifyJSONCodeFailure(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		wantMsg string
	}{
		{name: "code 200 成功", body: `{"code":200,"message":"success"}`},
		{name: "code 缺失", body: `{"message":"success"}`},
		{name: "code 为 0", body: `{"code":0,"message":""}`},
		{name: "非 JSON 响应", body: `success`},
		{name: "空响应体", body: ``},
		{name: "code 400", body: `{"code":400,"message":"设备 key 不存在"}`, wantErr: true, wantMsg: "设备 key 不存在"},
		{name: "code 500", body: `{"code":500,"message":"push failed"}`, wantErr: true, wantMsg: "push failed"},
		{name: "code 100", body: `{"code":100,"message":"weird"}`, wantErr: true, wantMsg: "weird"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newBarkServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})
			b := NewBark(srv.URL+"/k", srv.Client())
			err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("body %q 应当返回错误，实际为 nil", tc.body)
			case !tc.wantErr && err != nil:
				t.Fatalf("body %q 应当成功，实际返回: %v", tc.body, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("错误信息 %v 里没有包含服务端消息 %q", err, tc.wantMsg)
			}
		})
	}
}

// TestBarkNotifyHTMLErrorPageIsSummarized 验证整页 HTML 不会原样塞进 error。
func TestBarkNotifyHTMLErrorPageIsSummarized(t *testing.T) {
	huge := "<html>\n" + strings.Repeat("<div>错误页面内容</div>\n", 2000) + "</html>"
	srv := newBarkServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(huge))
	})
	b := NewBark(srv.URL+"/k", srv.Client())
	err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if n := len([]rune(err.Error())); n > 200 {
		t.Errorf("错误信息长度 %d 个字符，说明响应体没有被压缩: %v", n, err)
	}
	if strings.ContainsAny(err.Error(), "\n\r") {
		t.Errorf("错误信息里含有换行: %q", err.Error())
	}
}

// TestBarkNotifyUnconfigured 验证未配置（空 / 全空白）时返回 nil，
// 表示「没开这个功能」，不是错误。
func TestBarkNotifyUnconfigured(t *testing.T) {
	for _, raw := range []string{"", " ", "   ", "\t", "\n", " \t\r\n "} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			b := NewBark(raw, nil)
			if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err != nil {
				t.Errorf("未配置时应当返回 nil，实际返回: %v", err)
			}
		})
	}
}

// TestBarkNilReceiver 验证 nil 接收者不 panic。
func TestBarkNilReceiver(t *testing.T) {
	var b *Bark
	if got := b.Name(); got != "Bark" {
		t.Errorf("Name() = %q，期望 Bark", got)
	}
	if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err != nil {
		t.Errorf("nil 接收者 Notify 应当返回 nil，实际返回: %v", err)
	}
	// 作为 Notifier 塞进 Multi 也不该出事。
	if err := NewMulti(b).Notify(context.Background(), Notification{Title: "到货"}); err != nil {
		t.Errorf("Multi 包着 nil *Bark 应当返回 nil，实际返回: %v", err)
	}
}

// TestBarkNotifyInvalidBaseURL 验证非法地址返回错误而不是发出诡异请求。
func TestBarkNotifyInvalidBaseURL(t *testing.T) {
	for _, raw := range []string{
		"api.day.app/key",         // 缺 scheme
		"ftp://api.day.app/key",   // 非 http(s)
		"https://",                // 缺 host
		"/just/a/path",            // 相对路径
		"ht tp://api.day.app/key", // 无法解析
		"://api.day.app/key",
	} {
		t.Run(raw, func(t *testing.T) {
			b := NewBark(raw, http.DefaultClient)
			err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
			if err == nil {
				t.Errorf("地址 %q 应当返回错误，实际为 nil", raw)
			}
		})
	}
}

// TestBarkNotifyRespectsContextCancel 验证 ctx 取消时尽快返回错误。
func TestBarkNotifyRespectsContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := newBarkServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	})
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	b := NewBark(srv.URL+"/k", srv.Client())

	errCh := make(chan error, 1)
	go func() {
		errCh <- b.Notify(ctx, Notification{Title: "到货", Body: "有货"})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("ctx 取消后应当返回错误，实际为 nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("错误应当可以 errors.Is 到 context.Canceled，实际: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 Notify 没有及时返回")
	}
}

// TestBarkNotifyTruncatedResponse 验证响应被截断（Content-Length 撒谎后连接断开）
// 时返回错误，而不是当作推送成功。
func TestBarkNotifyTruncatedResponse(t *testing.T) {
	srv := newBarkServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		// 声明 4096 字节，实际只写 5 个，然后直接把连接掐掉。
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 4096\r\n\r\nshort")
		_ = buf.Flush()
	})

	b := NewBark(srv.URL+"/k", srv.Client())
	err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
	if err == nil {
		t.Fatal("响应被截断时应当返回错误，实际为 nil")
	}
}

// TestBarkNotifyServerDown 验证连接失败返回错误。
func TestBarkNotifyServerDown(t *testing.T) {
	srv := newBarkServer(t, nil)
	base := srv.URL + "/k"
	client := srv.Client()
	srv.Close() // 关掉服务端，让连接失败

	b := NewBark(base, client)
	if err := b.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err == nil {
		t.Fatal("服务端不可达时应当返回错误，实际为 nil")
	}
}

// TestNewBarkDefaultClientHasTimeout 验证不传 client 时回退到带超时的客户端，
// 而不是没有任何超时的 http.DefaultClient。
func TestNewBarkDefaultClientHasTimeout(t *testing.T) {
	b := NewBark("https://api.day.app/key", nil)
	if b.hc == nil {
		t.Fatal("hc 为 nil")
	}
	if b.hc.Timeout <= 0 {
		t.Errorf("默认客户端没有设置超时: %v", b.hc.Timeout)
	}
	if b.hc == http.DefaultClient {
		t.Error("默认客户端不应当是 http.DefaultClient（它没有超时）")
	}
}

// ---------------------------------------------------------------------------
// Multi
// ---------------------------------------------------------------------------

// TestMultiAllSucceed 验证全部成功时返回 nil，且每个渠道都收到了完整通知。
func TestMultiAllSucceed(t *testing.T) {
	a, b, c := okNotifier("Bark"), okNotifier("提示音"), okNotifier("桌面通知")
	m := NewMulti(a, b, c)

	msg := Notification{Title: "到货提醒", Body: "上海-环球港 有货", URL: "https://example.com/bag"}
	if err := m.Notify(context.Background(), msg); err != nil {
		t.Fatalf("全部成功时应当返回 nil，实际返回: %v", err)
	}
	for _, n := range []*fakeNotifier{a, b, c} {
		if got := n.calls.Load(); got != 1 {
			t.Errorf("渠道 %s 被调用 %d 次，期望 1 次", n.name, got)
		}
		if got := n.got.Load(); got == nil || *got != msg {
			t.Errorf("渠道 %s 收到的通知为 %+v，期望 %+v", n.name, got, msg)
		}
	}
	if got, want := m.Name(), "multi(Bark, 提示音, 桌面通知)"; got != want {
		t.Errorf("Name() = %q，期望 %q", got, want)
	}
}

// TestMultiPartialFailure 验证部分失败时用 errors.Join 汇总，
// 且错误信息里能看出是哪个渠道失败的。
func TestMultiPartialFailure(t *testing.T) {
	errBark := errors.New("Bark 服务器返回 HTTP 400")
	errSound := errors.New("初始化音频设备失败")

	bark := failNotifier("Bark", errBark)
	sound := failNotifier("提示音", errSound)
	desktop := okNotifier("桌面通知")

	m := NewMulti(bark, sound, desktop)
	err := m.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
	if err == nil {
		t.Fatal("有渠道失败时应当返回错误，实际为 nil")
	}

	// 原始错误必须可以被 errors.Is 找到，说明用的是 Join + %w 而不是字符串拼接。
	if !errors.Is(err, errBark) {
		t.Errorf("errors.Is 找不到 Bark 的原始错误: %v", err)
	}
	if !errors.Is(err, errSound) {
		t.Errorf("errors.Is 找不到提示音的原始错误: %v", err)
	}

	// 错误信息里必须能看出是哪个渠道。
	text := err.Error()
	for _, want := range []string{"Bark", "提示音", errBark.Error(), errSound.Error()} {
		if !strings.Contains(text, want) {
			t.Errorf("错误信息里缺少 %q: %s", want, text)
		}
	}
	// 成功的渠道不该出现在错误信息里，也必须照常执行。
	if strings.Contains(text, "桌面通知") {
		t.Errorf("成功的渠道不该出现在错误信息里: %s", text)
	}
	if got := desktop.calls.Load(); got != 1 {
		t.Errorf("失败渠道不该影响其他渠道，桌面通知被调用 %d 次", got)
	}
}

// TestMultiRecoversPanic 是本项目相对上游最重要的健壮性改进：
// 上游 Bark 出错就 panic，而它跑在后台 goroutine 里，会直接杀掉整个进程。
// 这里 panic 必须被 recover 成 error，且其余渠道照常执行完。
func TestMultiRecoversPanic(t *testing.T) {
	panicValues := []any{
		"runtime error: invalid memory address",
		errors.New("bark 推送失败"),
		42,
		nil, // Go 1.21 起 panic(nil) 会变成 *runtime.PanicNilError
	}
	for i, pv := range panicValues {
		t.Run(fmt.Sprintf("panic值%d", i), func(t *testing.T) {
			boom := panicNotifier("Bark", pv)
			before := okNotifier("提示音")
			after := okNotifier("桌面通知")
			failing := failNotifier("邮件", errors.New("smtp 拒绝"))

			m := NewMulti(before, boom, after, failing)
			err := m.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
			if err == nil {
				t.Fatal("渠道 panic 时应当返回错误，实际为 nil")
			}
			if !strings.Contains(err.Error(), "Bark") {
				t.Errorf("错误信息里看不出是 Bark panic 了: %v", err)
			}
			// 其他渠道必须照常执行完，包括排在 panic 渠道之后的那个。
			for _, n := range []*fakeNotifier{before, after, failing} {
				if got := n.calls.Load(); got != 1 {
					t.Errorf("渠道 %s 被调用 %d 次，期望 1 次（panic 不该影响其他渠道）", n.name, got)
				}
			}
			// 其他渠道自己的失败也要一并汇总，不能被 panic 吃掉。
			if !strings.Contains(err.Error(), "smtp 拒绝") {
				t.Errorf("其他渠道的错误被 panic 吃掉了: %v", err)
			}
		})
	}
}

// TestMultiRecoversPanicInName 验证连 Name() 都 panic 的渠道也不会击穿 recover，
// safeName 是在 recover 之后被调用的，它自己也不能出事。
func TestMultiRecoversPanicInName(t *testing.T) {
	bad := &fakeNotifier{
		namePanic: "Name 也炸了",
		notify:    func(context.Context, Notification) error { panic("Notify 炸了") },
	}
	other := okNotifier("提示音")

	m := NewMulti(bad, other)

	// Name() 本身也不该 panic。
	if got := m.Name(); !strings.Contains(got, "未知渠道") {
		t.Errorf("Name() = %q，期望包含「未知渠道」", got)
	}

	err := m.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "未知渠道") {
		t.Errorf("错误信息里应当用兜底渠道名: %v", err)
	}
	if got := other.calls.Load(); got != 1 {
		t.Errorf("其他渠道被调用 %d 次，期望 1 次", got)
	}
}

// TestMultiIgnoresNilNotifier 验证 nil 渠道被忽略，界面层可以直接
// NewMulti(bark, sound) 而不必先剔除没启用的。
func TestMultiIgnoresNilNotifier(t *testing.T) {
	ok := okNotifier("Bark")
	m := NewMulti(nil, ok, nil, nil)

	if got, want := m.Name(), "multi(Bark)"; got != want {
		t.Errorf("Name() = %q，期望 %q", got, want)
	}
	if err := m.Notify(context.Background(), Notification{Title: "到货", Body: "有货"}); err != nil {
		t.Fatalf("应当返回 nil，实际返回: %v", err)
	}
	if got := ok.calls.Load(); got != 1 {
		t.Errorf("渠道被调用 %d 次，期望 1 次", got)
	}

	t.Run("全是 nil", func(t *testing.T) {
		m := NewMulti(nil, nil)
		if got, want := m.Name(), "multi()"; got != want {
			t.Errorf("Name() = %q，期望 %q", got, want)
		}
		if err := m.Notify(context.Background(), Notification{Title: "到货"}); err != nil {
			t.Errorf("应当返回 nil，实际返回: %v", err)
		}
	})
}

// TestMultiEmptyAndNilReceiver 验证空组合与 nil 接收者都不 panic。
func TestMultiEmptyAndNilReceiver(t *testing.T) {
	t.Run("空组合", func(t *testing.T) {
		m := NewMulti()
		if got, want := m.Name(), "multi()"; got != want {
			t.Errorf("Name() = %q，期望 %q", got, want)
		}
		if err := m.Notify(context.Background(), Notification{Title: "到货"}); err != nil {
			t.Errorf("应当返回 nil，实际返回: %v", err)
		}
	})
	t.Run("nil 接收者", func(t *testing.T) {
		var m *Multi
		if got, want := m.Name(), "multi()"; got != want {
			t.Errorf("Name() = %q，期望 %q", got, want)
		}
		if err := m.Notify(context.Background(), Notification{Title: "到货"}); err != nil {
			t.Errorf("应当返回 nil，实际返回: %v", err)
		}
	})
}

// TestMultiUnnamedNotifier 验证 Name() 返回空串的渠道有兜底名字。
func TestMultiUnnamedNotifier(t *testing.T) {
	anon := failNotifier("", errors.New("挂了"))
	m := NewMulti(anon)
	if got, want := m.Name(), "multi(未命名渠道)"; got != want {
		t.Errorf("Name() = %q，期望 %q", got, want)
	}
	err := m.Notify(context.Background(), Notification{Title: "到货"})
	if err == nil || !strings.Contains(err.Error(), "未命名渠道") {
		t.Errorf("错误信息应当包含「未命名渠道」，实际: %v", err)
	}
}

// TestSafeNotifyWithNilNotifier 直接盯住最后一道防线：
// NewMulti 会过滤掉 nil，但 safeNotify 本身也必须扛得住 nil 渠道，
// 因为它是「渠道实现出了任何意外都不许杀进程」这条约定的兜底。
func TestSafeNotifyWithNilNotifier(t *testing.T) {
	err := safeNotify(context.Background(), nil, Notification{Title: "到货"})
	if err == nil {
		t.Fatal("nil 渠道应当返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "未知渠道") {
		t.Errorf("错误信息应当用兜底渠道名，实际: %v", err)
	}
	if got := safeName(nil); got != "未知渠道" {
		t.Errorf("safeName(nil) = %q，期望 未知渠道", got)
	}
}

// TestMultiNested 验证 Multi 嵌套 Multi 可用，错误能穿透多层。
func TestMultiNested(t *testing.T) {
	errInner := errors.New("Bark 400")
	bark := failNotifier("Bark", errInner)
	sound := okNotifier("提示音")
	desktop := okNotifier("桌面通知")

	inner := NewMulti(bark, sound)
	outer := NewMulti(inner, desktop)

	if got, want := outer.Name(), "multi(multi(Bark, 提示音), 桌面通知)"; got != want {
		t.Errorf("Name() = %q，期望 %q", got, want)
	}

	err := outer.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !errors.Is(err, errInner) {
		t.Errorf("嵌套后 errors.Is 找不到原始错误: %v", err)
	}
	for _, n := range []*fakeNotifier{bark, sound, desktop} {
		if got := n.calls.Load(); got != 1 {
			t.Errorf("渠道 %s 被调用 %d 次，期望 1 次", n.name, got)
		}
	}

	t.Run("嵌套里的 panic 也被拦截", func(t *testing.T) {
		boom := panicNotifier("Bark", "炸了")
		sibling := okNotifier("提示音")
		outer := NewMulti(NewMulti(boom), sibling)
		err := outer.Notify(context.Background(), Notification{Title: "到货"})
		if err == nil {
			t.Fatal("期望返回错误，实际为 nil")
		}
		if got := sibling.calls.Load(); got != 1 {
			t.Errorf("兄弟渠道被调用 %d 次，期望 1 次", got)
		}
	})

	t.Run("嵌套空 Multi 不算失败", func(t *testing.T) {
		if err := NewMulti(NewMulti(), NewMulti(nil)).Notify(context.Background(), Notification{Title: "到货"}); err != nil {
			t.Errorf("应当返回 nil，实际返回: %v", err)
		}
	})
}

// TestMultiRunsConcurrently 验证各渠道是并发而不是串行执行的：
// Bark 服务器无响应时，本地提示音不该跟着干等。
func TestMultiRunsConcurrently(t *testing.T) {
	const n = 4
	started := make(chan struct{}, n)
	release := make(chan struct{})

	notifiers := make([]Notifier, 0, n)
	for i := range n {
		notifiers = append(notifiers, &fakeNotifier{
			name: fmt.Sprintf("渠道%d", i),
			notify: func(context.Context, Notification) error {
				started <- struct{}{}
				<-release // 只有并发执行时，所有渠道才可能同时停在这里
				return nil
			},
		})
	}

	done := make(chan error, 1)
	go func() { done <- NewMulti(notifiers...).Notify(context.Background(), Notification{Title: "到货"}) }()

	deadline := time.After(3 * time.Second)
	for i := range n {
		select {
		case <-started:
		case <-deadline:
			t.Fatalf("只有 %d 个渠道启动了，说明是串行执行的", i)
		}
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("应当返回 nil，实际返回: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Notify 没有返回")
	}
}

// TestMultiPassesContext 验证 ctx 被原样传给各渠道。
func TestMultiPassesContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "标记")

	var seen atomic.Int32
	probe := &fakeNotifier{
		name: "探针",
		notify: func(c context.Context, _ Notification) error {
			if c.Value(ctxKey{}) == "标记" {
				seen.Add(1)
			}
			return nil
		},
	}
	if err := NewMulti(probe, probe).Notify(ctx, Notification{Title: "到货"}); err != nil {
		t.Fatalf("应当返回 nil，实际返回: %v", err)
	}
	if got := seen.Load(); got != 2 {
		t.Errorf("只有 %d 次调用拿到了原始 ctx，期望 2 次", got)
	}
}

// TestMultiWithRealBark 把 Bark 放进 Multi 走一遍，确认组合层不吞错误。
func TestMultiWithRealBark(t *testing.T) {
	srv := newBarkServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"message":"key 不存在"}`))
	})

	bark := NewBark(srv.URL+"/k", srv.Client())
	sound := NewSound(nil) // 未配置，返回 nil
	sibling := okNotifier("桌面通知")

	err := NewMulti(bark, sound, sibling).Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
	if err == nil {
		t.Fatal("Bark 失败时应当返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "Bark") || !strings.Contains(err.Error(), "400") {
		t.Errorf("错误信息不完整: %v", err)
	}
	if got := sibling.calls.Load(); got != 1 {
		t.Errorf("桌面通知被调用 %d 次，期望 1 次", got)
	}
}

// ---------------------------------------------------------------------------
// Sound
//
// 这里全部走「未配置」和「解码失败」两条路径：mp3.Decode 在这两种情况下都会
// 先失败返回，speaker.Init 根本不会被调用，因此测试不会碰到真实音频设备。
// 唯一需要设备的成功路径用例默认跳过，见 TestSoundPlaysRealAudio。
// ---------------------------------------------------------------------------

// TestSoundUnconfigured 验证空音频数据等同于未配置，返回 nil 而不是错误。
func TestSoundUnconfigured(t *testing.T) {
	for name, data := range map[string][]byte{
		"nil": nil,
		"空切片": {},
	} {
		t.Run(name, func(t *testing.T) {
			s := NewSound(data)
			for i := range 3 {
				if err := s.Notify(context.Background(), Notification{Title: "到货"}); err != nil {
					t.Fatalf("第 %d 次调用应当返回 nil，实际返回: %v", i+1, err)
				}
			}
		})
	}
}

// TestSoundInvalidMP3 验证非法 mp3 数据返回 error 而不是 panic。
// 上游解码失败直接 panic，而它跑在后台 goroutine 里，会终止整个进程。
func TestSoundInvalidMP3(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"纯文本", []byte("this is definitely not an mp3 file")},
		{"全零字节", make([]byte, 4096)},
		{"截断的帧头", []byte{0xff, 0xfb, 0x90}},
		{"单字节", []byte{0x00}},
		{"随机二进制", []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 0x02, 0x03}},
		{"ID3 头后为空", append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), 0x00)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSound(tc.data)
			err := s.Notify(context.Background(), Notification{Title: "到货", Body: "有货"})
			if err == nil {
				t.Fatal("非法音频数据应当返回错误，实际为 nil")
			}
		})
	}
}

// TestSoundInitErrorIsStable 验证初始化失败后每次调用都稳定返回同一个 error，
// 而不是第二次开始变成 nil（sync.Once 只跑一次，initErr 必须一直可读）。
func TestSoundInitErrorIsStable(t *testing.T) {
	s := NewSound([]byte("not an mp3 at all"))
	first := s.Notify(context.Background(), Notification{Title: "到货"})
	if first == nil {
		t.Fatal("首次调用应当返回错误，实际为 nil")
	}
	for i := range 5 {
		err := s.Notify(context.Background(), Notification{Title: "到货"})
		if err == nil {
			t.Fatalf("第 %d 次调用返回了 nil，初始化失败必须稳定复现", i+2)
		}
		if err.Error() != first.Error() {
			t.Fatalf("第 %d 次调用的错误变了\n got: %v\nwant: %v", i+2, err, first)
		}
	}
}

// TestSoundInitErrorIsStableConcurrently 验证并发首次调用时 sync.Once 的写入
// 对所有调用方可见，不会有人拿到 nil。配合 -race 一起看。
func TestSoundInitErrorIsStableConcurrently(t *testing.T) {
	s := NewSound([]byte("still not an mp3"))

	const goroutines = 32
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	start := make(chan struct{})
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.Notify(context.Background(), Notification{Title: "到货"})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Fatalf("第 %d 个 goroutine 拿到了 nil，初始化失败没有被所有调用方看到", i)
		}
	}
}

// TestSoundNilReceiver 验证 nil 接收者不 panic。
func TestSoundNilReceiver(t *testing.T) {
	var s *Sound
	if got := s.Name(); got != "提示音" {
		t.Errorf("Name() = %q，期望 提示音", got)
	}
	if err := s.Notify(context.Background(), Notification{Title: "到货"}); err != nil {
		t.Errorf("nil 接收者应当返回 nil，实际返回: %v", err)
	}
	if err := NewMulti(s).Notify(context.Background(), Notification{Title: "到货"}); err != nil {
		t.Errorf("Multi 包着 nil *Sound 应当返回 nil，实际返回: %v", err)
	}
}

// TestSoundCanceledContextDoesNotPanic 验证已取消的 ctx 走解码失败路径时行为一致。
// 解码失败发生在碰音频设备之前，所以这里依然不需要设备。
func TestSoundCanceledContextDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewSound([]byte("not an mp3"))
	if err := s.Notify(ctx, Notification{Title: "到货"}); err == nil {
		t.Error("非法音频应当返回错误，实际为 nil")
	}
}

// TestSoundImplementsNotifier 是编译期断言的运行时版本。
func TestSoundImplementsNotifier(t *testing.T) {
	var _ Notifier = (*Sound)(nil)
	var _ Notifier = (*Bark)(nil)
	var _ Notifier = (*Multi)(nil)
}

// TestSoundPlaysRealAudio 是唯一需要真实音频设备的用例，默认跳过。
// 想跑它得同时满足：非 -short 模式，且显式设置 NOTIFY_AUDIO_DEVICE_TEST=1。
// CI 里两个条件都不满足，speaker.Init 不会被调用。
func TestSoundPlaysRealAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 模式下跳过需要音频设备的用例")
	}
	if os.Getenv("NOTIFY_AUDIO_DEVICE_TEST") != "1" {
		t.Skip("需要真实音频设备，设置 NOTIFY_AUDIO_DEVICE_TEST=1 才运行")
	}
	data, err := os.ReadFile("../../assets/alert.mp3")
	if err != nil {
		t.Skipf("读取提示音失败: %v", err)
	}
	s := NewSound(data)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Notify(ctx, Notification{Title: "到货", Body: "有货"}); err != nil {
		t.Fatalf("播放失败: %v", err)
	}
}
