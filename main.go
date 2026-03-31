package main

import (
	"bufio"
	"context"
	"fmt"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/io/system"
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

// AppState represents the current execution state
type AppState int

const (
	StateIdle AppState = iota
	StateResolving
	StateDone
)

// UI holds all the UI state and widgets
type UI struct {
	Theme        *material.Theme
	IsDarkMode   bool
	ThemeToggle  widget.Bool

	BtnStart     widget.Clickable
	BtnStop      widget.Clickable
	BtnClear     widget.Clickable
	BtnInput     widget.Clickable
	BtnSettings  widget.Clickable
	BtnOutput    widget.Clickable

	LogList      widget.List
	Logs         []string
	LogMutex     sync.Mutex

	State        AppState
	ProgressAnim float32

	CancelFunc   context.CancelFunc
	Window       *app.Window
}

// Config represents the settings.txt configuration
type Config struct {
	Server string
	Port   string
	IPv4   bool
	IPv6   bool
}

func main() {
	ensureFilesExist()

	go func() {
		w := app.NewWindow(
			app.Title("DNStoHOSTS"),
			app.Size(unit.Dp(800), unit.Dp(600)),
		)
		if err := drawWindow(w); err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func ensureFilesExist() {
	// input.txt
	if _, err := os.Stat("input.txt"); os.IsNotExist(err) {
		content := "# Google\ngoogle.com\n"
		os.WriteFile("input.txt", []byte(content), 0644)
	}

	// settings.txt
	if _, err := os.Stat("settings.txt"); os.IsNotExist(err) {
		content := "server=dns.google\n#port=443\nipv4=true\nipv6=false\n"
		os.WriteFile("settings.txt", []byte(content), 0644)
	}
}

func drawWindow(w *app.Window) error {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

	ui := &UI{
		Theme:      th,
		IsDarkMode: true,
		Window:     w,
		State:      StateIdle,
	}
	ui.ThemeToggle.Value = true // Default to dark mode

	ui.LogList.Axis = layout.Vertical
	ui.LogList.Scrollbar.Style.Width = unit.Dp(8)

	var ops op.Ops

	for {
		e := w.NextEvent()
		switch e := e.(type) {
		case system.DestroyEvent:
			return e.Err
		case system.FrameEvent:
			gtx := layout.NewContext(&ops, e)
			ui.handleEvents(gtx)
			ui.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (ui *UI) handleEvents(gtx layout.Context) {
	if ui.ThemeToggle.Update(gtx) {
		ui.IsDarkMode = ui.ThemeToggle.Value
	}

	if ui.BtnStart.Clicked(gtx) {
		if ui.State != StateResolving {
			ui.State = StateResolving
			ctx, cancel := context.WithCancel(context.Background())
			ui.CancelFunc = cancel
			go ui.startResolving(ctx)
		}
	}

	if ui.BtnStop.Clicked(gtx) {
		if ui.State == StateResolving && ui.CancelFunc != nil {
			ui.addLog("Stop requested, waiting for current operation to complete...")
			ui.CancelFunc()
			ui.CancelFunc = nil
		}
	}

	if ui.BtnClear.Clicked(gtx) {
		ui.LogMutex.Lock()
		ui.Logs = []string{}
		ui.LogMutex.Unlock()
		ui.State = StateIdle
		ui.Window.Invalidate()
	}

	if ui.BtnInput.Clicked(gtx) {
		openFile("input.txt")
	}
	if ui.BtnSettings.Clicked(gtx) {
		openFile("settings.txt")
	}
	if ui.BtnOutput.Clicked(gtx) {
		openFile("output.txt")
	}
}

func (ui *UI) layout(gtx layout.Context) layout.Dimensions {
	ui.updateTheme()

	// Update animation for indeterminate progress bar
	if ui.State == StateResolving {
		ui.ProgressAnim += 0.03
		if ui.ProgressAnim > math.Pi*2 {
			ui.ProgressAnim = 0
		}
		op.InvalidateOp{}.Add(gtx.Ops)
	}

	// Main background
	paint.Fill(gtx.Ops, ui.Theme.Bg)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Top controls
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								btn := material.Button(ui.Theme, &ui.BtnStart, "Start")
								if ui.State == StateResolving {
									btn.Background = color.NRGBA{R: 100, G: 100, B: 100, A: 255}
								} else {
									btn.Background = color.NRGBA{R: 0, G: 150, B: 0, A: 255}
								}
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, btn.Layout)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								btn := material.Button(ui.Theme, &ui.BtnStop, "Stop")
								btn.Background = color.NRGBA{R: 200, G: 50, B: 50, A: 255}
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, btn.Layout)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, material.Button(ui.Theme, &ui.BtnClear, "Clear Log").Layout)
							}),
						)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, material.Button(ui.Theme, &ui.BtnInput, "input.txt").Layout)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, material.Button(ui.Theme, &ui.BtnSettings, "settings.txt").Layout)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, material.Button(ui.Theme, &ui.BtnOutput, "output.txt").Layout)
							}),
							layout.Rigid(material.Switch(ui.Theme, &ui.ThemeToggle, "Dark Mode").Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(ui.Theme, unit.Sp(12), " Dark Theme")
								return layout.Inset{Left: unit.Dp(4)}.Layout(gtx, lbl.Layout)
							}),
						)
					}),
				)
			})
		}),

		// Log area
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			logBg := color.NRGBA{R: 245, G: 245, B: 245, A: 255}
			if ui.IsDarkMode {
				logBg = color.NRGBA{R: 30, G: 30, B: 30, A: 255}
			}
			
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				// Draw log background
				rect := clip.Rect{Max: gtx.Constraints.Max}.Op()
				paint.FillShape(gtx.Ops, logBg, rect)

				ui.LogMutex.Lock()
				logs := make([]string, len(ui.Logs))
				copy(logs, ui.Logs)
				ui.LogMutex.Unlock()

				return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return material.List(ui.Theme, &ui.LogList).Layout(gtx, len(logs), func(gtx layout.Context, index int) layout.Dimensions {
						lbl := material.Label(ui.Theme, unit.Sp(14), logs[index])
						if ui.IsDarkMode {
							lbl.Color = color.NRGBA{R: 200, G: 200, B: 200, A: 255}
						} else {
							lbl.Color = color.NRGBA{R: 40, G: 40, B: 40, A: 255}
						}
						// Use monospace font-like appearance for logs if possible, but default is fine
						return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, lbl.Layout)
					})
				})
			})
		}),

		// Bottom Progress Bar
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ui.drawProgressBar(gtx)
			})
		}),
	)
}

func (ui *UI) drawProgressBar(gtx layout.Context) layout.Dimensions {
	height := gtx.Dp(unit.Dp(12))
	width := gtx.Constraints.Max.X

	// Background of progress bar
	bgRect := clip.Rect{Max: gtx.Constraints.Max}
	bgRect.Max.Y = height
	paint.FillShape(gtx.Ops, color.NRGBA{R: 200, G: 200, B: 200, A: 255}, bgRect.Op())

	var fgColor color.NRGBA
	switch ui.State {
	case StateIdle:
		fgColor = color.NRGBA{R: 128, G: 128, B: 128, A: 255} // Grey
		fgRect := bgRect
		paint.FillShape(gtx.Ops, fgColor, fgRect.Op())
	case StateDone:
		fgColor = color.NRGBA{R: 0, G: 200, B: 0, A: 255} // Green
		fgRect := bgRect
		paint.FillShape(gtx.Ops, fgColor, fgRect.Op())
	case StateResolving:
		fgColor = color.NRGBA{R: 0, G: 120, B: 215, A: 255} // Blue (Windows style)
		
		// Calculate indeterminate position
		barWidth := width / 4
		pos := float32(width-barWidth) * (float32(math.Sin(float64(ui.ProgressAnim))) + 1.0) / 2.0
		
		fgRect := clip.Rect{
			Min: goImagePoint(int(pos), 0),
			Max: goImagePoint(int(pos)+barWidth, height),
		}
		paint.FillShape(gtx.Ops, fgColor, fgRect.Op())
	}

	return layout.Dimensions{Size: goImagePoint(width, height)}
}

// goImagePoint replaces image.Point to avoid importing image package just for this
func goImagePoint(x, y int) struct{ X, Y int } {
	return struct{ X, Y int }{X: x, Y: y}
}

func (ui *UI) updateTheme() {
	if ui.IsDarkMode {
		ui.Theme.Bg = color.NRGBA{R: 40, G: 40, B: 40, A: 255}
		ui.Theme.Fg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	} else {
		ui.Theme.Bg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
		ui.Theme.Fg = color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	}
}

func (ui *UI) addLog(msg string) {
	ui.LogMutex.Lock()
	defer ui.LogMutex.Unlock()
	timestamp := time.Now().Format("15:04:05")
	ui.Logs = append(ui.Logs, fmt.Sprintf("[%s] %s", timestamp, msg))
	
	// Auto-scroll logic (rudimentary)
	ui.LogList.Position.First = len(ui.Logs)
	ui.Window.Invalidate()
}

func openFile(filename string) {
	absPath, err := filepath.Abs(filename)
	if err != nil {
		return
	}
	// Use standard Windows start command to open file in default editor
	exec.Command("cmd", "/c", "start", absPath).Start()
}

// --- Logic ---

func (ui *UI) startResolving(ctx context.Context) {
	ui.addLog("Starting to resolve domains...")
	
	// Load settings
	ui.addLog("Reading settings.txt...")
	cfg := loadSettings()
	ui.addLog(fmt.Sprintf("DNS Server: %s", cfg.Server))
	ui.addLog(fmt.Sprintf("IPv4: %v, IPv6: %v", cfg.IPv4, cfg.IPv6))

	// Load input
	ui.addLog("Reading input.txt...")
	lines, err := readLines("input.txt")
	if err != nil {
		ui.addLog(fmt.Sprintf("Error reading input.txt: %v", err))
		ui.finish(StateIdle)
		return
	}

	var toResolve []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			toResolve = append(toResolve, line)
		}
	}
	ui.addLog(fmt.Sprintf("Found %d domains to resolve", len(toResolve)))
	ui.addLog("----------------------------------------")

	outputLines := make([]string, 0)
	client := &dns.Client{Net: "https", Timeout: 5 * time.Second}
	
	// Construct DNS URL
	portPart := ""
	if cfg.Port != "" {
		portPart = ":" + cfg.Port
	}
	dohURL := fmt.Sprintf("https://%s%s/dns-query", cfg.Server, portPart)

	for _, line := range lines {
		// Check for cancellation
		select {
		case <-ctx.Done():
			ui.addLog("Operation cancelled by user")
			ui.finish(StateIdle)
			return
		default:
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#") {
			outputLines = append(outputLines, line)
			ui.addLog(line)
			continue
		}

		domain := line
		ui.addLog(fmt.Sprintf("Resolving: %s", domain))

		var ips []string

		if cfg.IPv4 {
			res := resolveType(client, dohURL, domain, dns.TypeA)
			ips = append(ips, res...)
		}
		if cfg.IPv6 {
			res := resolveType(client, dohURL, domain, dns.TypeAAAA)
			ips = append(ips, res...)
		}

		if len(ips) == 0 {
			ui.addLog(fmt.Sprintf("   No records found for %s", domain))
			outputLines = append(outputLines, fmt.Sprintf("No records found: %s", domain))
		} else {
			for _, ip := range ips {
				ui.addLog(fmt.Sprintf("   %s %s", ip, domain))
				outputLines = append(outputLines, fmt.Sprintf("%s %s", ip, domain))
			}
		}
	}

	ui.addLog("----------------------------------------")
	ui.addLog("Writing output.txt...")
	
	err = writeLines("output.txt", outputLines)
	if err != nil {
		ui.addLog(fmt.Sprintf("Error writing output.txt: %v", err))
	} else {
		ui.addLog(fmt.Sprintf("Successfully wrote %d lines to output.txt", len(outputLines)))
	}

	ui.finish(StateDone)
}

func (ui *UI) finish(state AppState) {
	ui.State = state
	ui.CancelFunc = nil
	ui.Window.Invalidate()
}

func resolveType(client *dns.Client, url, domain string, qtype uint16) []string {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(domain), qtype)
	msg.RecursionDesired = true

	resp, _, err := client.Exchange(msg, url)
	if err != nil || resp == nil {
		return nil
	}

	var ips []string
	for _, ans := range resp.Answer {
		switch record := ans.(type) {
		case *dns.A:
			ips = append(ips, record.A.String())
		case *dns.AAAA:
			ips = append(ips, record.AAAA.String())
		}
	}
	return ips
}

func loadSettings() Config {
	cfg := Config{
		Server: "dns.google",
		IPv4:   true,
		IPv6:   false,
	}

	file, err := os.Open("settings.txt")
	if err != nil {
		return cfg
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		
		key, val := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "server":
			cfg.Server = val
		case "port":
			cfg.Port = val
		case "ipv4":
			cfg.IPv4 = (val == "true")
		case "ipv6":
			cfg.IPv6 = (val == "true")
		}
	}
	return cfg
}

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func writeLines(path string, lines []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for _, line := range lines {
		_, err := writer.WriteString(line + "\n")
		if err != nil {
			return err
		}
	}
	return writer.Flush()
}
