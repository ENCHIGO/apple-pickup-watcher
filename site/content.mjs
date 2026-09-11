// Visible page copy and structured data share these facts and answers.
export const site = {
  "name": "Apple Pickup Watcher",
  "url": "https://enchigo.github.io/apple-pickup-watcher/",
  "repository": "https://github.com/ENCHIGO/apple-pickup-watcher"
};

export const locales = [
  {
    "lang": "zh-CN",
    "path": "",
    "ogLocale": "zh_CN",
    "switchLabel": "English",
    "switchPath": "en/",
    "title": "Apple Pickup Watcher — 苹果直营店库存监控与到货提醒",
    "description": "免费开源的 Apple 到店取货库存监控工具，支持 iPhone、iPad、Mac、Apple Watch 和七个地区。适用于 macOS、Windows、Linux，提供系统通知、提示音与 Bark 推送，明确区分无货与查询失败。",
    "skip": "跳到正文",
    "source": "查看源代码",
    "download": "下载最新版本",
    "intro": "选择你想买的型号和取货门店，程序会定期查询库存，有货时通过系统通知、提示音或 Bark 提醒你。免费开源，支持 macOS、Windows 和 Linux。",
    "features": [
      [
        "后台运行",
        "关闭窗口后收进系统托盘，继续查询和提醒。需要保持应用运行、电脑联网且处于唤醒状态。"
      ],
      [
        "库存状态",
        "分别显示有货、无货、未知和待查询。查询失败会给出原因，不会把失败当成无货。"
      ],
      [
        "到货提醒",
        "支持系统通知、提示音和可选的 Bark 推送。持续有货不会每轮重复提醒；离开有货状态后再次有货，会重新提醒。"
      ],
      [
        "CLI 与 agent skill",
        "独立命令行 apw 输出 JSON，watch 逐行输出 NDJSON，无需桌面环境；配套 skill 让 Codex 等 agent 直接查询库存或等待到货。CLI 本身不弹窗、不推送、不下单。"
      ]
    ],
    "screenshotAlt": "Apple Pickup Watcher 桌面版截图：监控列表逐行显示门店、型号、有货 / 无货 / 未知状态与最后检查时间",
    "keywords": "苹果直营店库存监控, Apple Store 到店取货, iPhone 到货提醒, iPhone 库存查询, Apple Store 库存提醒, apple-store-helper 替代, Bark 推送",
    "coverageHeading": "支持范围",
    "categoriesLabel": "产品",
    "regionsLabel": "地区",
    "categories": "iPhone / iPad / Mac / Apple Watch",
    "regions": "中国大陆、中国香港、中国台湾、日本、新加坡、澳大利亚、马来西亚。",
    "coverageNote": "具体型号和门店取决于应用目录与当地 Apple 在线商店。可从已支持的购买页更新型号列表；尚未配置的新购买页需要程序更新。支持某地区不代表每次查询均能成功。",
    "stepsHeading": "使用方法",
    "steps": [
      [
        "添加监控目标。",
        "选择地区、品类、门店和型号。可以同时关注不同门店的多个型号。"
      ],
      [
        "检查通知设置并开始。",
        "用“测试提醒”确认声音与推送有效。默认每 30 秒查询一次。"
      ],
      [
        "收到提醒后去官网购买。",
        "程序可按设置打开购物袋；添加商品、选择门店、结账和付款均需手动完成。"
      ]
    ],
    "downloadHeading": "下载与安装",
    "downloadLead": "安装包发布在 GitHub Releases，请按系统和芯片架构选择。",
    "packages": [
      [
        "macOS",
        "Apple Silicon / Intel · DMG"
      ],
      [
        "Windows",
        "x64 · EXE / MSI"
      ],
      [
        "Linux",
        "x86_64 · AppImage / DEB / RPM"
      ]
    ],
    "installGuide": "安装说明",
    "installNote": "macOS 版本未经 Apple 公证，Windows 安装包未做代码签名。安装时遇到系统拦截，请参照",
    "faqHeading": "常见问题",
    "faqs": [
      [
        "它会自动下单或保证买到吗？",
        "不会。它查询到店取货库存并发送提醒，也可按设置打开购物袋。添加商品、选择取货门店、结账和付款由用户在 Apple 官网完成。库存可能在提醒后变化，程序不预留库存，也不保证购得。"
      ],
      [
        "关闭窗口后，还能收到到货通知吗？",
        "可以。关闭窗口会收进系统托盘，后台继续查询和提醒。需要保持应用运行、电脑联网且处于唤醒状态；从托盘退出应用后不会继续监控。"
      ],
      [
        "“未知”、HTTP 541 和“无货”有什么区别？",
        "无货表示查询返回了可识别的不可取货结果；未知表示此次查询不能确认库存，原因可能是网络失败、限流、拦截或响应结构变化。HTTP 541 应当作为查询失败处理，不能据此判断无货；具体原因需要结合日志和网络环境确认。"
      ],
      [
        "如何在手机上接收到货提醒？",
        "在 iPhone 上安装 Bark，将自己的 Bark 推送地址填入应用设置，再点击“测试提醒”。Bark 为可选通知渠道，推送失败不会改变库存状态。"
      ],
      [
        "这是 Apple 官方软件吗？",
        "不是。Apple Pickup Watcher 是独立开源项目，与 Apple Inc. 无关联，未获其授权或认可。它是 hteen/apple-store-helper 的 Rust + Tauri 重写版本，来源与版权声明见仓库 NOTICE。"
      ]
    ],
    "evidenceLinks": [
      "README",
      "版本记录",
      "提交问题",
      "来源声明"
    ],
    "footer": "独立开源项目，与 Apple Inc. 无关联。Apple 等名称属于其各自权利人。库存以 Apple 官网确认结果为准。",
    "facts": "项目事实摘要",
    "sitemap": "站点地图",
    "subtitle": "Apple 直营店到店取货库存监控",
    "tableHead": [
      "系统",
      "安装包",
      "下载"
    ],
    "packageLink": "下载",
    "featuresHeading": "监控与通知",
    "resourcesHeading": "文档与反馈",
    "resourcesText": "详细用法见 README。遇到问题，可在 GitHub Issues 中附上应用版本、地区和脱敏后的日志。"
  },
  {
    "lang": "en",
    "path": "en/",
    "ogLocale": "en_US",
    "switchLabel": "简体中文",
    "switchPath": "",
    "title": "Apple Pickup Watcher — Apple Store Stock & Restock Alerts",
    "description": "Free, open-source Apple Store pickup stock monitor for iPhone, iPad, Mac and Apple Watch in seven regions. Get desktop and optional Bark restock alerts on macOS, Windows and Linux, with clear query failure states.",
    "skip": "Skip to content",
    "source": "Source code",
    "download": "Download latest release",
    "intro": "Choose a model and a pickup store. The app checks availability periodically and alerts you through desktop notifications, sound or Bark when it finds stock. Free and open source for macOS, Windows and Linux.",
    "features": [
      [
        "Background monitoring",
        "Closing the window moves the app to the system tray, where it keeps checking and notifying. Keep the app running and your computer awake and online."
      ],
      [
        "Stock status",
        "Available, unavailable, unknown and pending states are displayed separately. A failed query shows its reason instead of being reported as out of stock."
      ],
      [
        "Notifications",
        "Desktop notifications, sound and optional Bark push are supported. Continuous availability does not trigger an alert every cycle. A target that leaves and re-enters the available state triggers a new alert."
      ],
      [
        "CLI and agent skill",
        "The standalone apw command prints JSON and its watch mode streams NDJSON, with no desktop environment required. A bundled skill lets Codex-style agents query stock or wait for a restock. The CLI itself shows no popups, sends no pushes and places no orders."
      ]
    ],
    "screenshotAlt": "Screenshot of the Apple Pickup Watcher desktop app: a watch list showing store, model, in-stock / out-of-stock / unknown state and last-checked time per row",
    "keywords": "Apple Store stock checker, Apple Store pickup availability, iPhone restock alert, iPhone in-store stock monitor, Apple Store stock notifier, apple-store-helper alternative, Bark push",
    "coverageHeading": "Supported products and regions",
    "categoriesLabel": "Products",
    "regionsLabel": "Regions",
    "categories": "iPhone / iPad / Mac / Apple Watch",
    "regions": "China mainland, Hong Kong, Taiwan, Japan, Singapore, Australia and Malaysia.",
    "coverageNote": "Individual models and stores depend on the app catalog and the regional Apple online store. The catalog can refresh from supported purchase pages; a new purchase-page route needs an app update. Region support does not guarantee every query will succeed.",
    "stepsHeading": "Getting started",
    "steps": [
      [
        "Add a target.",
        "Choose a region, product family, store and model. You can watch multiple models at different stores."
      ],
      [
        "Test notifications and start.",
        "Use the test notification control to check sound and push settings. The default polling interval is 30 seconds."
      ],
      [
        "Buy on Apple’s website.",
        "The app can open your shopping bag if enabled. Add the item, select a pickup store, check out and pay manually."
      ]
    ],
    "downloadHeading": "Download",
    "downloadLead": "Installers are published on GitHub Releases. Choose the package for your operating system and processor.",
    "packages": [
      [
        "macOS",
        "Apple Silicon / Intel · DMG"
      ],
      [
        "Windows",
        "x64 · EXE / MSI"
      ],
      [
        "Linux",
        "x86_64 · AppImage / DEB / RPM"
      ]
    ],
    "installGuide": "installation instructions",
    "installNote": "The macOS app is not notarized by Apple, and Windows installers are not code-signed. If your system blocks the app, see the",
    "faqHeading": "Frequently asked questions",
    "faqs": [
      [
        "Does it automatically buy or reserve a device?",
        "No. It checks pickup availability and sends alerts. It can optionally open your shopping bag, but you add items, select a pickup store, check out and pay manually on Apple’s website. Stock may change after an alert. The app does not reserve stock or guarantee a purchase."
      ],
      [
        "Will alerts work after I close the window?",
        "Yes. Closing the window moves the app to the system tray, where it keeps checking and notifying. The app must stay running and your computer must stay awake and online. Quitting from the tray stops monitoring."
      ],
      [
        "What do unknown and HTTP 541 mean?",
        "Out of stock means a query returned a recognized unavailable pickup result. Unknown means the query could not establish availability, for example due to a network failure, rate limit, block or changed response. Treat HTTP 541 as a failed query, not evidence of no stock. Its cause requires logs and network context."
      ],
      [
        "Can I receive restock alerts on my phone?",
        "Yes, with the optional Bark notification channel. Install Bark on your iPhone, enter your own Bark push URL in the app settings, then send a test notification. A failed push does not change the stock status."
      ],
      [
        "Is this an official Apple application?",
        "No. Apple Pickup Watcher is an independent open-source project, unaffiliated with and not endorsed by Apple Inc. It is a Rust + Tauri rewrite of hteen/apple-store-helper. Attribution and copyright details are in the repository NOTICE."
      ]
    ],
    "evidenceLinks": [
      "README",
      "Release notes",
      "Report an issue",
      "Attribution"
    ],
    "footer": "An independent open-source project. Not affiliated with or endorsed by Apple Inc. Product names belong to their respective owners. Confirm availability on Apple’s website.",
    "facts": "Project facts",
    "sitemap": "Sitemap",
    "subtitle": "Apple Store pickup stock monitor",
    "tableHead": [
      "System",
      "Package",
      "Download"
    ],
    "packageLink": "Download",
    "featuresHeading": "Monitoring and alerts",
    "resourcesHeading": "Documentation and feedback",
    "resourcesText": "The README contains full setup instructions. Report problems on GitHub Issues with the app version, region and logs with personal information removed."
  }
];
