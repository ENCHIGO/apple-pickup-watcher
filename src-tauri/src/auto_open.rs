//! 每次监控会话只自动打开一次购买流程，避免多个目标同时到货打断结账。

#[derive(Default)]
pub(crate) struct AutoOpenBag {
    opened_this_run: bool,
}

impl AutoOpenBag {
    pub(crate) fn on_run_state_changed(&mut self, running: bool) {
        // 暂停不能重新布防：本轮已经排队的到货事件仍属于上一次会话。
        // 只有下一次真正开始监控时，才允许重新打开购物袋。
        if running {
            self.opened_this_run = false;
        }
    }

    pub(crate) fn open_if_needed<E>(
        &mut self,
        enabled: bool,
        url: Option<&str>,
        open: impl FnOnce(&str) -> Result<(), E>,
    ) -> Result<(), E> {
        if !enabled || self.opened_this_run {
            return Ok(());
        }
        let Some(url) = url else {
            return Ok(());
        };

        // 整个会话使用首个有效命中的地区。其他地区仍逐项提醒，但不再导航浏览器。
        // 打开失败不消耗这次机会，之后的到货事件可以重试。
        open(url)?;
        self.opened_this_run = true;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 多门店多颜色连续命中只打开一次购物袋() {
        let mut gate = AutoOpenBag::default();
        gate.on_run_state_changed(true);
        let mut opened = Vec::new();
        for _ in 0..15 {
            gate.open_if_needed(true, Some("https://www.apple.com.cn/shop/bag"), |url| {
                opened.push(url.to_owned());
                Ok::<_, ()>(())
            })
            .unwrap();
        }
        assert_eq!(opened, vec!["https://www.apple.com.cn/shop/bag"]);

        // 下一轮再次有货，或者另一个地区随后到货，都不打断当前购物袋。
        gate.open_if_needed(true, Some("https://www.apple.com/jp/shop/bag"), |_| {
            panic!("同一会话不应再次打开")
        })
        .unwrap_or_else(|_: ()| unreachable!());
    }

    #[test]
    fn 暂停后残余事件不重开而下一次开始可以重新打开() {
        let mut gate = AutoOpenBag::default();
        let mut opened = Vec::new();
        for running in [true, false, true] {
            gate.on_run_state_changed(running);
            gate.open_if_needed(true, Some("https://www.apple.com/jp/shop/bag"), |url| {
                opened.push(url.to_owned());
                Ok::<_, ()>(())
            })
            .unwrap();
        }
        assert_eq!(opened, vec!["https://www.apple.com/jp/shop/bag"; 2]);
    }

    #[test]
    fn 禁用和无效地区不会消耗会话的打开机会() {
        let mut gate = AutoOpenBag::default();
        gate.on_run_state_changed(true);
        for (enabled, url) in [
            (false, Some("https://www.apple.com.cn/shop/bag")),
            (true, None),
        ] {
            gate.open_if_needed(enabled, url, |_| panic!("不应打开购物袋"))
                .unwrap_or_else(|_: ()| unreachable!());
        }
        let mut opened = false;
        gate.open_if_needed(true, Some("https://www.apple.com.cn/shop/bag"), |_| {
            opened = true;
            Ok::<_, ()>(())
        })
        .unwrap();
        assert!(opened);
    }

    #[test]
    fn 打开失败会返回错误并允许下次命中重试() {
        let mut gate = AutoOpenBag::default();
        gate.on_run_state_changed(true);
        assert_eq!(
            gate.open_if_needed(true, Some("https://www.apple.com.cn/shop/bag"), |_| Err(
                "失败"
            )),
            Err("失败")
        );

        let mut retried = false;
        gate.open_if_needed(true, Some("https://www.apple.com.cn/shop/bag"), |_| {
            retried = true;
            Ok::<_, ()>(())
        })
        .unwrap();
        assert!(retried);
    }
}
