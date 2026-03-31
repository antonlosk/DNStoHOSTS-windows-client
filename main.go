package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/miekg/dns"
)

type AppState int

const (
	StateIdle AppState = iota
	StateResolving
	StateDone
)

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
	NewLogAdded  bool

	State        AppState
	ProgressAnim float32

	CancelFunc   context.CancelFunc
	Window       *app.Window
}

type Config struct {
	Server string
	Port   string
	IPv4   bool
	IPv6   bool
}

func main() {
	ensureFilesExist()

	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("DNStoHOSTS"),
			app.Size(unit.Dp(800), unit.Dp(600)),
		)
		
		if err := drawWindow(w); err != nil {
			fmt.Println("Critical Error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func ensureFilesExist() {
	if _, err := os.Stat("input.txt"); os.IsNotExist(err) {
		content := "# Google\ngoogle.com\n"
		os.WriteFile("input.txt", []byte(content), 0644)
	}

	if _, err := os.Stat("settings.txt"); os.IsNotExist(err) {
		// Добавлен #port=443
		content := "server=dns.google\n#port=443\nipv4=true\nipv6=false\n"
		os.WriteFile("settings.txt", []byte(content), 0644)
	}
}

func drawWindow(w *app.Window) error {
	th := material.NewTheme()
	ui := &UI{
		Theme:      th,
		IsDarkMode: true,
		Window:     w,
		State:      StateIdle,
	}
	ui.ThemeToggle.Value = true
	ui.LogList.Axis = layout.Vertical

	var ops op.Ops

	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
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

	if ui.BtnStart.Clicked(gtx) && ui.State != StateResolving {
		ui.State = StateResolving
		ctx, cancel := context.WithCancel(context.Background())
		ui.CancelFunc = cancel
		go ui.startResolving(ctx)
	}

	if ui.BtnStop.Clicked(gtx) && ui.State == StateResolving {
		if ui.CancelFunc != nil {
			ui.CancelFunc()
			ui.addLog("Stopping...")
		}
	}

	if ui.BtnClear.Clicked(gtx) {
		ui.LogMutex.Lock()
		ui.Logs = []string{}
		ui.LogMutex.Unlock()
		ui.State = StateIdle
	}

	if ui.BtnInput.Clicked(gtx) { openFile("input.txt") }
	if ui.BtnSettings.Clicked(gtx) { openFile("settings.txt") }
	if ui.BtnOutput.Clicked(gtx) { openFile("output.txt") }
}

func (ui *UI) layout(gtx layout.Context) layout.Dimensions {
	ui.updateTheme()
	if ui.State == StateResolving {
		ui.ProgressAnim += 0.03
		if ui.ProgressAnim > math.Pi*2 { ui.ProgressAnim = 0 }
		ui.Window.Invalidate()
	}

	paint.Fill(gtx.Ops, ui.Theme.Bg)

	ui.LogMutex.Lock()
	if ui.NewLogAdded {
		ui.LogList.Position.First = len(ui.Logs)
		ui.NewLogAdded = false
	}
	logsCopy := append([]string(nil), ui.Logs...)
	ui.LogMutex.Unlock()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
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
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, material.Button(ui.Theme, &ui.BtnClear, "Clear").Layout)
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
							layout.Rigid(material.Switch(ui.Theme, &ui.ThemeToggle, "").Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Left: unit.Dp(4)}.Layout(gtx, material.Label(ui.Theme, unit.Sp(12), "Dark").Layout)
							}),
						)
					}),
				)
			})
		}),

		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			logBg := color.NRGBA{R: 245, G: 245, B: 245, A: 255}
			if ui.IsDarkMode { logBg = color.NRGBA{R: 30, G: 30, B: 30, A: 255} }
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				paint.FillShape(gtx.Ops, logBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return material.List(ui.Theme, &ui.LogList).Layout(gtx, len(logsCopy), func(gtx layout.Context, i int) layout.Dimensions {
						lbl := material.Label(ui.Theme, unit.Sp(14), logsCopy[i])
						if ui.IsDarkMode { lbl.Color = color.NRGBA{R: 220, G: 220, B: 220, A: 255} }
						return layout.Inset{Bottom: unit.Dp(2)}.Layout(gtx, lbl.Layout)
					})
				})
			})
		}),

		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(10), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, ui.drawProgressBar)
		}),
	)
}

func (ui *UI) drawProgressBar(gtx layout.Context) layout.Dimensions {
	height := gtx.Dp(unit.Dp(10))
	width := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, color.NRGBA{R: 200, G: 200, B: 200, A: 255}, clip.Rect{Max: image.Pt(width, height)}.Op())

	var fgColor color.NRGBA
	switch ui.State {
	case StateIdle:
		fgColor = color.NRGBA{R: 150, G: 150, B: 150, A: 255}
		paint.FillShape(gtx.Ops, fgColor, clip.Rect{Max: image.Pt(width, height)}.Op())
	case StateDone:
		fgColor = color.NRGBA{R: 0, G: 180, B: 0, A: 255}
		paint.FillShape(gtx.Ops, fgColor, clip.Rect{Max: image.Pt(width, height)}.Op())
	case StateResolving:
		fgColor = color.NRGBA{R: 0, G: 120, B: 215, A: 255}
		barW := width / 4
		pos := float32(width-barW) * (float32(math.Sin(float64(ui.ProgressAnim))) + 1.0) / 2.0
		paint.FillShape(gtx.Ops, fgColor, clip.Rect{Min: image.Pt(int(pos), 0), Max: image.Pt(int(pos)+barW, height)}.Op())
	}
	return layout.Dimensions{Size: image.Pt(width, height)}
}

func (ui *UI) updateTheme() {
	if ui.IsDarkMode {
		ui.Theme.Bg, ui.Theme.Fg = color.NRGBA{R: 45, G: 45, B: 45, A: 255}, color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	} else {
		ui.Theme.Bg, ui.Theme.Fg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	}
}

func (ui *UI) addLog(msg string) {
	ui.LogMutex.Lock()
	ui.Logs = append(ui.Logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg))
	ui.NewLogAdded = true
	ui.LogMutex.Unlock()
	ui.Window.Invalidate()
}

func openFile(fn string) {
	path, _ := filepath.Abs(fn)
	exec.Command("cmd", "/c", "start", "", path).Start()
}

func (ui *UI) startResolving(ctx context.Context) {
	ui.addLog("Process started...")
	cfg := loadSettings()
	
	lines, err := readLines("input.txt")
	if err != nil {
		ui.addLog("Error: input.txt not found")
		ui.finish(StateIdle)
		return
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	port := cfg.Port
	if port == "" { port = "443" }
	dohURL := fmt.Sprintf("https://%s:%s/dns-query", cfg.Server, port)

	var output []string
	for _, line := range lines {
		select {
		case <-ctx.Done():
			ui.addLog("Cancelled.")
			ui.finish(StateIdle)
			return
		default:
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" { continue }
		if strings.HasPrefix(trimmed, "#") {
			output = append(output, trimmed)
			ui.addLog(trimmed)
			continue
		}

		ui.addLog("Resolving: " + trimmed)
		var found []string
		if cfg.IPv4 { found = append(found, resolveBinaryDoH(ctx, httpClient, dohURL, trimmed, dns.TypeA)...) }
		if cfg.IPv6 { found = append(found, resolveBinaryDoH(ctx, httpClient, dohURL, trimmed, dns.TypeAAAA)...) }

		if len(found) == 0 {
			ui.addLog("   Not found: " + trimmed)
			output = append(output, "# Not found: "+trimmed)
		} else {
			for _, ip := range found {
				ui.addLog("   Found: " + ip)
				// Исправлено: теперь строго один пробел между IP и доменом
				output = append(output, fmt.Sprintf("%s %s", ip, trimmed))
			}
		}
	}

	writeLines("output.txt", output)
	ui.addLog("Results saved to output.txt")
	ui.finish(StateDone)
}

func (ui *UI) finish(s AppState) { 
	ui.State = s
	ui.CancelFunc = nil
	ui.Window.Invalidate() 
}

func resolveBinaryDoH(ctx context.Context, client *http.Client, url, domain string, qtype uint16) []string {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true

	buf, err := m.Pack()
	if err != nil { return nil }

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	if err != nil { return nil }
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := client.Do(req)
	if err != nil { return nil }
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK { return nil }

	body, err := io.ReadAll(resp.Body)
	if err != nil { return nil }

	respMsg := new(dns.Msg)
	if err := respMsg.Unpack(body); err != nil { return nil }

	var ips []string
	for _, a := range respMsg.Answer {
		if t, ok := a.(*dns.A); ok && qtype == dns.TypeA { ips = append(ips, t.A.String()) }
		if t, ok := a.(*dns.AAAA); ok && qtype == dns.TypeAAAA { ips = append(ips, t.AAAA.String()) }
	}
	return ips
}

func loadSettings() Config {
	c := Config{Server: "dns.google", IPv4: true, IPv6: false}
	f, err := os.Open("settings.txt")
	if err != nil { return c }
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		l := strings.TrimSpace(s.Text())
		if l == "" || strings.HasPrefix(l, "#") { continue }
		p := strings.SplitN(l, "=", 2)
		if len(p) < 2 { continue }
		k, v := strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		switch k {
		case "server": c.Server = v
		case "port": c.Port = v
		case "ipv4": c.IPv4 = (v == "true")
		case "ipv6": c.IPv6 = (v == "true")
		}
	}
	return c
}

func readLines(p string) ([]string, error) {
	f, err := os.Open(p); if err != nil { return nil, err }; defer f.Close()
	var l []string
	s := bufio.NewScanner(f)
	for s.Scan() { l = append(l, s.Text()) }
	return l, s.Err()
}

func writeLines(p string, l []string) {
	f, _ := os.Create(p); defer f.Close()
	for _, line := range l { f.WriteString(line + "\n") }
}
