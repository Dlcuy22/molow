package backend

import (
	"embed"
	"log/slog"
	"os"

	"github.com/dlcuy22/molo/priority"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Application identity. These are the values a packaged build shows in the
// window title, the about box and the dock, so they name the product rather
// than the framework.
const (
	appName        = "MoLow"
	appDescription = "Music player and library manager made with ❤️"
	// appID is the bundle identity and, on Linux, the desktop entry the
	// compositor uses to resolve the window and taskbar icon. The engine
	// library is molo; this app is molow.
	appID = "com.dlcuy22.molow"
)

// Window names, so the service can find a window it did not create and the
// frontend can tell the main window from the effect window.
const (
	mainWindowName   = "main"
	effectWindowName = "effects"
)

// Run builds the application, binds the service and blocks until it exits. The
// embedded frontend is passed in rather than declared here: //go:embed cannot
// reach a parent directory, so the directive has to sit in the root package
// next to frontend/.
func Run(assets embed.FS) {
	// Attach to the launching terminal, if there is one, before the logger is
	// built so slog captures the console's handles. A GUI launch with no parent
	// console leaves stderr pointing nowhere and allocates nothing, so a
	// double-click never pops a console window.
	attachConsole()

	// The settings document is read once here to arm the log sinks from it,
	// so the config lifecycle lines, the priority report and everything a dev
	// console shows land in the file the same way they land on stderr. The
	// service reads the document again when it is built; a small file read
	// twice costs nothing.
	seed := loadAppSettings(slog.New(slog.DiscardHandler), "loaded")

	// A terminal gets the tinted handler so the lines stay readable; a piped
	// or redirected stderr keeps the ANSI codes out of whatever collects it.
	level := resolveLogLevel(seed.General.LogLevel)
	tidy := consoleHandler(level)
	installLogRedirect(tidy)
	registerConsoleLogHandler(tidy, level)
	// The held-back record keeps the repeat count of a stream that ends in
	// repeats, so it is sent on before the process goes away.
	defer func() { _ = tidy.Flush() }()
	if err := applyLog(level, seed.General.LogFileEnabled, seed.General.LogFile); err != nil {
		slog.Warn("log file: could not arm", "error", err)
	}

	// Raise scheduling priority before the engine builds its audio thread, so
	// the thread inherits it. The report names the platform and the scheduling
	// state; off Windows it is a no-op, and a refusal there is logged and
	// playback continues, just without the extra margin.
	rep, err := priority.Raise()
	for _, line := range rep.Lines() {
		slog.Info(line)
	}
	if err != nil {
		slog.Warn("priority", "error", err)
	}

	// Registering the payload types is what gives the frontend a typed event
	// envelope; an unregistered name still travels, but untyped.
	application.RegisterEvent[Snapshot](eventSnapshot)
	application.RegisterEvent[[]float64](eventFrame)
	application.RegisterEvent[[]EffectMetersInfo](eventEffectMeters)
	application.RegisterEvent[LibraryProgress](eventLibrary)
	application.RegisterEvent[string](eventLyrics)

	service := newPlayerService()

	app := application.New(application.Options{
		Name:        appName,
		Description: appDescription,
		// Wails' own lines (build info, platform info, the asset server's
		// requests) go through the app's sinks, so the console and the log
		// file see them like any other record. The redirect keeps them
		// following level moves, and the floor keeps wails' own debug
		// chatter out of a debug run, which is for the app's diagnostics.
		Logger: slog.New(wailsLogHandler{base: slog.Default().Handler()}),
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Linux: application.LinuxOptions{
			// The Wayland app_id is the desktop entry's identity: KDE and
			// GNOME resolve the icon from <application id>.desktop, and GTK4
			// has no per-window icon API. The id is also what the installed
			// entry is named after, so bin/molo and a packaged molo agree.
			ApplicationID: appID,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Server: application.ServerOptions{
			Host: "127.0.0.1",
			Port: 18080,
		},
	})

	// The desktop entry fixes the taskbar icon, but KWin draws the titlebar
	// icon from the toplevel icon the client supplies. Point GTK at the same
	// theme name so both surfaces agree.
	setWindowIconName(appID)

	// The effect window is created on demand from the main window (see
	// PlayerService.OpenEffectWindow). It is not created hidden here: a closed
	// window leaves a stale handle, so the service looks it up by name and
	// rebuilds it if it is gone.

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             mainWindowName,
		Title:            appName,
		Width:            1280,
		Height:           720,
		MinWidth:         820,
		MinHeight:        560,
		BackgroundColour: application.NewRGB(0x13, 0x13, 0x15),
		// Files dropped on the window are the other half of "add tracks": a
		// listener folds them into the queue through the same service call the
		// picker uses.
		EnableFileDrop: true,
		URL:            "/",
	})

	// The drop arrives as a window event, not a bound method, so it is wired
	// here where the window exists. The files are handed to the service rather
	// than loaded here, keeping all queue logic in one place.
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		files := e.Context().DroppedFiles()
		if len(files) == 0 {
			return
		}
		if err := service.LoadPaths(files); err != nil {
			slog.Error("load dropped files", "error", err)
		}
	})

	// Minimise state comes from the OS events for the same reason the drop
	// does: the webview cannot be trusted to report it (a minimised WebView2
	// keeps compositing whatever it is sent), so the spectrum pump learns it
	// from the window and stops producing frames nobody can see.
	window.OnWindowEvent(events.Common.WindowMinimise, func(*application.WindowEvent) {
		service.setMainWindowMinimised(true)
	})
	window.OnWindowEvent(events.Common.WindowUnMinimise, func(*application.WindowEvent) {
		service.setMainWindowMinimised(false)
	})

	if err := app.Run(); err != nil {
		slog.Error("application exited", "error", err)
		// os.Exit skips defers, so the held-back record is sent on here too.
		_ = tidy.Flush()
		os.Exit(1)
	}
}

// openEffectWindow shows the effect window, creating it the first time and
// rebuilding it if it was closed. It is idempotent: a window already open is
// focused rather than duplicated, which is what makes the header's "open in new
// window" safe to click twice.
func (s *PlayerService) openEffectWindow() {
	app := application.Get()
	if app == nil {
		return
	}
	if w, ok := app.Window.GetByName(effectWindowName); ok {
		w.Show()
		w.Focus()

		return
	}

	w := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             effectWindowName,
		Title:            "Effects",
		Width:            880,
		Height:           620,
		MinWidth:         640,
		MinHeight:        420,
		BackgroundColour: application.NewRGB(0x13, 0x13, 0x15),
		URL:              "/?window=effects",
	})
	w.Show()
	w.Focus()
}

// effectWindowPresent reports whether the effects window exists. The fast
// meters are drawn only there, so a desktop session with the window closed
// stops the tick instead of pushing a payload nobody renders. A session with
// no main window is server mode, where a browser can render the meters, so
// the tick keeps feeding; so does a service with no application (tests).
// Only the window's existence is polled: IsVisible would synchronously hop
// to the main thread at the tick's rate.
func effectWindowPresent() bool {
	app := application.Get()
	if app == nil {
		return true
	}
	if _, ok := app.Window.GetByName(mainWindowName); !ok {
		return true
	}
	_, ok := app.Window.GetByName(effectWindowName)

	return ok
}
