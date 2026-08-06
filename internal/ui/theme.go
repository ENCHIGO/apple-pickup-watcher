package ui

import (
	"encoding/binary"
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// chineseTheme 在系统默认主题的基础上只替换正文字体。
//
// 上游也做了同样的事（theme/theme.go:15-17），但它的 Font 对任何 TextStyle
// 都返回同一份中文字体，等宽文本和符号字体一并被顶掉 —— Fyne 的部分图标是
// 用符号字体渲染的，换掉之后这些图标就画不出来。这里只接管普通文本，
// 等宽与符号两种场景原样交回默认主题。
type chineseTheme struct {
	base fyne.Theme
	font fyne.Resource
}

var _ fyne.Theme = (*chineseTheme)(nil)

// NewChineseTheme 用一份 TTF 数据构造支持中文的主题。
//
// 数据不像字体时返回 error 而不是照单全收：把一份坏资源交给渲染器，问题会在
// 每一帧绘制时才暴露，报出来的错也和真实原因对不上号。调用方拿到 error 后应当
// 退回默认主题继续运行 —— 中文显示成方框很难看，但比程序起不来强。
func NewChineseTheme(name string, ttf []byte) (fyne.Theme, error) {
	if err := validateFont(ttf); err != nil {
		return nil, err
	}
	return &chineseTheme{
		base: theme.DefaultTheme(),
		font: fyne.NewStaticResource(name, ttf),
	}, nil
}

// Font 返回正文字体。
func (t *chineseTheme) Font(style fyne.TextStyle) fyne.Resource {
	if style.Monospace || style.Symbol {
		return t.base.Font(style)
	}
	// 这份字体只有一个字重，粗体/斜体也只能用它。返回默认字体虽然能拿到真正的
	// 粗体，但那份字体不含汉字，混排时中文又会变成方框，得不偿失。
	return t.font
}

// Color 沿用默认主题，跟随系统的浅色/深色设置。
func (t *chineseTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	return t.base.Color(name, variant)
}

// Icon 沿用默认主题。
func (t *chineseTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

// Size 沿用默认主题。
func (t *chineseTheme) Size(name fyne.ThemeSizeName) float32 {
	return t.base.Size(name)
}

// validateFont 粗略校验一份数据是不是字体文件。
//
// 只看开头的 sfnt 版本标记，不做完整解析：目的是拦住「资源没打进去」「文件被
// 截断」这类明显错误，而不是替渲染器把关。
func validateFont(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("字体数据为空或过短（%d 字节）", len(data))
	}
	switch tag := binary.BigEndian.Uint32(data[:4]); tag {
	case 0x00010000, // TrueType
		0x74727565, // "true"，早期 macOS 的 TrueType
		0x4F54544F, // "OTTO"，带 CFF 轮廓的 OpenType
		0x74746366: // "ttcf"，TrueType 字体集合
		return nil
	default:
		return fmt.Errorf("不是可识别的字体文件，起始标记为 0x%08X", tag)
	}
}
