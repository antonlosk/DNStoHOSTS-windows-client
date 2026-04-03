package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	StateCancelled
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

	StateMutex   sync.Mutex
	State        AppState
	CancelFunc   context.CancelFunc
	TotalLines   int
	CurrentLine  int

	Window       *app.Window
}

type Config struct {
	Server string
	Port   string
	IPv4   bool
	IPv6   bool
}

func main() {
	if err := ensureFilesExist(); err != nil {
		fmt.Fprintf(os.Stderr, "Initialization error: %v\n", err)
	}

	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("DNStoHOSTS"),
			app.Size(unit.Dp(800), unit.Dp(600)),
		)
		
		if err := drawWindow(w); err != nil {
			fmt.Println("Critical Window Error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func ensureFilesExist() error {
	if _, err := os.Stat("input.txt"); os.IsNotExist(err) {
		content := "# Google\ngoogle.com\n"
		if err := os.WriteFile("input.txt", []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to create input.txt: %w", err)
		}
	}

	if _, err := os.Stat("settings.txt"); os.IsNotExist(err) {
		content := "server=dns.google\nport=443\nipv4=true\nipv6=false\n"
		if err := os.WriteFile("settings.txt", []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to create settings.txt: %w", err)
		}
	}
	return nil
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

	ui.StateMutex.Lock()
	currentState := ui.State
	cancelFunc := ui.CancelFunc
	ui.StateMutex.Unlock()

	// Logic: Start is clickable only when IDLE or DONE
	if ui.BtnStart.Clicked(gtx) && currentState != StateResolving {
		ui.StateMutex.Lock()
		ui.State = StateResolving
		ui.TotalLines = 0
		ui.CurrentLine = 0
		ctx, cancel := context.WithCancel(context.Background())
		ui.CancelFunc = cancel
		ui.StateMutex.Unlock()
		
		go ui.startResolving(ctx)
	}

	// Logic: Stop is clickable only during work
	if ui.BtnStop.Clicked(gtx) && currentState == StateResolving {
		if cancelFunc != nil {
			cancelFunc()
		}
	}

	// Logic: Clear is clickable only when NOT resolving
	if ui.BtnClear.Clicked(gtx) && currentState != StateResolving {
		ui.LogMutex.Lock()
		ui.Logs = []string{}
		ui.LogMutex.Unlock()
		
		ui.StateMutex.Lock()
		ui.State = StateIdle
		ui.TotalLines = 0
		ui.CurrentLine = 0
		ui.StateMutex.Unlock()
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

	ui.StateMutex.Lock()
	currentState := ui.State
	total := ui.TotalLines
	current := ui.CurrentLine
	ui.StateMutex.Unlock()

	if currentState == StateResolving {
		ui.Window.Invalidate()
	}

	paint.Fill(gtx.Ops, ui.Theme.Bg)

	ui.LogMutex.Lock()
	// Intelligent autoscroll: only if user is near the bottom (within 5 lines)
	isNearBottom := ui.LogList.Position.First >= len(ui.Logs)-5
	if ui.NewLogAdded && isNearBottom && len(ui.Logs) > 0 {
		ui.LogList.Position.First = len(ui.Logs) - 1
		ui.LogList.Position.Offset = 0
	}
	ui.NewLogAdded = false
	logsCopy := append([]string(nil), ui.Logs...)
	ui.LogMutex.Unlock()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									// Start button: disabled if resolving
									return ui.drawStateButton(gtx, &ui.BtnStart, "Start", color.NRGBA{R: 0, G: 150, B: 0, A: 255}, currentState != StateResolving)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									// Stop button: disabled if NOT resolving
									return ui.drawStateButton(gtx, &ui.BtnStop, "Stop", color.NRGBA{R: 200, G: 50, B: 50, A: 255}, currentState == StateResolving)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									// Clear button: disabled if resolving
									return ui.drawStateButton(gtx, &ui.BtnClear, "Clear", ui.Theme.Palette.ContrastBg, currentState != StateResolving)
								})
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
			return layout.Inset{Top: unit.Dp(5), Bottom: unit.Dp(10), Left: unit.Dp(10), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						var txt string
						switch currentState {
						case StateResolving:
							txt = fmt.Sprintf("Processing: %d / %d", current, total)
						case StateDone:
							txt = fmt.Sprintf("Done: %d / %d", current, total)
						case StateCancelled:
							txt = fmt.Sprintf("Cancelled: %d / %d", current, total)
						default:
							txt = "Ready"
						}
						lbl := material.Label(ui.Theme, unit.Sp(12), txt)
						return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, lbl.Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return ui.drawProgressBar(gtx, currentState, current, total)
					}),
				)
			})
		}),
	)
}

// drawStateButton renders a button with an explicit visual disabled state
func (ui *UI) drawStateButton(gtx layout.Context, btn *widget.Clickable, label string, baseColor color.NRGBA, enabled bool) layout.Dimensions {
	button := material.Button(ui.Theme, btn, label)
	if !enabled {
		button.Background = color.NRGBA{R: 120, G: 120, B: 120, A: 150}
	} else {
		button.Background = baseColor
	}
	return button.Layout(gtx)
}

func (ui *UI) drawProgressBar(gtx layout.Context, currentState AppState, current, total int) layout.Dimensions {
	height := gtx.Dp(unit.Dp(10))
	width := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, color.NRGBA{R: 200, G: 200, B: 200, A: 255}, clip.Rect{Max: image.Pt(width, height)}.Op())

	var fgColor color.NRGBA
	var progressWidth int

	switch currentState {
	case StateIdle:
		fgColor = color.NRGBA{R: 150, G: 150, B: 150, A: 255}
		progressWidth = 0
	case StateDone:
		fgColor = color.NRGBA{R: 0, G: 180, B: 0, A: 255}
		progressWidth = width
	case StateCancelled:
		fgColor = color.NRGBA{R: 255, G: 165, B: 0, A: 255}
		if total > 0 {
			progressWidth = int(float32(width) * (float32(current) / float32(total)))
		}
	case StateResolving:
		fgColor = color.NRGBA{R: 0, G: 120, B: 215, A: 255}
		if total > 0 {
			progressWidth = int(float32(width) * (float32(current) / float32(total)))
		}
	}
	
	if progressWidth > 0 {
		paint.FillShape(gtx.Ops, fgColor, clip.Rect{Max: image.Pt(progressWidth, height)}.Op())
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

func openFile(fn string) error {
	path, err := filepath.Abs(fn)
	if err != nil { return err }
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func (ui *UI) startResolving(ctx context.Context) {
	ui.addLog("Process started...")
	cfg := loadSettings()
	
	lines, err := readLines("input.txt")
	if err != nil {
		ui.addLog("Error reading input.txt: " + err.Error())
		ui.finish(StateIdle)
		return
	}

	var tasks []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			tasks = append(tasks, l)
		}
	}

	ui.StateMutex.Lock()
	ui.TotalLines = len(tasks)
	ui.CurrentLine = 0
	ui.StateMutex.Unlock()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	port := cfg.Port
	if port == "" { port = "443" }
	dohURL := fmt.Sprintf("https://%s:%s/dns-query", cfg.Server, port)

	var output []string
	for _, line := range tasks {
		select {
		case <-ctx.Done():
			ui.addLog("Cancelled.")
			ui.finish(StateCancelled)
			return
		default:
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			output = append(output, trimmed)
			ui.addLog(trimmed)
			ui.incrementProgress()
			continue
		}

		ui.addLog("Resolving: " + trimmed)
		
		var foundIps []string
		
		handleResolve := func(qtype uint16, typeName string) {
			ips, err := resolveBinaryDoH(ctx, httpClient, dohURL, trimmed, qtype)
			if err != nil {
				ui.addLog(fmt.Sprintf("   [%s] Failed after retries: %v", typeName, err))
			} else if len(ips) > 0 {
				foundIps = append(foundIps, ips...)
				for _, ip := range ips {
					ui.addLog(fmt.Sprintf("   [%s] Found: %s", typeName, ip))
				}
			}
		}

		if cfg.IPv4 { handleResolve(dns.TypeA, "A") }
		if cfg.IPv6 { handleResolve(dns.TypeAAAA, "AAAA") }

		if len(foundIps) == 0 {
			output = append(output, "# Not found: "+trimmed)
		} else {
			for _, ip := range foundIps {
				output = append(output, fmt.Sprintf("%s %s", ip, trimmed))
			}
		}
		ui.incrementProgress()
	}

	if err := writeLines("output.txt", output); err != nil {
		ui.addLog("Error saving output.txt: " + err.Error())
	} else {
		ui.addLog("Results successfully saved to output.txt")
	}
	ui.finish(StateDone)
}

func (ui *UI) incrementProgress() {
	ui.StateMutex.Lock()
	ui.CurrentLine++
	ui.StateMutex.Unlock()
	ui.Window.Invalidate()
}

func (ui *UI) finish(s AppState) { 
	ui.StateMutex.Lock()
	ui.State = s
	ui.CancelFunc = nil
	ui.StateMutex.Unlock()
	ui.Window.Invalidate() 
}

func resolveBinaryDoH(ctx context.Context, client *http.Client, url, domain string, qtype uint16) ([]string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true
	buf, err := m.Pack()
	if err != nil { 
		return nil, fmt.Errorf("failed to pack DNS query: %w", err) 
	}

	const maxRetries = 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
		if err != nil { 
			return nil, fmt.Errorf("failed to create HTTP request: %w", err) 
		}
		req.Header.Set("Content-Type", "application/dns-message")
		req.Header.Set("Accept", "application/dns-message")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxRetries {
				time.Sleep(1 * time.Second)
				continue
			}
			break
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server returned HTTP %d (%s)", resp.StatusCode, string(body))
			// Retry only on server side errors or rate limit
			if (resp.StatusCode >= 500 || resp.StatusCode == 429) && attempt < maxRetries {
				time.Sleep(1 * time.Second)
				continue
			}
			return nil, lastErr
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil { 
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		respMsg := new(dns.Msg)
		if err := respMsg.Unpack(body); err != nil { 
			lastErr = fmt.Errorf("failed to unpack DNS response: %w", err)
			continue
		}

		if respMsg.Rcode != dns.RcodeSuccess {
			return nil, fmt.Errorf("DNS error: %s", dns.RcodeToString[respMsg.Rcode])
		}

		var ips []string
		for _, a := range respMsg.Answer {
			if t, ok := a.(*dns.A); ok && qtype == dns.TypeA { 
				ips = append(ips, t.A.String()) 
			}
			if t, ok := a.(*dns.AAAA); ok && qtype == dns.TypeAAAA { 
				ips = append(ips, t.AAAA.String()) 
			}
		}
		return ips, nil
	}

	return nil, fmt.Errorf("all %d attempts failed: %v", maxRetries, lastErr)
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

func writeLines(p string, l []string) error {
	f, err := os.Create(p)
	if err != nil { return err }
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, line := range l {
		if _, err := w.WriteString(line + "\n"); err != nil { return err }
	}
	return w.Flush()
}
