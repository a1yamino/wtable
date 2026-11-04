package main

import (
	ghostty "github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"wtable/internal/model"
)

var (
	deskBackground = ui.Hex("#111316")
	deskRail       = ui.Hex("#171A1E")
	deskSurface    = ui.Hex("#1C2025")
	deskRaised     = ui.Hex("#252B32")
	deskLine       = ui.Hex("#303740")
	deskText       = ui.Hex("#E8EDF3")
	deskMuted      = ui.Hex("#A0AAB7")
	deskFaint      = ui.Hex("#788492")
	deskAccent     = ui.Hex("#A0D6B4")
	deskAccentText = ui.Hex("#12251A")
	deskSelected   = ui.Hex("#26372F")
	deskTerminal   = ui.Hex("#111316")
)

func desktopTheme() *ui.Theme {
	t := ui.DarkTheme()
	t.Background, t.Surface = deskBackground, deskSurface
	t.SurfaceHover, t.SurfacePressed = deskRaised, ui.Hex("#303841")
	t.Border, t.Text, t.TextMuted = deskLine, deskText, deskMuted
	t.Accent, t.AccentHover, t.AccentPressed = deskAccent, ui.Hex("#B5E2C5"), ui.Hex("#8FC4A4")
	t.AccentText, t.Focus = deskAccentText, deskAccent
	t.Success, t.Warning, t.Danger = ui.Hex("#84C6A0"), ui.Hex("#E6BD77"), ui.Hex("#F0929B")
	t.Inverse, t.InverseText = deskRaised, deskText
	t.Radius, t.FontSize, t.Spacing = 6, 13, 3
	return t
}

func desktopTerminalTheme() *ghostty.Theme {
	t := ghostty.DarkTheme()
	t.Background, t.Foreground = deskTerminal, ui.Hex("#D5DDE7")
	t.Cursor, t.CursorText = deskAccent, deskTerminal
	t.Selection, t.SelectionText = ui.Hex("#34473F"), deskText
	t.Palette = [16]ui.Color{
		ui.Hex("#29323D"), ui.Hex("#DA8890"), ui.Hex("#94CDAA"), ui.Hex("#DBBD86"),
		ui.Hex("#91ADD2"), ui.Hex("#B5A2D5"), ui.Hex("#8FC4CB"), ui.Hex("#CFD8E3"),
		ui.Hex("#6F7C8C"), ui.Hex("#F1ADB2"), ui.Hex("#B4E5C5"), ui.Hex("#ECD3A7"),
		ui.Hex("#B6CDEC"), ui.Hex("#D3BDEB"), ui.Hex("#B2E0E3"), ui.Hex("#F0F4F8"),
	}
	return t
}

func quietButton(c *ui.Context, label string, icon *ui.SVG) ui.Element {
	button := ui.ButtonBase(c).Height(30).Padding(0, 10).Gap(6).
		Radius(5).AlignItems(ui.Center).Justify(ui.Center).Label(label).
		TextColor(deskMuted)
	if button.Hovered() {
		button.Background(deskRaised).TextColor(deskText)
	}
	if button.Pressed() {
		button.Background(c.Theme().SurfacePressed).TextColor(deskText)
	}
	if button.FocusVisible() {
		button.Border(1, deskAccent)
	}
	button.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(15, 15).Shrink(0)
		}
		if label != "" {
			ui.Text(c, label).FontSize(12).FontWeight(500).SingleLine()
		}
	})
	return button
}

func iconButton(c *ui.Context, label string, icon *ui.SVG) ui.Element {
	button := ui.ButtonBase(c).Size(28, 28).Padding(5).Radius(5).
		AlignItems(ui.Center).Justify(ui.Center).TextColor(deskMuted).
		Label(label).Tooltip(label)
	if button.Hovered() {
		button.Background(deskRaised).TextColor(deskText)
	}
	if button.Pressed() {
		button.Background(c.Theme().SurfacePressed)
	}
	if button.FocusVisible() {
		button.Border(1, deskAccent)
	}
	return button.Children(func() { ui.Icon(c, icon).Size(16, 16) })
}

func actionButton(c *ui.Context, label string, icon *ui.SVG) ui.Element {
	return ui.PrimaryButton(c, "").Height(30).Padding(0, 11).Gap(6).
		Radius(5).Label(label).Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(15, 15).Shrink(0)
		}
		ui.Text(c, label).FontSize(12).FontWeight(600).SingleLine()
	})
}

func compactMenu(c *ui.Context, label string, icon *ui.SVG, build func(*ui.Menu)) ui.Element {
	return iconButton(c, label, icon).PopoverMenuButton(build)
}

func smallBadge(c *ui.Context, label string, color ui.Color) ui.Element {
	return ui.Text(c, label).FontSize(10).FontWeight(500).SingleLine().
		TextColor(color).Background(color.Alpha(.09)).Padding(2, 5).Radius(4)
}

func agentGlyph(c *ui.Context, pane model.Pane, size float32) {
	color := deskMuted
	switch backendLabel(pane) {
	case "Codex":
		color = deskAccent
	case "Claude":
		color = ui.Hex("#D5A58B")
	case "Agy":
		color = ui.Hex("#91ACD1")
	}
	ui.Box(c).Size(size, size).Shrink(0).Radius(6).
		Background(color.Alpha(.08)).Center().Children(func() {
		ui.Icon(c, iconTerminal).Size(size*.55, size*.55).TextColor(color)
	})
}
