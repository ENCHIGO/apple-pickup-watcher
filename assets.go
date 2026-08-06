// Package assets 把随二进制一起分发的静态资源编进程序。
//
// 这个包放在仓库根目录不是随意选的：go:embed 只能引用包目录及其子目录下的
// 文件，而字体与提示音都在仓库根的 assets/ 下，任何位于 internal/ 或 cmd/
// 的包都够不着它们。除非把资源复制一份到子目录（一份 4.6 MB 的字体没有理由
// 在仓库里存两遍），否则只能由根包来做这件事。
//
// 用 go:embed 而不是运行时按路径读文件，是为了消掉上游那个失败模式：上游按
// 相对路径去进程工作目录找资源（config/config.go 的 ReadConfigFile），打包成
// macOS .app 之后工作目录取决于应用被如何启动，读不到就在 services/store.go:22
// 直接 panic。资源编进二进制之后，这个失败模式从根上就不存在了。
package assets

import _ "embed"

// AlertMP3 是有货时播放的提示音。
//
//go:embed assets/alert.mp3
var AlertMP3 []byte

// FontTTF 是界面字体。
//
// Fyne 内置字体不含汉字，不换字体的话界面上所有中文都会渲染成方框。
//
//go:embed assets/font.ttf
var FontTTF []byte
