# 搜索与 AI 检索入口

公开入口分为 GitHub README 和 `site/` 静态介绍页。桌面应用依赖 Tauri IPC，
它的 `index.html` 不是公开网页；不要把桌面构建 `dist/` 部署成官网。

## 构建与预览

无需安装 npm 依赖，使用 Node.js LTS 与 Python 3：

```sh
node scripts/build-site.mjs
python3 scripts/check-site.py
python3 -m http.server 8080 --directory _site --bind 127.0.0.1
```

本地预览需与部署路径一致。想直接访问 `http://127.0.0.1:8080/`，先运行：

```sh
SITE_URL=https://preview.example/ node scripts/build-site.mjs
python3 scripts/check-site.py
```

这只是本地验证用的 URL，发布前重新运行默认构建。生产默认 URL 是
`https://enchigo.github.io/apple-pickup-watcher/`。生成物在 `_site/`，不提交到 Git。

## 首次发布

1. 在仓库 Settings → Pages 中将 Source 设置为 **GitHub Actions**。
2. 通过 PR 合并站点代码到 main。Website 工作流仅在 main 发布；PR 只构建和检查。
3. 验证中英文页、CSS、分享图片、`sitemap.xml`、`llms.txt` 均返回 200，正文直接出现在 HTML 中。
4. 发布成功后，把仓库 About 的 Website 改成上述介绍页 URL，并在 README 顶部加入介绍页链接。
5. 在 Google Search Console 和 Bing Webmaster Tools 使用自己的账号验证站点，并提交
   `https://enchigo.github.io/apple-pickup-watcher/sitemap.xml`。验证文件或 meta token 必须使用平台实际签发的值。

如果使用独立域名，先在 Pages 配置并验证域名，再设置仓库 Actions 变量 `SITE_URL`
为最终 HTTPS 地址，重新执行 Website 工作流。它同时控制 canonical、hreflang、
分享图片绝对地址、站点地图与资源路径；同步修改 README 和 GitHub About 的入口链接。

## 内容维护约定

- `site/content.mjs` 是中英文正文与 FAQ 的单一来源。FAQ 的 JSON-LD 使用同一份问答，
  不添加隐藏内容、虚构评价或用户数量。下载链接使用 `releases/latest`，不硬编码安装包版本。
- 产品范围来自 `crates/apw-core/src/model.rs`，平台包来自公开 Release。
  发布新型号支持后再更新宣传；不把源码分支中的未发布能力写成已提供。
- `llms.txt` 是可选的事实与文档索引。核心内容完整存在于 HTML 中，不能依赖这个文件获得收录或 AI 引用。
  功能范围变化时同步修改构建脚本中的事实摘要。
- 错误原因以日志和可复现证据为准，不把一次 HTTP 541、某个网络的失败或过去的观察概括成普遍结论。
- 没有运行时 JS、远程字体、跟踪脚本或网站库存请求。图中的库存状态明确标为示意。

### robots.txt 的路径边界

搜索爬虫读取的是 **域名根目录** `/robots.txt`。GitHub 项目 Pages 下的
`/apple-pickup-watcher/robots.txt` 不控制抓取，所以子路径部署不会生成它。
需要检查 `https://enchigo.github.io/robots.txt` 是否存在限制；若有限制，应由该用户站点的维护者修改，
不能在本项目里假装已经解除。站点地图可通过站长平台直接提交。
当 `SITE_URL` 指向独立域名根目录时，构建器会生成允许抓取并声明 sitemap 的 robots.txt。

## 如何判断有效

先验证 URL 可访问、抓取无阻挡、canonical 与语言版本正确，再观察收录和自然搜索表现。
在站长平台记录发布前后的展示量、点击量、查询词与被引用页面；按周比较，避免把一次手动搜索当成排名结论。
可以持续观察“苹果直营店库存监控”“iPhone 到货提醒”“Apple Store pickup stock monitor”
等与实际功能一致的查询。网站上线和技术检查通过，都不等于搜索排名或 AI 推荐已经提升。

官方参考：

- [Google：AI features and your website](https://developers.google.com/search/docs/appearance/ai-features)
- [Google：robots.txt](https://developers.google.com/search/docs/crawling-indexing/robots/intro)
- [GitHub Pages 自定义工作流](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages)
