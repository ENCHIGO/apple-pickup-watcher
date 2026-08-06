//go:build live

// 这是针对 Apple 真实接口的契约测试，默认不参与 go test，需要显式启用：
//
//	go test -tags live ./internal/apple/ -v
//
// 它存在的理由很直接：上游项目就是因为 Apple 悄悄换掉了接口而彻底失效，
// 却因为没有任何测试，半年时间里没人发现程序返回的「无货」其实是被拦截。
// 单元测试用假响应验证解析逻辑，但拦不住这种事；只有真的打一次 Apple 才行。
// 建议在 CI 里按天定时跑，而不是每次提交都跑，以免给 Apple 添麻烦。
package apple

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ENCHIGO/apple-pickup-watcher/internal/model"
)

// liveCases 每个地区挑一家真实门店和一个真实零件号。
// 零件号会随机型更新而失效，届时应当更新此表，而不是删掉测试。
var liveCases = []struct {
	locale string
	store  string
	part   string
}{
	{"zh_CN", "R683", "MG724CH/A"},
	{"zh_HK", "R428", "MG6L4ZA/A"},
	{"zh_TW", "R694", "MG6L4ZP/A"},
	{"ja_JP", "R119", "MG6A4J/A"},
	{"en_SG", "R669", "MG6L4X/A"},
	{"en_AU", "R440", "MG6L4X/A"},
	{"en_MY", "R742", "MG6L4X/A"},
}

// TestLivePickupMessage 验证每个地区的接口仍然可用且响应结构未变。
func TestLivePickupMessage(t *testing.T) {
	client := NewClient(WithMinInterval(2 * time.Second))

	for _, tc := range liveCases {
		t.Run(tc.locale, func(t *testing.T) {
			region, ok := model.RegionByLocale(tc.locale)
			if !ok {
				t.Fatalf("地区表里没有 %s", tc.locale)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()

			result, err := client.PickupMessage(ctx, region, tc.store, []string{tc.part})
			if err != nil {
				switch {
				case errors.Is(err, ErrBlocked):
					t.Fatalf("%s 的请求被 Apple 拦截，接口或请求特征需要调整: %v", tc.locale, err)
				case errors.Is(err, ErrUnexpectedSchema):
					t.Fatalf("%s 的响应结构已改变，解析逻辑需要更新: %v", tc.locale, err)
				default:
					t.Fatalf("%s 查询失败: %v", tc.locale, err)
				}
			}

			if result.StoreNumber != tc.store {
				t.Errorf("门店号不符: 期望 %s, 实际 %s", tc.store, result.StoreNumber)
			}
			status, ok := result.Parts[tc.part]
			if !ok {
				t.Fatalf("响应中没有零件号 %s（可能该型号已下架，需更新用例）", tc.part)
			}
			if status.PickupDisplay == "" {
				t.Error("pickupDisplay 为空，字段名可能已变更")
			}
			// 状态是有货还是无货取决于当下库存，不做断言；
			// 但解析结果不能是 Unknown —— 那说明出现了我们没见过的取值。
			if status.Availability == model.Unknown {
				t.Errorf("pickupDisplay=%q 未能识别，需要在 availabilityFrom 中补充该取值", status.PickupDisplay)
			}
			t.Logf("%s %s %s -> %s (pickupDisplay=%s, 商品名=%s)",
				tc.locale, result.StoreName, tc.part, status.Availability, status.PickupDisplay, status.ProductTitle)
		})
	}
}

// TestLiveUnknownPartIsNotOutOfStock 验证本项目最关键的不变量在真实接口上也成立：
// 查不到的型号必须是「未知」，绝不能被当成「无货」。
func TestLiveUnknownPartIsNotOutOfStock(t *testing.T) {
	client := NewClient(WithMinInterval(2 * time.Second))
	region, _ := model.RegionByLocale("zh_CN")

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := client.PickupMessage(ctx, region, "R683", []string{"NOSUCHPART/A"})
	if err != nil {
		// Apple 对无效零件号可能直接报错，这也是可接受的结果。
		t.Logf("无效零件号返回错误（可接受）: %v", err)
		return
	}
	if status, ok := result.Parts["NOSUCHPART/A"]; ok && status.Availability == model.OutOfStock {
		t.Errorf("无效零件号被判定成了「无货」，这正是上游那个致命缺陷: %+v", status)
	}
}
