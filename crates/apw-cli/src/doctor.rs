//! `apw doctor`：在受影响用户自己的网络上，逐个切换请求特征做对照。
//!
//! HTTP 541 在维护者的网络上从未复现，唯一能拿到证据的地方是报告者的网络。这个
//! 命令把「cookie 来源」「请求头档案」「Apple 自定义头」拆成一个个变体，每个变体
//! 用一个全新的客户端（独立 cookie 罐），按固定间隔各查一次，把每次请求的状态码、
//! HTTP 版本、耗时和 cookie **名字**汇成一份可以直接贴进 issue 的报告。
//!
//! 纪律：不重试；两个变体连续被拦就停；变体之间至少隔 10 秒。目的是诊断，不是压测。

use std::time::Duration;

use apw_core::apple::{
    ApiError, AppleClient, ClientConfig, RequestProfile, RequestRecord, WarmPage,
};
use apw_core::catalog::Catalog;
use serde::Serialize;
use serde_json::json;
use tokio::io::{AsyncWrite, AsyncWriteExt};

use crate::{CliError, SCHEMA_VERSION, json_line, region, valid_part_number};

/// 一个对照变体：一套客户端配置加一句给人看的说明。
pub struct Variant {
    pub name: &'static str,
    pub summary: &'static str,
    pub config: ClientConfig,
}

/// 按对照价值排序的变体表。前两个是「旧行为」与「当前默认」，后面逐项只改一个变量。
pub fn variants() -> Vec<Variant> {
    let base = ClientConfig {
        max_retries: 0,
        ..ClientConfig::default()
    };
    let chrome = RequestProfile::chrome();
    vec![
        Variant {
            name: "legacy",
            summary: "v0.4.1 的请求特征（Chrome/130、X-Requested-With、无 client hints）+ 购物袋页暖场",
            config: ClientConfig {
                profile: RequestProfile::legacy(),
                warm_page: WarmPage::Bag,
                ..base.clone()
            },
        },
        Variant {
            name: "chrome",
            summary: "当前默认：Chrome 153 特征 + 购物袋页暖场",
            config: ClientConfig {
                profile: chrome.clone(),
                warm_page: WarmPage::Bag,
                ..base.clone()
            },
        },
        Variant {
            name: "chrome-no-warm",
            summary: "Chrome 153 特征，不暖场（不带任何 cookie）",
            config: ClientConfig {
                profile: chrome.clone(),
                warm_page: WarmPage::None,
                ..base.clone()
            },
        },
        Variant {
            name: "chrome-buypage-warm",
            summary: "Chrome 153 特征 + 购买页暖场（CDN 页面，通常只发 geo 一个 cookie）",
            config: ClientConfig {
                profile: chrome.clone(),
                warm_page: WarmPage::BuyPage,
                ..base.clone()
            },
        },
        Variant {
            name: "chrome-xrw",
            summary: "Chrome 153 特征 + X-Requested-With: XMLHttpRequest",
            config: ClientConfig {
                profile: RequestProfile {
                    name: "chrome-153-xrw",
                    x_requested_with: true,
                    ..chrome.clone()
                },
                warm_page: WarmPage::Bag,
                ..base.clone()
            },
        },
        Variant {
            name: "chrome-apple-extras",
            summary: "Chrome 153 特征 + x-skip-redirect + x-aos-ui-fetch-call-1",
            config: ClientConfig {
                profile: RequestProfile {
                    name: "chrome-153-apple-extras",
                    apple_extras: true,
                    ..chrome
                },
                warm_page: WarmPage::Bag,
                ..base
            },
        },
    ]
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Outcome {
    /// 拿到了明确的库存答复（有货或无货都算）。
    Ok,
    /// 被 Apple 拦截（HTTP 541 / 403 或返回了非 JSON 的拦截页）。
    Blocked,
    /// 其他失败：网络、限流、结构不符。
    Failed,
    /// 没有执行：前面连续被拦，为免加重风控评分停止。
    Skipped,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VariantReport {
    pub variant: &'static str,
    pub summary: &'static str,
    pub profile: &'static str,
    pub outcome: Outcome,
    pub detail: String,
    pub records: Vec<RequestRecord>,
}

pub async fn run<W: AsyncWrite + Unpin>(
    locale: String,
    store: String,
    parts: Vec<String>,
    interval: u64,
    json: bool,
    out: &mut W,
) -> Result<u8, CliError> {
    let region = region(&locale)?;
    if !(2..=12).contains(&store.len())
        || !store.starts_with('R')
        || !store[1..].bytes().all(|b| b.is_ascii_digit())
    {
        return Err(CliError::invalid(format!(
            "Invalid store number {store:?}; expected R followed by digits"
        )));
    }
    for part in &parts {
        if !valid_part_number(part) {
            return Err(CliError::invalid(format!(
                "Invalid part number {part:?}; use the SKU returned by apw products"
            )));
        }
    }

    // Apple Watch 的表壳要配表带才查得出真话，和正常查询保持一致。
    let catalog = Catalog::new();
    let mut all_parts = parts.clone();
    for part in &parts {
        if let Some(companion) = catalog
            .product_by_part(&locale, part)
            .and_then(|p| p.companion_part)
            && !all_parts.contains(&companion)
        {
            all_parts.push(companion);
        }
    }

    let interval = Duration::from_secs(interval.max(10));
    let mut reports = Vec::new();
    let mut consecutive_blocked = 0u32;

    for (i, variant) in variants().into_iter().enumerate() {
        let profile = variant.config.profile.name;
        if consecutive_blocked >= 2 {
            reports.push(VariantReport {
                variant: variant.name,
                summary: variant.summary,
                profile,
                outcome: Outcome::Skipped,
                detail: "前面两个变体连续被拦，为免加重风控评分而停止".into(),
                records: Vec::new(),
            });
            continue;
        }
        if i > 0 {
            tokio::time::sleep(interval).await;
        }
        let client = AppleClient::new(variant.config.clone())
            .map_err(|e| CliError::new(1, "client_error", e.to_string()))?;
        let result = client.pickup_message(region, &store, &all_parts).await;
        let records = client.recent_requests().await;
        let (outcome, detail) = match result {
            Ok(availability) => {
                consecutive_blocked = 0;
                let known = availability
                    .parts
                    .values()
                    .filter(|p| !p.availability.is_unknown())
                    .count();
                (
                    Outcome::Ok,
                    format!(
                        "返回 {} 个型号，{} 个有明确答复",
                        availability.parts.len(),
                        known
                    ),
                )
            }
            Err(ApiError::Blocked(detail)) => {
                consecutive_blocked += 1;
                (Outcome::Blocked, detail)
            }
            Err(err) => {
                consecutive_blocked = 0;
                (Outcome::Failed, err.to_string())
            }
        };
        reports.push(VariantReport {
            variant: variant.name,
            summary: variant.summary,
            profile,
            outcome,
            detail,
            records,
        });
    }

    if json {
        for report in &reports {
            json_line(
                out,
                &json!({"schemaVersion": SCHEMA_VERSION, "command": "doctor",
                    "locale": locale, "store": store, "parts": all_parts, "report": report}),
            )
            .await?;
        }
    } else {
        out.write_all(render_markdown(&locale, &store, &all_parts, interval, &reports).as_bytes())
            .await?;
        out.flush().await?;
    }
    Ok(if reports.iter().any(|r| r.outcome == Outcome::Ok) {
        0
    } else {
        3
    })
}

fn describe(records: &[RequestRecord], kind: &str) -> String {
    let Some(record) = records.iter().find(|r| r.kind == kind) else {
        return "—".into();
    };
    let status = record
        .status
        .map_or_else(|| record.outcome.clone(), |s| s.to_string());
    let version = record.http_version.as_deref().unwrap_or("?");
    let mut text = format!("{status} {version} {} ms", record.duration_ms);
    if kind == "warm" {
        if record.cookies_after.is_empty() {
            text.push_str(" · 无 cookie");
        } else {
            text.push_str(&format!(" · cookie: {}", record.cookies_after.join(", ")));
        }
    } else if !record.cookies_sent.is_empty() {
        text.push_str(&format!(" · 带 cookie: {}", record.cookies_sent.join(", ")));
    } else {
        text.push_str(" · 未带 cookie");
    }
    text
}

fn outcome_label(outcome: Outcome) -> &'static str {
    match outcome {
        Outcome::Ok => "通过",
        Outcome::Blocked => "被拦",
        Outcome::Failed => "失败",
        Outcome::Skipped => "未跑",
    }
}

/// 根据各变体的结果给出判读。只说结果之间能推出来的话，不替 Apple 下结论。
pub fn verdict(reports: &[VariantReport]) -> Vec<String> {
    let outcome = |name: &str| {
        reports
            .iter()
            .find(|r| r.variant == name)
            .map(|r| r.outcome)
    };
    let ran: Vec<&VariantReport> = reports
        .iter()
        .filter(|r| r.outcome != Outcome::Skipped)
        .collect();
    let mut lines = Vec::new();
    if ran.iter().all(|r| r.outcome == Outcome::Ok) {
        lines.push(
            "当前网络没有复现 541：所有变体都拿到了明确答复。请在出现 541 的时段和网络上再跑一次。"
                .into(),
        );
        return lines;
    }
    if ran.iter().all(|r| r.outcome == Outcome::Blocked) {
        lines.push("该网络此刻对所有变体都拦，这份结果无法区分原因。至少等 10 分钟，或换一条网络（例如手机热点）再跑一次。".into());
        return lines;
    }
    let chrome = outcome("chrome");
    if outcome("legacy") == Some(Outcome::Blocked) && chrome == Some(Outcome::Ok) {
        lines.push(
            "请求特征有影响：旧档案（Chrome/130 + X-Requested-With）被拦，Chrome 153 档案通过。"
                .into(),
        );
    }
    if outcome("chrome-no-warm") == Some(Outcome::Blocked) && chrome == Some(Outcome::Ok) {
        lines.push("cookie 有影响：不暖场被拦，购物袋页暖场后通过。".into());
    }
    if outcome("chrome-buypage-warm") == Some(Outcome::Blocked) && chrome == Some(Outcome::Ok) {
        lines.push(
            "会话 cookie 有影响：只带 geo 的购买页暖场被拦，带完整会话 cookie 的购物袋页暖场通过。"
                .into(),
        );
    }
    if outcome("chrome-xrw") == Some(Outcome::Blocked) && chrome == Some(Outcome::Ok) {
        lines.push("X-Requested-With 会触发拦截。".into());
    }
    if chrome == Some(Outcome::Blocked) && outcome("chrome-apple-extras") == Some(Outcome::Ok) {
        lines.push("Apple 自定义头（x-skip-redirect / x-aos-ui-fetch-call-1）有影响。".into());
    }
    if chrome == Some(Outcome::Blocked) && outcome("chrome-no-warm") == Some(Outcome::Ok) {
        lines.push(
            "反常：不带 cookie 反而通过。可能是暖场攒到的 cookie 已被作废，请附上完整报告。".into(),
        );
    }
    if lines.is_empty() {
        lines.push(
            "结果混合，没有单一变量能解释。请附上完整报告，并注明当时是否同时开着桌面版监控。"
                .into(),
        );
    }
    if reports.iter().any(|r| r.outcome == Outcome::Skipped) {
        lines.push("有变体因连续被拦而没有执行；等冷却后可以只重跑这些变体。".into());
    }
    lines
}

/// 生成给人看、也能直接贴进 issue 的报告。
pub fn render_markdown(
    locale: &str,
    store: &str,
    parts: &[String],
    interval: Duration,
    reports: &[VariantReport],
) -> String {
    let mut md = String::new();
    md.push_str("## apw doctor 报告\n\n");
    md.push_str(&format!(
        "- 版本：apw {}\n- 地区 / 门店：{locale} / {store}\n- 零件号：{}\n- 变体间隔：{} 秒，不重试，连续两次被拦即停\n- 本报告只含状态码、耗时与 cookie 名，不含 cookie 值、IP 或账号信息，可直接贴到 issue\n\n",
        env!("CARGO_PKG_VERSION"),
        parts.join(", "),
        interval.as_secs()
    ));
    md.push_str("| 变体 | 说明 | 暖场 | 取货 | 结果 |\n| --- | --- | --- | --- | --- |\n");
    for r in reports {
        let detail = if r.outcome == Outcome::Ok {
            r.detail.clone()
        } else {
            format!("{}：{}", outcome_label(r.outcome), r.detail)
        };
        md.push_str(&format!(
            "| `{}` | {} | {} | {} | {} |\n",
            r.variant,
            r.summary,
            describe(&r.records, "warm"),
            describe(&r.records, "pickup"),
            detail.replace('|', "\\|")
        ));
    }
    md.push_str("\n### 判读\n\n");
    for line in verdict(reports) {
        md.push_str(&format!("- {line}\n"));
    }
    md
}

#[cfg(test)]
mod tests {
    use super::*;

    fn report(variant: &'static str, outcome: Outcome) -> VariantReport {
        VariantReport {
            variant,
            summary: "",
            profile: "p",
            outcome,
            detail: String::new(),
            records: Vec::new(),
        }
    }

    #[test]
    fn 六个变体只改一个变量() {
        let list = variants();
        assert_eq!(list.len(), 6);
        assert!(
            list.iter().all(|v| v.config.max_retries == 0),
            "诊断绝不能重试"
        );
        assert_eq!(list[0].config.profile.name, "legacy-0.4.1");
        assert_eq!(list[1].config.profile, RequestProfile::chrome());
        assert_eq!(list[2].config.warm_page, WarmPage::None);
        assert!(list[4].config.profile.x_requested_with);
        assert!(list[5].config.profile.apple_extras);
    }

    #[test]
    fn 判读只说结果之间推得出的话() {
        let all_ok: Vec<_> = variants()
            .iter()
            .map(|v| report(v.name, Outcome::Ok))
            .collect();
        assert!(verdict(&all_ok)[0].contains("没有复现"));

        let mixed = vec![
            report("legacy", Outcome::Blocked),
            report("chrome", Outcome::Ok),
            report("chrome-no-warm", Outcome::Blocked),
            report("chrome-buypage-warm", Outcome::Ok),
            report("chrome-xrw", Outcome::Ok),
            report("chrome-apple-extras", Outcome::Skipped),
        ];
        let lines = verdict(&mixed);
        assert!(lines.iter().any(|l| l.contains("请求特征有影响")));
        assert!(lines.iter().any(|l| l.contains("cookie 有影响")));
        assert!(lines.iter().any(|l| l.contains("没有执行")));

        let md = render_markdown(
            "zh_CN",
            "R359",
            &["MJTF4CH/A".into()],
            Duration::from_secs(15),
            &mixed,
        );
        assert!(md.contains("| `legacy` |") && md.contains("被拦"));
        assert!(!md.contains("dssid2="), "报告里不能出现 cookie 值");
    }
}
