package app

import (
	"fmt"
	"log"
	"runtime"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const EventApplicationClosing = "application:closing"

// QuitGuard asks before quitting while sessions are still running. Cmd+Q and
// closing the only window both terminate the app, so both routes come here.
type QuitGuard struct {
	app       *application.App
	sessions  *Sessions
	confirmed atomic.Bool
	asking    atomic.Bool
	closing   atomic.Bool
	ready     atomic.Bool
	quit      func()
}

func NewQuitGuard(wailsApp *application.App, sessions *Sessions) *QuitGuard {
	return &QuitGuard{app: wailsApp, sessions: sessions, quit: wailsApp.Quit}
}

// ShouldQuit answers macOS's applicationShouldTerminate. It runs on the main
// thread and cannot wait for the user, so a "no" here is followed by app.Quit
// from the dialog's Quit button. Cleanup also runs off the main thread before
// retrying Quit, so AppKit remains responsive until termination is ready.
func (g *QuitGuard) ShouldQuit() bool {
	if g.ready.Load() {
		return true
	}
	if g.closing.Load() {
		return false
	}
	running := g.sessions.count()
	if g.confirmed.Load() || running == 0 {
		g.prepareQuit()
		return false
	}
	if g.asking.CompareAndSwap(false, true) {
		go g.ask(running)
	}
	return false
}

func (g *QuitGuard) prepareQuit() {
	if !g.closing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if g.app != nil {
			g.app.Event.Emit(EventApplicationClosing)
		}
		if err := g.sessions.shutdown(); err != nil {
			log.Printf("Close sessions before quitting: %v", err)
		}
		g.ready.Store(true)
		g.quit()
	}()
}

// confirmForRestart asks before the updater launches its helper. The helper
// waits for this process to exit, so the prompt must finish first.
func (g *QuitGuard) confirmForRestart(decided func(bool)) {
	if decided == nil {
		return
	}
	running := g.sessions.count()
	if running == 0 {
		decided(true)
		return
	}
	if !g.asking.CompareAndSwap(false, true) {
		decided(false)
		return
	}

	message := fmt.Sprintf("%d sessions are still running. Restarting closes them.", running)
	if running == 1 {
		message = "1 session is still running. Restarting closes it."
	}
	dialog := g.app.Dialog.Question().
		SetTitle("Restart SSHBrowse?").
		SetMessage(message).
		AttachToWindow(g.app.Window.Current())
	dialog.AddButton("No").SetAsCancel().SetAsDefault().OnClick(func() {
		g.asking.Store(false)
		decided(false)
	})
	dialog.AddButton("Yes").OnClick(func() {
		g.asking.Store(false)
		decided(true)
	})
	dialog.Show()
}

// authorizeRestartQuit is called only after the updater helper is ready to
// wait for this process to exit.
func (g *QuitGuard) authorizeRestartQuit() {
	g.confirmed.Store(true)
}

// GuardWindow vetoes a user-initiated close of the window under the same rule.
// Hooks run before Wails's own close listener, so cancelling here keeps the window.
func (g *QuitGuard) GuardWindow(window *application.WebviewWindow) {
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if !g.ShouldQuit() {
			event.Cancel()
		}
	})
}

func (g *QuitGuard) ask(running int) {
	message := fmt.Sprintf("%d sessions are still running. Quitting closes them.", running)
	if running == 1 {
		message = "1 session is still running. Quitting closes it."
	}
	dialog := g.app.Dialog.Question().
		SetTitle("Quit SSHBrowse?").
		SetMessage(message).
		AttachToWindow(g.app.Window.Current())
	no := dialog.AddButton("No").SetAsCancel().OnClick(func() { g.asking.Store(false) })
	yes := dialog.AddButton("Yes").SetAsDefault().OnClick(func() {
		g.confirmed.Store(true)
		g.app.Quit()
	})
	if runtime.GOOS == "darwin" {
		// Wails reverses the configured button order on macOS.
		dialog.AddButtons([]*application.Button{yes, no})
	}
	dialog.Show()
}
