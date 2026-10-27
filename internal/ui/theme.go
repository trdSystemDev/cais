package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Cores da TUI. Regra: o que tem fundo proprio (botoes, abas, item em foco)
// define fundo E texto em RGB, nunca pela paleta ANSI do terminal -- cada
// tema remapeia "azul" e "amarelo" e a combinacao pode ficar ilegivel (o
// padrao do tview, texto amarelo em fundo azul, sumia num tema claro). O
// resto usa as cores do proprio terminal, que o tema ja deixa legiveis.
var (
	rgbText   = tcell.NewRGBColor(0xf2, 0xf4, 0xf8)
	rgbInk    = tcell.NewRGBColor(0x16, 0x1a, 0x21)
	rgbSlate  = tcell.NewRGBColor(0x3b, 0x47, 0x5a)
	rgbAccent = tcell.NewRGBColor(0x7f, 0xd1, 0xae)
	rgbOff    = tcell.NewRGBColor(0x2b, 0x30, 0x38)
	rgbOffTxt = tcell.NewRGBColor(0x8b, 0x93, 0xa1)
	rgbDanger = tcell.NewRGBColor(0xb3, 0x3a, 0x3a)
	rgbField  = tcell.NewRGBColor(0x2f, 0x37, 0x46)
	rgbHint   = tcell.NewRGBColor(0x9a, 0xa3, 0xb2)

	// campos: fundo e texto explicitos (o Form troca o estilo inteiro do
	// campo pelas cores dele a cada desenho, entao atributo como sublinhado
	// nao sobrevive; so cor)
	styleField       = tcell.StyleDefault.Foreground(rgbText).Background(rgbField)
	stylePlaceholder = tcell.StyleDefault.Foreground(rgbHint).Background(rgbField).Italic(true)

	styleButton         = tcell.StyleDefault.Foreground(rgbText).Background(rgbSlate)
	styleButtonFocus    = tcell.StyleDefault.Foreground(rgbInk).Background(rgbAccent).Bold(true)
	styleButtonDisabled = tcell.StyleDefault.Foreground(rgbOffTxt).Background(rgbOff)

	styleTab       = tcell.StyleDefault
	styleTabFocus  = styleButton
	styleTabActive = styleButtonFocus
	styleQuit      = tcell.StyleDefault.Foreground(rgbText).Background(rgbDanger)
)

// Tags de texto: secundario e atenuado sobre a cor do terminal ([gray] e
// uma cor da paleta e some em alguns temas); normal volta tudo ao padrao.
const (
	tagMuted  = "[-::d]"
	tagNormal = "[-::-]"
)

// applyTheme troca os padroes globais do tview, que vem da paleta (fundo
// preto, borda e titulo brancos: somem num tema claro), pelas cores do
// proprio terminal. Elementos com fundo proprio usam os pares RGB acima.
func applyTheme() {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.BorderColor = tcell.ColorDefault
	tview.Styles.TitleColor = tcell.ColorDefault
	tview.Styles.GraphicsColor = tcell.ColorDefault
	tview.Styles.PrimaryTextColor = tcell.ColorDefault
	tview.Styles.SecondaryTextColor = tcell.ColorDefault
	tview.Styles.TertiaryTextColor = tcell.ColorDefault
	tview.Styles.ContrastBackgroundColor = rgbField
	tview.Styles.MoreContrastBackgroundColor = rgbSlate
	tview.Styles.InverseTextColor = rgbInk
	tview.Styles.ContrastSecondaryTextColor = rgbHint
}

// styleForm aplica o tema num formulario e nos itens dele. O Form so passa
// cores aos itens a cada desenho, entao os atributos (sublinhado, foco)
// ficam em cada item.
func styleForm(f *tview.Form) {
	f.SetLabelColor(tcell.ColorDefault)
	f.SetFieldStyle(styleField)
	f.SetButtonStyle(styleButton)
	f.SetButtonActivatedStyle(styleButtonFocus)
	f.SetButtonDisabledStyle(styleButtonDisabled)
	for i := range f.GetFormItemCount() {
		switch it := f.GetFormItem(i).(type) {
		case *tview.InputField:
			it.SetPlaceholderStyle(stylePlaceholder)
		case *tview.Checkbox:
			// as strings aceitam tags de estilo: "[x]" sem escape some
			it.SetCheckedString(tview.Escape("[x]"))
			it.SetUncheckedString(tview.Escape("[ ]"))
			it.SetActivatedStyle(styleButtonFocus)
		case *tview.DropDown:
			it.SetFocusedStyle(styleButtonFocus)
			it.SetListStyles(styleButton, styleButtonFocus)
		}
	}
}
