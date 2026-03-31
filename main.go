package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
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

// UI and Application State
type Application struct {
	theme *material.Theme

	// Buttons for Editor
	btnCancel widget.Clickable
	btnSave   widget.Clickable
	btnCopy   widget.Clickable
	btnClear  widget.Clickable
	btnReset  widget.Clickable

	// Buttons for Control
	btnStart    widget.Clickable
	btnStop     widget.Clickable
	btnClearLog widget.Clickable

	// Editors
	editorSettings widget.Editor
	editorLog      widget.Editor

	// Scrollbars
	scrollLog material.ScrollbarStyle

	// State
	mu           sync.Mutex
	logText      string
	progress     float32
	progressMode int // 0: Gray (Idle), 1: Blue (Resolving), 2: Green (Done)
	cancelFunc   context.CancelFunc
	isRunning    bool

	window *app.Window
}

const (
	defaultSettings = "server=dns.google\nport=8443\nipv4=true\nipv6=false"
	defaultInput    = "Google\ngoogle.com"
	settingsFile    = "settings.txt"
	inputFile       = "input.txt"
	outputFile      = "output.txt"
)

func main() {
	ensureFilesExist()

	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("DNStoHOSTS"),
			app.Size(unit.Dp(800), unit.Dp(600)),
		)
		err := runLoop(w)
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func ensureFilesExist() {
	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		os.WriteFile(inputFile, []byte(defaultInput), 0644)
	}
	if _, err := os.Stat(settingsFile); os.IsNotExist(err) {
		os.WriteFile(settingsFile, []byte(defaultSettings), 0644)
	}
}

func runLoop(w *app.Window) error {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	// Windows 10 flat style tweaks
	th.FingerSize = unit.Dp(32)

	application := &Application{
		theme:          th,
		editorSettings: widget.Editor{SingleLine: false, Submit: false},
		editorLog:      widget.Editor{SingleLine: false, ReadOnly: true},
		window:         w,
	}

	// Load initial settings
	settingsBytes, _ := os.ReadFile(settingsFile)
	application.editorSettings.SetText(string(settingsBytes))

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			application.handleEvents(gtx)
			application.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (a *Application) handleEvents(gtx layout.Context) {
	// Editor actions
	if a.btnCancel.Clicked(gtx) {
		b, _ := os.ReadFile(settingsFile)
		a.editorSettings.SetText(string(b))
	}
	if a.btnSave.Clicked(gtx) {
		os.WriteFile(settingsFile, []byte(a.editorSettings.Text()), 0644)
	}
	if a.btnCopy.Clicked(gtx) {
		// Native Windows clipboard copy to avoid Gio API version issues
		cmd := exec.Command("clip")
		in, err := cmd.StdinPipe()
		if err == nil {
			go func() {
				defer in.Close()
				io.WriteString(in, a.editorSettings.Text())
				cmd.Run()
			}()
		}
	}
	if a.btnClear.Clicked(gtx) {
		a.editorSettings.SetText("")
	}
	if a.btnReset.Clicked(gtx) {
		a.editorSettings.SetText(defaultSettings)
	}

	// Control actions
	if a.btnStart.Clicked(gtx) {
		a.mu.Lock()
		if !a.isRunning {
			a.isRunning = true
			a.progressMode = 1
			a.progress = 0
			ctx, cancel := context.WithCancel(context.Background())
			a.cancelFunc = cancel
			go a.runResolver(ctx)
		}
		a.mu.Unlock()
	}
	if a.btnStop.Clicked(gtx) {
		a.mu.Lock()
		if a.isRunning && a.cancelFunc != nil {
			a.addLog("Stop requested, waiting for current operation to complete...")
			a.cancelFunc()
		}
		a.mu.Unlock()
	}
	if a.btnClearLog.Clicked(gtx) {
		a.mu.Lock()
		a.logText = ""
		a.editorLog.SetText("")
		if !a.isRunning {
			a.progressMode = 0
			a.progress = 0
		}
		a.mu.Unlock()
	}
}

func (a *Application) addLog(msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	timestamp := time.Now().Format("15:04:05")
	line := fmt.Sprintf("[%s] %s\n", timestamp, msg)
	a.logText += line
	a.editorLog.SetText(a.logText)
	a.window.Invalidate()
}

func (a *Application) setProgress(val float32, mode int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progress = val
	a.progressMode = mode
	a.window.Invalidate()
}

func (a *Application) layout(gtx layout.Context) layout.Dimensions {
	// Background
	paint.Fill(gtx.Ops, color.NRGBA{R: 240, G: 240, B: 240, A: 255})

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, a.layoutEditorControls)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, a.layoutSettingsEditor)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, a.layoutLogControls)
		}),
		layout.Flexed(2, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, a.layoutLogViewer)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(8)).Layout(gtx, a.layoutProgressBar)
		}),
	)
}

func (a *Application) layoutEditorControls(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceStart}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnCancel, "Cancel")
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, btn.Layout)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnSave, "Save")
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, btn.Layout)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnCopy, "Copy")
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, btn.Layout)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnClear, "Clear")
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, btn.Layout)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnReset, "Reset DNS to dns.google")
			return btn.Layout(gtx)
		}),
	)
}

func (a *Application) layoutSettingsEditor(gtx layout.Context) layout.Dimensions {
	// Draw background for editor
	rect := clip.Rect{Max: gtx.Constraints.Max}.Op()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, rect)

	ed := material.Editor(a.theme, &a.editorSettings, "Edit settings.txt here...")
	return layout.UniformInset(unit.Dp(4)).Layout(gtx, ed.Layout)
}

func (a *Application) layoutLogControls(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceStart}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnStart, "Start")
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, btn.Layout)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnStop, "Stop")
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, btn.Layout)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(a.theme, &a.btnClearLog, "Clear Log")
			return btn.Layout(gtx)
		}),
	)
}

func (a *Application) layoutLogViewer(gtx layout.Context) layout.Dimensions {
	// Slightly different background for log
	rect := clip.Rect{Max: gtx.Constraints.Max}.Op()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 30, G: 30, B: 30, A: 255}, rect)

	ed := material.Editor(a.theme, &a.editorLog, "")
	ed.Color = color.NRGBA{R: 220, G: 220, B: 220, A: 255}

	return layout.UniformInset(unit.Dp(4)).Layout(gtx, ed.Layout)
}

func (a *Application) layoutProgressBar(gtx layout.Context) layout.Dimensions {
	height := gtx.Dp(unit.Dp(10))
	width := gtx.Constraints.Max.X

	var c color.NRGBA
	switch a.progressMode {
	case 1: // Resolving - Blue
		c = color.NRGBA{R: 0, G: 120, B: 215, A: 255} // Win10 Blue
	case 2: // Done - Green
		c = color.NRGBA{R: 34, G: 177, B: 76, A: 255}
	default: // Idle - Gray
		c = color.NRGBA{R: 180, G: 180, B: 180, A: 255}
	}

	// Draw Background
	bgRect := clip.Rect{Max: image.Pt(width, height)}.Op()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 200, G: 200, B: 200, A: 255}, bgRect)

	// Draw Foreground
	a.mu.Lock()
	progressWidth := int(float32(width) * a.progress)
	if a.progressMode != 1 && a.progress == 0 {
		progressWidth = width // Fill completely for Idle/Done if progress is 0 but we want full color
	}
	a.mu.Unlock()

	fgRect := clip.Rect{Max: image.Pt(progressWidth, height)}.Op()
	paint.FillShape(gtx.Ops, c, fgRect)

	return layout.Dimensions{Size: image.Pt(width, height)}
}

// Resolver Logic
func (a *Application) runResolver(ctx context.Context) {
	defer func() {
		a.mu.Lock()
		a.isRunning = false
		a.mu.Unlock()
	}()

	a.addLog("Starting to resolve domains...")
	a.addLog("Reading settings.txt...")

	// Parse settings
	settingsMap := make(map[string]string)
	settingsContent := a.editorSettings.Text()
	lines := strings.Split(settingsContent, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			settingsMap[strings.ToLower(parts[0])] = parts[1]
		}
	}

	server := settingsMap["server"]
	if server == "" {
		server = "dns.google"
	}
	port := settingsMap["port"] // Optional
	ipv4 := settingsMap["ipv4"] == "true"
	ipv6 := settingsMap["ipv6"] == "true"

	if !ipv4 && !ipv6 {
		ipv4 = true // Fallback
	}

	a.addLog(fmt.Sprintf("DNS Server: %s", server))
	a.addLog(fmt.Sprintf("IPv4: %t, IPv6: %t", ipv4, ipv6))

	a.addLog("Reading input.txt...")
	inputBytes, err := os.ReadFile(inputFile)
	if err != nil {
		a.addLog("Error reading input.txt: " + err.Error())
		a.setProgress(1.0, 0)
		return
	}

	inputLines := strings.Split(string(inputBytes), "\n")
	var domains []string
	for _, l := range inputLines {
		l = strings.TrimSpace(l)
		if l != "" {
			domains = append(domains, l)
		}
	}

	domainCount := 0
	for _, d := range domains {
		if strings.Contains(d, ".") {
			domainCount++
		}
	}

	a.addLog(fmt.Sprintf("Found %d domains to resolve", domainCount))
	a.addLog("----------------------------------------")

	var outputBuffer bytes.Buffer
	resolvedCount := 0
	domainIndex := 0

	for _, line := range domains {
		select {
		case <-ctx.Done():
			a.addLog("Operation cancelled by user")
			a.setProgress(1.0, 0) // Back to gray
			return
		default:
		}

		if !strings.Contains(line, ".") {
			// Category / Header
			a.addLog(fmt.Sprintf("# %s", line))
			outputBuffer.WriteString(line + "\n")
			continue
		}

		// It's a domain
		domainIndex++
		a.addLog(fmt.Sprintf("Resolving: %s", line))

		// Calculate progress
		if domainCount > 0 {
			a.setProgress(float32(domainIndex)/float32(domainCount), 1)
		}

		var ips []string

		if ipv4 {
			res, err := resolveDoH(server, port, line, dns.TypeA)
			if err == nil {
				ips = append(ips, res...)
			}
		}
		if ipv6 {
			res, err := resolveDoH(server, port, line, dns.TypeAAAA)
			if err == nil {
				ips = append(ips, res...)
			}
		}

		if len(ips) == 0 {
			a.addLog(fmt.Sprintf("  No records found for %s", line))
			outputBuffer.WriteString(fmt.Sprintf("No records found: %s\n", line))
		} else {
			for _, ip := range ips {
				a.addLog(fmt.Sprintf("  %s %s", ip, line))
				outputBuffer.WriteString(fmt.Sprintf("%s %s\n", ip, line))
				resolvedCount++
			}
		}
	}

	a.addLog("----------------------------------------")
	a.addLog("Writing output.txt...")
	err = os.WriteFile(outputFile, outputBuffer.Bytes(), 0644)
	if err != nil {
		a.addLog("Error writing output: " + err.Error())
	} else {
		a.addLog(fmt.Sprintf("Successfully wrote %d lines to output.txt", resolvedCount))
	}

	a.setProgress(1.0, 2) // Green when done
}

func resolveDoH(server, port, domain string, qtype uint16) ([]string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), qtype)
	m.RecursionDesired = true

	wire, err := m.Pack()
	if err != nil {
		return nil, err
	}

	url := "https://" + server
	if port != "" {
		url += ":" + port
	}
	url += "/dns-query"

	req, err := http.NewRequest("POST", url, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	rMsg := new(dns.Msg)
	err = rMsg.Unpack(body)
	if err != nil {
		return nil, err
	}

	var ips []string
	for _, ans := range rMsg.Answer {
		switch record := ans.(type) {
		case *dns.A:
			ips = append(ips, record.A.String())
		case *dns.AAAA:
			ips = append(ips, record.AAAA.String())
		}
	}

	return ips, nil
}
