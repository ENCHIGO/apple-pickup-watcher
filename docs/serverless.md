# 无服务器常驻方案 (Serverless Headless Deployment)

桌面版与 CLI 版要求宿主电脑保持开机并处于联网唤醒状态。若希望在电脑关机、外出或无个人服务器的情况下 7×24 小时常驻监控并接收到货提醒，可以采用基于 **Cloudflare Workers + D1 + Gmail / Bark Relay** 的无服务器（Serverless）架构。

参考实现：[landerhe/hk-iphone-monitor](https://github.com/landerhe/hk-iphone-monitor)（开源于 GPL-3.0）。

---

## 架构原理

```
[Cloudflare Cron Trigger (* * * * *)]
                  │
                  ▼
         [Cloudflare Worker]
                  │
     ┌────────────┴────────────┐
     ▼                         ▼
[D1 分布式租约锁]        [Apple Retail 接口查询]
(防止并发重入)            (/shop/retail/pickup-message)
                               │
                               ▼
                        [库存状态状态机]
                        (有货 / 无货 / 未知)
                               │
                 ┌─────────────┴─────────────┐
                 ▼                           ▼
          [状态与历史落盘]             [到货事件通知]
             (Cloudflare D1)      (Gmail Apps Script / Bark)
```

1. **调度与边缘计算**：利用 Cloudflare Cron 每分钟唤醒边缘 Worker，执行随机抖动（45–90 秒）的主动探测。
2. **分布式排他锁（Distributed Lease Lock）**：通过 D1 数据库执行原子更新 `UPDATE monitor SET lock_until=?, lock_token=? WHERE id=1 AND lock_until<?`，确保即使遇到 Cloudflare 多节点并发调度，同一时间也只有一个 Worker 实例查询 Apple 接口。
3. **退避与防封保护**：严格继承 `apple-pickup-watcher` 的调度原则，当遇到 HTTP 403 / 429 / 541 拦截时，触发随机冷却阶梯（15m -> 35m -> 65m -> 125m），避免连续盲目请求导致出口 IP 永久受限。
4. **轻量提醒 Relay**：通过免费的 Google Apps Script（固定收件人与防重 Token）或 Bark Webhook 实现实时邮件/手机推送，无需自建 SMTP 服务器或维护域名。

---

## 资源成本与免费额度

根据 Cloudflare 与 Google 当前的开发者配额：
- **Cloudflare Workers**：每日 100,000 次请求（单分钟 Cron 调度约 1,440 次/日，远低于上限）。
- **Cloudflare D1**：每日 5,000,000 次读行与 100,000 次写行，完全落在免费层级内。
- **Google Apps Script**：个人账户每日最多 100 封邮件配额，因到货通知进行了状态机去重（只有“持续无货后首次有货”才会触发），日常运行零消耗。

---

## 部署概览

详细配置与自动化脚本见参考仓库：
1. **初始化数据库**：
   ```bash
   npx wrangler d1 create apple-pickup-monitor
   npx wrangler d1 execute apple-pickup-monitor --remote --file schema.sql
   ```
2. **配置鉴权密钥**：
   ```bash
   npx wrangler secret put ADMIN_TOKEN
   npx wrangler secret put MAIL_RELAY_URL
   npx wrangler secret put MAIL_RELAY_SECRET
   ```
3. **发布上线**：
   ```bash
   npm run deploy
   ```

发布完成后，您即可在手机浏览器访问 `https://<your-worker>.workers.dev` 仪表盘查看 7×24 小时各门店最新库存状态与检测日志。
