# 桌面版指南

[返回 README](../README.md) · [CLI / agent](cli.md)

## 安装

从 [Releases](https://github.com/ENCHIGO/apple-pickup-watcher/releases) 选择系统和架构对应的安装包。

- **macOS**：区分 Apple Silicon 和 Intel。应用未经过 Apple 公证，放入「应用程序」后，如果被系统拦下，可以对这个应用执行 `xattr -cr "/Applications/Apple Pickup Watcher.app"`。
- **Windows**：安装包未签名，SmartScreen 可能提示「Windows 已保护你的电脑」。确认下载来源后，选择「更多信息」→「仍要运行」。
- **Linux**：按 Release 提供的格式安装。AppImage 需要可执行权限：`chmod +x 下载的文件.AppImage`。系统要求以对应 Release 说明为准。

## 监控与提醒

选择地区、品类、门店和型号，添加目标后点击「开始」。可以同时监控多个门店和商品。

窗口关闭后应用会收进托盘，Rust 后台继续查询和提醒。真正退出需使用托盘菜单的「退出」。电脑需要联网并保持唤醒。

默认查询间隔 30 秒，下限 5 秒；实际间隔还包括抖动和失败退避。持续有货不会每轮重复提醒；从无货或未知再次变成有货时重新提醒。暂停后再开始会重新启用提醒，下一次查询确认仍有货也会再次提醒。

有货后会继续查询全部目标，表格的「最后检查」时间可用于确认是否还在轮询。每次开始到暂停期间，按设置最多自动打开一次购物袋；其他目标仍然提醒。添加商品、选择取货门店、结账和付款由用户在 Apple 官网完成。

## 型号目录

「从 Apple 官网更新型号列表」只刷新当前品类的已配置购买页；失败的页面保留旧数据并显示错误。新增购买页的机型可能需要先更新程序，不会自动发现所有新页面。内置快照中的商品不代表目前仍在售或有货。

### Apple Watch 按表壳监控

目录里的 Apple Watch 型号是表壳（尺寸、材质、颜色、连接方式），不含表带。Apple 的取货接口只把「表壳 + 表带」当作可售组合：单独查表壳，要么返回空响应，要么把表壳报成「不支持到店取货」，后者会显示成假的「无货」。因此程序会在同一次查询里自动附带该购买页的一条表带零件号，状态取表壳自己的结果；表带不占状态行，也不影响判定。旧版本添加的 Apple Watch 目标会在启动时自动补齐表带，无需重新添加。

应用内更新会先提示，由用户决定是否安装。

## Bark 推送

安装 [Bark](https://github.com/Finb/Bark)，将它提供的完整设备地址填入「Bark 推送地址」，留空表示关闭。可保留自定义查询参数，例如：

```text
https://api.day.app/你的Key?group=库存&sound=alarm
```

点击「测试提醒」验证系统通知、声音和 Bark。推送失败会显示渠道错误，不会改变库存判定。分享日志时请隐去设备 key。

## 配置文件

| 平台 | 路径 |
| --- | --- |
| macOS | `~/Library/Application Support/apple-pickup-watcher/settings.v2.json` |
| Windows | `%APPDATA%\apple-pickup-watcher\settings.v2.json` |
| Linux | `$XDG_CONFIG_HOME/apple-pickup-watcher/settings.v2.json`，默认 `~/.config/apple-pickup-watcher/settings.v2.json` |

新版配置与 Go 版的 `settings.json` 分开。迁移只读旧文件；写入采用原子替换。遇到损坏配置会尝试留档并显示原因和位置，避免用默认配置覆盖旧内容。CLI 使用显式参数或目标文件，不写桌面版配置。

## 常见问题

### 未知、待查询和无货有什么区别？

「待查询」表示还没有完成首次查询；「未知」表示没有取得明确结果，原因可能是拦截、限流、网络失败或响应结构变化；「无货」表示本次查询得到明确的不可取货结果。

看到「监控当前不可信」时，检查具体目标的原因与最后查询时间。不能把未知或过期的结果当作当前无货。

### HTTP 541，或浏览器正常但程序查询失败？

程序将这类响应归为被拦截，不能据此判断库存。记录应用版本、地区、门店、SKU、错误和出现时间，可暂停后稍后重试，或按界面建议检查网络。持续失败时提交 [Issue](https://github.com/ENCHIGO/apple-pickup-watcher/issues)。

浏览器与程序的会话和网络请求条件可能不同；单台机器查询成功，也不能证明其他网络上的问题已修复。

### 和原项目有什么关系？

本项目重写了 [apple-store-helper](https://github.com/hteen/apple-store-helper) 的客户端与界面，使用 `/shop/retail/pickup-message` 查询，并将失败保留为带原因的未知。项目同时保留离线回归与单独的真实接口契约测试。来源与修改说明见 [NOTICE](../NOTICE)。
