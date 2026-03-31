package main

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/miekg/dns"
)

const (
	InputFile    = "input.txt"
	SettingsFile = "settings.txt"
	OutputFile   = "output.txt"
)

const defaultInput = `# Google
google.com
`

const defaultSettings = `server=dns.google
port=8443
ipv4=true
ipv6=false
`

type TabMode int
const (
	TabLog TabMode = iota
	TabInput
	TabSettings
)

type ProgressState int
const (
	ProgressIdle ProgressState = iota
	ProgressRunning
	ProgressDone
)

type ThemeMode int
const (
	ThemeDark ThemeMode = iota
	ThemeLight
)

type AppState struct {
	mu            sync.Mutex
	window        *app.Window
	theme         *material.Theme
	themeMode     ThemeMode
	ThemeBtn      widget.Clickable
	currentTab    TabMode
	TabLogBtn     widget.Clickable
	TabInputBtn   widget.Clickable
	TabSetBtn     widget.Clickable
	StartBtn      widget.Clickable
	StopBtn       widget.Clickable
	ClearLogBtn   widget.Clickable
	CancelBtn     widget.Clickable
	SaveBtn       widget.Clickable
	CopyBtn       widget.Clickable
	ClearBtn      widget.Clickable
	ResetDNSBtn   widget.Clickable
	inputEditor   widget.Editor
	settingsEd    widget.Editor
	logList       widget.List
	logEntries[]string
	isRunning     bool
	cancelFunc    context.CancelFunc
	progress      float32
	progressState ProgressState
}

func main() {
	if _, err := os.Stat(InputFile); os.IsNotExist(err) {
		os.WriteFile(InputFile,[]byte(defaultInput), 0644)
	}
	if _, err := os.Stat(SettingsFile); os.IsNotExist(err) {
		os.WriteFile(SettingsFile,[]byte(defaultSettings), 0644)
	}

	go func() {
		window := new(app.Window)
		window.Option(app.Title("DNStoHOSTS"), app.Size(unit.Dp(800), unit.Dp(600)))
		
		th := material.NewTheme()
		th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

		state := &AppState{
			window:        window,
			theme:         th,
			themeMode:     ThemeDark,
			currentTab:    TabLog,
			progressState: ProgressIdle,
			logEntries:[]string{},
		}
		state.logList.Axis = layout.Vertical
		state.loadEditorsFromFiles()
		state.applyThemeColors()

		var ops op.Ops
		for {
			switch e := window.Event().(type) {
			case app.DestroyEvent:
				os.Exit(0)
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				state.handleEvents(gtx)
				state.layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}

func hex2color(hex string) color.NRGBA {
	var r, g, b, a uint8 = 0, 0, 0, 255
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 6 {
		fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	} else if len(hex) == 8 {
		fmt.Sscanf(hex, "%02x%02x%02x%02x", &r, &g, &b, &a)
	}
	return color.NRGBA{R: r, G: g, B: b, A: a}
}

func (s *AppState) applyThemeColors() {
	if s.themeMode == ThemeDark {
		s.theme.Bg = hex2color("#202020")
		s.theme.Fg = hex2color("#FFFFFF")
		s.theme.ContrastBg = hex2color("#333333")
		s.theme.ContrastFg = hex2color("#FFFFFF")
	} else {
		s.theme.Bg = hex2color("#FFFFFF")
		s.theme.Fg = hex2color("#000000")
		s.theme.ContrastBg = hex2color("#E0E0E0")
		s.theme.ContrastFg = hex2color("#000000")
	}
}

func (s *AppState) loadEditorsFromFiles() {
	inBytes, _ := os.ReadFile(InputFile)
	s.inputEditor.SetText(string(inBytes))
	setBytes, _ := os.ReadFile(SettingsFile)
	s.settingsEd.SetText(string(setBytes))
}

func (s *AppState) appendLog(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := time.Now().Format("15:04:05")
	s.logEntries = append(s.logEntries, fmt.Sprintf("[%s] %s", ts, msg))
	s.logList.Position.First = len(s.logEntries)
	s.window.Invalidate()
}

func (s *AppState) clearLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logEntries =[]string{}
	s.progressState = ProgressIdle
	s.progress = 0
	s.window.Invalidate()
}

func (s *AppState) setProgress(p float32, state ProgressState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = p
	s.progressState = state
	s.window.Invalidate()
}

func (s *AppState) handleEvents(gtx layout.Context) {
	if s.ThemeBtn.Clicked(gtx) {
		if s.themeMode == ThemeDark {
			s.themeMode = ThemeLight
		} else {
			s.themeMode = ThemeDark
		}
		s.applyThemeColors()
	}
	if s.TabLogBtn.Clicked(gtx) { s.currentTab = TabLog }
	if s.TabInputBtn.Clicked(gtx) { s.currentTab = TabInput }
	if s.TabSetBtn.Clicked(gtx) { s.currentTab = TabSettings }

	if s.StartBtn.Clicked(gtx) && !s.isRunning { s.startResolving() }
	if s.StopBtn.Clicked(gtx) && s.isRunning {
		s.appendLog("Stop requested, waiting for current operation to complete...")
		if s.cancelFunc != nil { s.cancelFunc() }
	}
	if s.ClearLogBtn.Clicked(gtx) && !s.isRunning { s.clearLog() }

	if s.CancelBtn.Clicked(gtx) { s.loadEditorsFromFiles() }
	if s.SaveBtn.Clicked(gtx) {
		if s.currentTab == TabInput { os.WriteFile(InputFile,[]byte(s.inputEditor.Text()), 0644) }
		if s.currentTab == TabSettings { os.WriteFile(SettingsFile,[]byte(s.settingsEd.Text()), 0644) }
	}
	if s.CopyBtn.Clicked(gtx) {
		txt := s.inputEditor.Text()
		if s.currentTab == TabSettings { txt = s.settingsEd.Text() }
		clipboard.WriteOp{Text: txt}.Add(gtx.Ops)
	}
	if s.ClearBtn.Clicked(gtx) {
		if s.currentTab == TabInput { s.inputEditor.SetText("") }
		if s.currentTab == TabSettings { s.settingsEd.SetText("") }
	}
	if s.ResetDNSBtn.Clicked(gtx) && s.currentTab == TabSettings {
		s.settingsEd.SetText(defaultSettings)
	}
}

func (s *AppState) layout(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(s.layoutTopBar),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			switch s.currentTab {
			case TabLog: return s.layoutLogArea(gtx)
			case TabInput: return s.layoutEditorArea(gtx, &s.inputEditor)
			case TabSettings: return s.layoutEditorArea(gtx, &s.settingsEd)
			}
			return layout.Dimensions{}
		}),
		layout.Rigid(s.layoutProgressBar),
	)
}

func (s *AppState) layoutTopBar(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
							layout.Rigid(s.drawTabButton(gtx, &s.TabLogBtn, "Logs", s.currentTab == TabLog)),
							layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
							layout.Rigid(s.drawTabButton(gtx, &s.TabInputBtn, "Edit Input", s.currentTab == TabInput)),
							layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
							layout.Rigid(s.drawTabButton(gtx, &s.TabSetBtn, "Edit Settings", s.currentTab == TabSettings)),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						themeText := "Dark Theme"
						if s.themeMode == ThemeLight { themeText = "Light Theme" }
						return material.Button(s.theme, &s.ThemeBtn, themeText).Layout(gtx)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if s.currentTab == TabLog {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(s.drawButton(gtx, &s.StartBtn, "Start", s.isRunning)),
						layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
						layout.Rigid(s.drawButton(gtx, &s.StopBtn, "Stop", !s.isRunning)),
						layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
						layout.Rigid(s.drawButton(gtx, &s.ClearLogBtn, "Clear Log", s.isRunning)),
					)
				}
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(s.drawButton(gtx, &s.CancelBtn, "Cancel", false)),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(s.drawButton(gtx, &s.SaveBtn, "Save", false)),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(s.drawButton(gtx, &s.CopyBtn, "Copy", false)),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(s.drawButton(gtx, &s.ClearBtn, "Clear", false)),
					layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if s.currentTab == TabSettings { return s.drawButton(gtx, &s.ResetDNSBtn, "Reset DNS to dns.google", false)(gtx) }
						return layout.Dimensions{}
					}),
				)
			}),
		)
	})
}

func (s *AppState) drawTabButton(gtx layout.Context, clk *widget.Clickable, txt string, active bool) func(gtx layout.Context) layout.Dimensions {
	return func(gtx layout.Context) layout.Dimensions {
		btn := material.Button(s.theme, clk, txt)
		if active {
			btn.Background = hex2color("#0078D7")
			btn.Color = hex2color("#FFFFFF")
		} else {
			btn.Background = s.theme.ContrastBg
			btn.Color = s.theme.ContrastFg
		}
		return btn.Layout(gtx)
	}
}

func (s *AppState) drawButton(gtx layout.Context, clk *widget.Clickable, txt string, disabled bool) func(gtx layout.Context) layout.Dimensions {
	return func(gtx layout.Context) layout.Dimensions {
		if disabled { gtx = gtx.Disabled() }
		return material.Button(s.theme, clk, txt).Layout(gtx)
	}
}

func (s *AppState) layoutLogArea(gtx layout.Context) layout.Dimensions {
	bgColor := hex2color("#1E1E1E")
	if s.themeMode == ThemeLight { bgColor = hex2color("#F5F5F5") }
	paint.FillShape(gtx.Ops, bgColor, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		s.mu.Lock()
		count := len(s.logEntries)
		s.mu.Unlock()
		return material.List(s.theme, &s.logList).Layout(gtx, count, func(gtx layout.Context, index int) layout.Dimensions {
			s.mu.Lock()
			textLine := s.logEntries[index]
			s.mu.Unlock()
			return material.Label(s.theme, unit.Sp(14), textLine).Layout(gtx)
		})
	})
}

func (s *AppState) layoutEditorArea(gtx layout.Context, ed *widget.Editor) layout.Dimensions {
	bgColor := hex2color("#252526")
	if s.themeMode == ThemeLight { bgColor = hex2color("#FFFFFF") }
	paint.FillShape(gtx.Ops, bgColor, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		mEd := material.Editor(s.theme, ed, "Type here...")
		mEd.TextSize = unit.Sp(14)
		return mEd.Layout(gtx)
	})
}

func (s *AppState) layoutProgressBar(gtx layout.Context) layout.Dimensions {
	height := 10
	s.mu.Lock()
	p, st := s.progress, s.progressState
	s.mu.Unlock()

	var barColor, bgBarColor color.NRGBA
	switch st {
	case ProgressIdle: barColor, bgBarColor = hex2color("#808080"), hex2color("#808080")
	case ProgressRunning: barColor, bgBarColor = hex2color("#0078D7"), hex2color("#404040")
	case ProgressDone: barColor, bgBarColor = hex2color("#28A745"), hex2color("#28A745")
	}

	return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		width := gtx.Constraints.Max.X
		fillWidth := int(float32(width) * p)
		if st == ProgressIdle || st == ProgressDone { fillWidth = width }

		bgRect := clip.Rect{Max: layout.Dimensions{Size: gtx.Constraints.Max}.Size}
		bgRect.Max.Y = height
		paint.FillShape(gtx.Ops, bgBarColor, bgRect.Op())

		if st == ProgressRunning {
			fgRect := clip.Rect{Max: layout.Dimensions{Size: gtx.Constraints.Max}.Size}
			fgRect.Max.Y = height
			fgRect.Max.X = fillWidth
			paint.FillShape(gtx.Ops, barColor, fgRect.Op())
		}
		return layout.Dimensions{Size: bgRect.Max}
	})
}

type Settings struct {
	Server string
	Port   string
	IPv4   bool
	IPv6   bool
}

func parseSettings() Settings {
	setBytes, _ := os.ReadFile(SettingsFile)
	lines := strings.Split(string(setBytes), "\n")
	settings := Settings{Server: "dns.google", Port: "", IPv4: true, IPv6: false}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" { continue }
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 { continue }
		val := strings.TrimSpace(parts[1])
		switch strings.ToLower(strings.TrimSpace(parts[0])) {
		case "server": settings.Server = val
		case "port": settings.Port = val
		case "ipv4": settings.IPv4 = (val == "true")
		case "ipv6": settings.IPv6 = (val == "true")
		}
	}
	return settings
}

func (s *AppState) startResolving() {
	s.mu.Lock()
	s.isRunning, s.progressState, s.progress = true, ProgressRunning, 0
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFunc = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.isRunning = false
			if s.progressState == ProgressRunning { s.progressState, s.progress = ProgressDone, 1.0 }
			s.mu.Unlock()
			s.window.Invalidate()
		}()

		s.appendLog("Starting to resolve domains...")
		s.appendLog("Reading settings.txt...")
		cfg := parseSettings()
		s.appendLog(fmt.Sprintf("DNS Server: %s", cfg.Server))
		s.appendLog(fmt.Sprintf("IPv4: %t, IPv6: %t", cfg.IPv4, cfg.IPv6))
		
		s.appendLog("Reading input.txt...")
		inputBytes, err := os.ReadFile(InputFile)
		if err != nil {
			s.appendLog("Error reading input.txt: " + err.Error())
			return
		}

		lines := strings.Split(string(inputBytes), "\n")
		var totalDomains int
		for _, line := range lines {
			l := strings.TrimSpace(line)
			if l != "" && !strings.HasPrefix(l, "#") { totalDomains++ }
		}

		s.appendLog(fmt.Sprintf("Found %d domains to resolve", totalDomains))
		s.appendLog("----------------------------------------")

		var outputLines[]string
		resolvedCount := 0

		for _, line := range lines {
			select {
			case <-ctx.Done():
				s.appendLog("Operation cancelled by user")
				return
			default:
			}

			origLine := strings.TrimSuffix(line, "\r")
			l := strings.TrimSpace(origLine)

			if l == "" || strings.HasPrefix(l, "#") {
				outputLines = append(outputLines, origLine)
				if l != "" { s.appendLog(origLine) }
				continue
			}

			s.appendLog("Resolving: " + l)
			ips := resolveDomainDoH(ctx, cfg, l)

			if len(ips) == 0 {
				s.appendLog(fmt.Sprintf("  No records found for %s", l))
				outputLines = append(outputLines, fmt.Sprintf("No records found: %s", l))
			} else {
				for _, ip := range ips {
					s.appendLog(fmt.Sprintf("  %s %s", ip, l))
					outputLines = append(outputLines, fmt.Sprintf("%s %s", ip, l))
				}
			}

			resolvedCount++
			if totalDomains > 0 { s.setProgress(float32(resolvedCount)/float32(totalDomains), ProgressRunning) }
		}

		s.appendLog("----------------------------------------")
		s.appendLog("Writing output.txt...")
		err = os.WriteFile(OutputFile,[]byte(strings.Join(outputLines, "\n")), 0644)
		if err != nil {
			s.appendLog("Failed to write output.txt: " + err.Error())
		} else {
			s.appendLog(fmt.Sprintf("Successfully wrote %d lines to output.txt", len(outputLines)))
		}
	}()
}
func resolveDomainDoH(ctx context.Context, cfg Settings, domain string) []string {
	var ips[]string
	urlStr := "https://" + cfg.Server
	if cfg.Port != "" {
		urlStr += ":" + cfg.Port
	}
	urlStr += "/dns-query"

	client := &http.Client{Timeout: 10 * time.Second}
	fqdn := dns.Fqdn(domain)

	var qTypes
