package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
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
)

// AppState represents the current execution state
type AppState int

const (
	StateIdle AppState = iota
	StateResolving
	StateDone
	StateCancelled

	maxLogs = 3000
)

// UI holds all graphical user interface elements and state
type UI struct {
	Theme       *material.Theme
	IsDarkMode  bool
	ThemeToggle widget.Bool

	BtnStart    widget.Clickable
	BtnStop     widget.Clickable
	BtnClear    widget.Clickable
	BtnInput    widget.Clickable
	BtnSettings widget.Clickable
	BtnOutput   widget.Clickable

	LogList    widget.List
	Logs       []string
	LogMutex   sync.Mutex
	AutoScroll bool

	StateMutex  sync.Mutex
	State       AppState
	CancelFunc  context.CancelFunc
	TotalLines  int
	CurrentLine int

	Window *app.Window
}

// runApp initializes and starts the GUI application
func runApp() {
	ensureFilesExist()

	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("DNStoHOSTS"),
			app.Size(unit.Dp(800), unit.Dp(600)),
		)

		th := material.NewTheme()

		// Load settings before creating UI to know the current theme
		cfg := loadSettings()
		isDark := cfg.Theme != "white"

		ui := &UI{
			Theme:      th,
			IsDarkMode: isDark,
			Window:     w,
			State:      StateIdle,
			AutoScroll: true,
		}
		ui.ThemeToggle.Value = isDark
		ui.LogList.Axis = layout.Vertical

		var ops op.Ops

		for {
			switch e := w.Event().(type) {
			case app.DestroyEvent:
				if e.Err != nil {
					fmt.Fprintf(os.Stderr, "Critical Window Error: %v\n", e.Err)
					os.Exit(1)
				}
				os.Exit(0)
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				ui.handleEvents(gtx)
				ui.layout(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}

// handleEvents processes user interactions
func (ui *UI) handleEvents(gtx layout.Context) {
	if ui.ThemeToggle.Update(gtx) {
		ui.IsDarkMode = ui.ThemeToggle.Value
		// Save theme in a separate goroutine to avoid blocking the UI
		go saveThemeSetting(ui.IsDarkMode)
	}

	ui.StateMutex.Lock()
	currentState := ui.State
	cancelFunc := ui.CancelFunc
	ui.StateMutex.Unlock()

	if currentState != StateResolving && ui.BtnStart.Clicked(gtx) {
		ui.StateMutex.Lock()
		ui.State = StateResolving
		ui.TotalLines, ui.CurrentLine = 0, 0
		ctx, cancel := context.WithCancel(context.Background())
		ui.CancelFunc = cancel
		ui.StateMutex.Unlock()
		go ui.startResolving(ctx)
	}

	if currentState == StateResolving && ui.BtnStop.Clicked(gtx) {
		if cancelFunc != nil {
			cancelFunc()
		}
	}

	if currentState != StateResolving && ui.BtnClear.Clicked(gtx) {
		ui.LogMutex.Lock()
		ui.Logs = []string{}
		ui.AutoScroll = true
		ui.LogMutex.Unlock()

		ui.StateMutex.Lock()
		ui.State = StateIdle
		ui.TotalLines, ui.CurrentLine = 0, 0
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

// layout renders the UI components
func (ui *UI) layout(gtx layout.Context) layout.Dimensions {
	ui.updateTheme()

	ui.StateMutex.Lock()
	currentState, total, current := ui.State, ui.TotalLines, ui.CurrentLine
	ui.StateMutex.Unlock()

	if currentState == StateResolving {
		ui.Window.Invalidate()
	}

	paint.Fill(gtx.Ops, ui.Theme.Bg)

	ui.LogMutex.Lock()
	if !ui.LogList.Position.BeforeEnd {
		ui.AutoScroll = true
	} else {
		ui.AutoScroll = false
	}
	ui.LogList.ScrollToEnd = ui.AutoScroll
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
									return ui.drawStateButton(gtx, &ui.BtnStart, "Start", color.NRGBA{R: 0, G: 150, B: 0, A: 255}, currentState != StateResolving)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return ui.drawStateButton(gtx, &ui.BtnStop, "Stop", color.NRGBA{R: 200, G: 50, B: 50, A: 255}, currentState == StateResolving)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
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
			if ui.IsDarkMode {
				logBg = color.NRGBA{R: 30, G: 30, B: 30, A: 255}
			}
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				paint.FillShape(gtx.Ops, logBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
				return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return material.List(ui.Theme, &ui.LogList).Layout(gtx, len(logsCopy), func(gtx layout.Context, i int) layout.Dimensions {
						lbl := material.Label(ui.Theme, unit.Sp(14), logsCopy[i])
						if ui.IsDarkMode {
							lbl.Color = color.NRGBA{R: 220, G: 220, B: 220, A: 255}
						}
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
						return layout.Inset{Bottom: unit.Dp(4)}.Layout(gtx, material.Label(ui.Theme, unit.Sp(12), txt).Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return ui.drawProgressBar(gtx, currentState, current, total)
					}),
				)
			})
		}),
	)
}

// drawStateButton creates buttons with visual state feedback
func (ui *UI) drawStateButton(gtx layout.Context, btn *widget.Clickable, label string, baseColor color.NRGBA, enabled bool) layout.Dimensions {
	if !enabled {
		return layout.Stack{Alignment: layout.Center}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				rr := gtx.Dp(unit.Dp(4))
				stack := clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, rr).Push(gtx.Ops)
				paint.Fill(gtx.Ops, color.NRGBA{R: 120, G: 120, B: 120, A: 120})
				stack.Pop()
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := material.Label(ui.Theme, unit.Sp(14), label)
					l.Color = color.NRGBA{R: 200, G: 200, B: 200, A: 255}
					return l.Layout(gtx)
				})
			}),
		)
	}
	b := material.Button(ui.Theme, btn, label)
	b.Background = baseColor
	return b.Layout(gtx)
}

// drawProgressBar renders the progress indicator
func (ui *UI) drawProgressBar(gtx layout.Context, currentState AppState, current, total int) layout.Dimensions {
	height := gtx.Dp(unit.Dp(10))
	width := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, color.NRGBA{R: 200, G: 200, B: 200, A: 255}, clip.Rect{Max: image.Pt(width, height)}.Op())

	var fgColor color.NRGBA
	var progressWidth int

	switch currentState {
	case StateDone:
		fgColor = color.NRGBA{R: 0, G: 180, B: 0, A: 255}
		progressWidth = width
	case StateCancelled, StateResolving:
		if currentState == StateCancelled {
			fgColor = color.NRGBA{R: 255, G: 165, B: 0, A: 255}
		} else {
			fgColor = color.NRGBA{R: 0, G: 120, B: 215, A: 255}
		}
		if total > 0 {
			progressWidth = int(float32(width) * (float32(current) / float32(total)))
		}
	default:
		progressWidth = 0
	}

	if progressWidth > 0 {
		paint.FillShape(gtx.Ops, fgColor, clip.Rect{Max: image.Pt(progressWidth, height)}.Op())
	}
	return layout.Dimensions{Size: image.Pt(width, height)}
}

// updateTheme adjusts colors based on the selected theme mode
func (ui *UI) updateTheme() {
	if ui.IsDarkMode {
		ui.Theme.Bg, ui.Theme.Fg = color.NRGBA{R: 45, G: 45, B: 45, A: 255}, color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	} else {
		ui.Theme.Bg, ui.Theme.Fg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	}
}

// addLog safely appends messages to the UI list
func (ui *UI) addLog(msg string) {
	ui.LogMutex.Lock()
	ui.Logs = append(ui.Logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg))
	if len(ui.Logs) > maxLogs {
		ui.Logs = ui.Logs[len(ui.Logs)-maxLogs:]
		ui.Logs[0] = fmt.Sprintf("[%s] [SYSTEM] Older logs truncated", time.Now().Format("15:04:05"))
	}
	ui.LogMutex.Unlock()
	ui.Window.Invalidate()
}

// incrementProgress safely bumps the processing counter
func (ui *UI) incrementProgress() {
	ui.StateMutex.Lock()
	ui.CurrentLine++
	ui.StateMutex.Unlock()
	ui.Window.Invalidate()
}

// finish updates the UI state after processing ends
func (ui *UI) finish(s AppState) {
	ui.StateMutex.Lock()
	ui.State = s
	ui.CancelFunc = nil
	ui.StateMutex.Unlock()
	ui.Window.Invalidate()
}