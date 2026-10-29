//go:build linux

package loginwin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Library file names. The versioned soname is tried first because that is what
// is actually on disk; the bare name is the fallback for distributions that only
// ship the development symlink.
var (
	libGTKNames    = []string{"libgtk-4.so.1", "libgtk-4.so"}
	libWebKitNames = []string{"libwebkitgtk-6.0.so.4", "libwebkitgtk-6.0.so"}
	libSoupNames   = []string{"libsoup-3.0.so.0", "libsoup-3.0.so"}
	libGObjectName = []string{"libgobject-2.0.so.0", "libgobject-2.0.so"}
	libGLibName    = []string{"libglib-2.0.so.0", "libglib-2.0.so"}
)

// gtypeObject is G_TYPE_OBJECT, the fundamental type used to box a pointer in a
// GValue. It is G_TYPE_MAKE_FUNDAMENTAL(20), which is 20 << 2.
const gtypeObject = 20 << 2

// persistentStorageSQLite is WEBKIT_COOKIE_PERSISTENT_STORAGE_SQLITE.
const persistentStorageSQLite = 1

// fn holds every C function the window uses. Registering them into typed fields
// is what makes the ABI checkable by the compiler rather than by a comment.
var fn struct {
	gtkInit                 func()
	gtkWindowNew            func() uintptr
	gtkWindowSetTitle       func(uintptr, *byte)
	gtkWindowSetDefaultSize func(uintptr, int32, int32)
	gtkWindowSetChild       func(uintptr, uintptr)
	gtkWindowPresent        func(uintptr)
	gtkWindowDestroy        func(uintptr)

	webkitWebViewGetType                 func() uintptr
	webkitWebViewNew                     func() uintptr
	webkitWebViewLoadURI                 func(uintptr, *byte)
	webkitWebViewGetURI                  func(uintptr) *byte
	webkitNetworkSessionNew              func(*byte, *byte) uintptr
	webkitNetworkSessionGetCookieManager func(uintptr) uintptr
	webkitCookieManagerGetAllCookies     func(uintptr, uintptr, uintptr, uintptr)
	webkitCookieManagerGetAllCookiesFin  func(uintptr, uintptr, *uintptr) uintptr
	webkitCookieManagerReplaceCookies    func(uintptr, uintptr, uintptr, uintptr, uintptr)
	webkitCookieManagerSetPersistStorage func(uintptr, *byte, int32)

	soupCookieNew         func(*byte, *byte, *byte, *byte, int32) uintptr
	soupCookieGetName     func(uintptr) *byte
	soupCookieGetValue    func(uintptr) *byte
	soupCookieGetDomain   func(uintptr) *byte
	soupCookieGetPath     func(uintptr) *byte
	soupCookieGetSecure   func(uintptr) int32
	soupCookieGetHTTPOnly func(uintptr) int32
	soupCookieGetExpires  func(uintptr) uintptr
	soupCookieGetSameSite func(uintptr) int32
	soupCookieSetSecure   func(uintptr, int32)
	soupCookieSetHTTPOnly func(uintptr, int32)
	soupCookieSetSameSite func(uintptr, int32)
	soupCookieFree        func(uintptr)

	gObjectNewWithProperties func(uintptr, uint32, **byte, *gvalue) uintptr
	gObjectUnref             func(uintptr)
	gTypeFromName            func(*byte) uintptr
	gValueInit               func(*gvalue, uintptr) uintptr
	gValueSetObject          func(*gvalue, uintptr)
	gValueUnset              func(*gvalue)

	gIdleAdd        func(uintptr, uintptr) uint32
	gErrorFree      func(uintptr)
	gListLength     func(uintptr) uint32
	gListNthData    func(uintptr, uint32) uintptr
	gListAppend     func(uintptr, uintptr) uintptr
	gListFree       func(uintptr)
	gDateTimeToUnix func(uintptr) int64
}

// gvalue mirrors GValue: a GType followed by the 16-byte data union.
type gvalue struct {
	gType uintptr
	data  [2]uintptr
}

var (
	loadOnce sync.Once
	loadErr  error
)

// requiredSymbols are the names that must resolve before the window can open.
var requiredSymbols = map[string][]string{
	"gtk": {
		"gtk_init", "gtk_window_new", "gtk_window_set_title",
		"gtk_window_set_default_size", "gtk_window_set_child",
		"gtk_window_present", "gtk_window_destroy",
	},
	"webkit": {
		"webkit_web_view_get_type", "webkit_web_view_new",
		"webkit_web_view_load_uri", "webkit_web_view_get_uri",
		"webkit_network_session_new", "webkit_network_session_get_cookie_manager",
		"webkit_cookie_manager_get_all_cookies",
		"webkit_cookie_manager_get_all_cookies_finish",
		"webkit_cookie_manager_replace_cookies",
		"webkit_cookie_manager_set_persistent_storage",
	},
}

// ensureLoaded resolves the libraries once. It returns an error instead of
// panicking so a machine without WebKitGTK gets a readable failure rather than a
// crash inside a purego registration.
func ensureLoaded() error {
	loadOnce.Do(func() { loadErr = load() })
	return loadErr
}

func load() error {
	gtk, err := openLib(libGTKNames)
	if err != nil {
		return fmt.Errorf("loginwin: gtk4: %w", err)
	}
	webkit, err := openLib(libWebKitNames)
	if err != nil {
		return fmt.Errorf("loginwin: webkitgtk 6.0: %w", err)
	}
	soup, err := openLib(libSoupNames)
	if err != nil {
		return fmt.Errorf("loginwin: libsoup 3: %w", err)
	}
	gobject, err := openLib(libGObjectName)
	if err != nil {
		return fmt.Errorf("loginwin: gobject: %w", err)
	}
	glib, err := openLib(libGLibName)
	if err != nil {
		return fmt.Errorf("loginwin: glib: %w", err)
	}

	// The symbol set is fixed at build time for one WebKitGTK major version, so a
	// missing one is a broken install rather than a runtime condition.
	var missing []string
	for group, names := range requiredSymbols {
		handle := map[string]uintptr{"gtk": gtk, "webkit": webkit}[group]
		for _, name := range names {
			if _, err := purego.Dlsym(handle, name); err != nil {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("loginwin: missing symbols: %s", strings.Join(missing, ", "))
	}

	purego.RegisterLibFunc(&fn.gtkInit, gtk, "gtk_init")
	purego.RegisterLibFunc(&fn.gtkWindowNew, gtk, "gtk_window_new")
	purego.RegisterLibFunc(&fn.gtkWindowSetTitle, gtk, "gtk_window_set_title")
	purego.RegisterLibFunc(&fn.gtkWindowSetDefaultSize, gtk, "gtk_window_set_default_size")
	purego.RegisterLibFunc(&fn.gtkWindowSetChild, gtk, "gtk_window_set_child")
	purego.RegisterLibFunc(&fn.gtkWindowPresent, gtk, "gtk_window_present")
	purego.RegisterLibFunc(&fn.gtkWindowDestroy, gtk, "gtk_window_destroy")

	purego.RegisterLibFunc(&fn.webkitWebViewGetType, webkit, "webkit_web_view_get_type")
	purego.RegisterLibFunc(&fn.webkitWebViewNew, webkit, "webkit_web_view_new")
	purego.RegisterLibFunc(&fn.webkitWebViewLoadURI, webkit, "webkit_web_view_load_uri")
	purego.RegisterLibFunc(&fn.webkitWebViewGetURI, webkit, "webkit_web_view_get_uri")
	purego.RegisterLibFunc(&fn.webkitNetworkSessionNew, webkit, "webkit_network_session_new")
	purego.RegisterLibFunc(&fn.webkitNetworkSessionGetCookieManager, webkit, "webkit_network_session_get_cookie_manager")
	purego.RegisterLibFunc(&fn.webkitCookieManagerGetAllCookies, webkit, "webkit_cookie_manager_get_all_cookies")
	purego.RegisterLibFunc(&fn.webkitCookieManagerGetAllCookiesFin, webkit, "webkit_cookie_manager_get_all_cookies_finish")
	purego.RegisterLibFunc(&fn.webkitCookieManagerReplaceCookies, webkit, "webkit_cookie_manager_replace_cookies")
	purego.RegisterLibFunc(&fn.webkitCookieManagerSetPersistStorage, webkit, "webkit_cookie_manager_set_persistent_storage")

	purego.RegisterLibFunc(&fn.soupCookieNew, soup, "soup_cookie_new")
	purego.RegisterLibFunc(&fn.soupCookieGetName, soup, "soup_cookie_get_name")
	purego.RegisterLibFunc(&fn.soupCookieGetValue, soup, "soup_cookie_get_value")
	purego.RegisterLibFunc(&fn.soupCookieGetDomain, soup, "soup_cookie_get_domain")
	purego.RegisterLibFunc(&fn.soupCookieGetPath, soup, "soup_cookie_get_path")
	purego.RegisterLibFunc(&fn.soupCookieGetSecure, soup, "soup_cookie_get_secure")
	purego.RegisterLibFunc(&fn.soupCookieGetHTTPOnly, soup, "soup_cookie_get_http_only")
	purego.RegisterLibFunc(&fn.soupCookieGetExpires, soup, "soup_cookie_get_expires")
	purego.RegisterLibFunc(&fn.soupCookieGetSameSite, soup, "soup_cookie_get_same_site_policy")
	purego.RegisterLibFunc(&fn.soupCookieSetSecure, soup, "soup_cookie_set_secure")
	purego.RegisterLibFunc(&fn.soupCookieSetHTTPOnly, soup, "soup_cookie_set_http_only")
	purego.RegisterLibFunc(&fn.soupCookieSetSameSite, soup, "soup_cookie_set_same_site_policy")
	purego.RegisterLibFunc(&fn.soupCookieFree, soup, "soup_cookie_free")

	purego.RegisterLibFunc(&fn.gObjectNewWithProperties, gobject, "g_object_new_with_properties")
	purego.RegisterLibFunc(&fn.gObjectUnref, gobject, "g_object_unref")
	purego.RegisterLibFunc(&fn.gTypeFromName, gobject, "g_type_from_name")
	purego.RegisterLibFunc(&fn.gValueInit, gobject, "g_value_init")
	purego.RegisterLibFunc(&fn.gValueSetObject, gobject, "g_value_set_object")
	purego.RegisterLibFunc(&fn.gValueUnset, gobject, "g_value_unset")

	purego.RegisterLibFunc(&fn.gIdleAdd, glib, "g_idle_add")
	purego.RegisterLibFunc(&fn.gErrorFree, glib, "g_error_free")
	purego.RegisterLibFunc(&fn.gListLength, glib, "g_list_length")
	purego.RegisterLibFunc(&fn.gListNthData, glib, "g_list_nth_data")
	purego.RegisterLibFunc(&fn.gListAppend, glib, "g_list_append")
	purego.RegisterLibFunc(&fn.gListFree, glib, "g_list_free")
	purego.RegisterLibFunc(&fn.gDateTimeToUnix, glib, "g_date_time_to_unix")

	return nil
}

func openLib(names []string) (uintptr, error) {
	var lastErr error
	for _, name := range names {
		handle, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			return handle, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

// Session is one login window.
type Session struct {
	opts Options

	mu      sync.Mutex
	window  uintptr
	view    uintptr
	manager uintptr
	closing chan struct{}
	closeOn sync.Once
	handle  uintptr
}

// Open creates the window on the GTK main thread and loads the first page.
//
// It blocks until the main loop has run the creation, so it must be called from
// any goroutine other than the GTK thread. Wails already runs that loop, so in
// the app this is a plain service call.
func Open(opts Options) (*Session, error) {
	if err := ensureLoaded(); err != nil {
		return nil, err
	}
	if opts.URL == "" {
		opts.URL = DefaultLoginURL
	}
	if opts.JarDir == "" {
		return nil, fmt.Errorf("loginwin: JarDir is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	s := &Session{
		opts:    opts,
		closing: make(chan struct{}),
	}
	s.handle = retain(s)

	done := make(chan error, 1)
	postToMain(func() { done <- s.create() })

	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
		return s, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("loginwin: the GTK main loop did not run the window creation")
	}
}

// create builds the window. It must run on the GTK thread.
func (s *Session) create() error {
	// gtk_init is idempotent, so calling it here is safe whether or not the host
	// application already initialised GTK.
	fn.gtkInit()

	dataDir := filepath.Join(s.opts.JarDir, "webkit")
	cacheDir := filepath.Join(s.opts.JarDir, "cache")
	for _, dir := range []string{dataDir, cacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("loginwin: %w", err)
		}
	}

	// A private network session keeps this window's cookies out of the app's own
	// webview, which is the whole point of the separate window.
	strs, keep := cstrings(dataDir, cacheDir)
	session := fn.webkitNetworkSessionNew(strs[0], strs[1])
	runtime.KeepAlive(keep)
	if session == 0 {
		return fmt.Errorf("loginwin: webkit_network_session_new returned NULL")
	}

	manager := fn.webkitNetworkSessionGetCookieManager(session)
	if manager == 0 {
		fn.gObjectUnref(session)
		return fmt.Errorf("loginwin: the network session has no cookie manager")
	}

	// Persisting the jar means a second launch can reuse the session instead of
	// asking the user to sign in again.
	jarPath := filepath.Join(s.opts.JarDir, "cookies.sqlite")
	ps, keepPS := cstrings(jarPath)
	fn.webkitCookieManagerSetPersistStorage(manager, ps[0], persistentStorageSQLite)
	runtime.KeepAlive(keepPS)

	// webkit_web_view_new() always rides the default session, so the view is
	// built through the construct property instead.
	//
	// The type has to come from webkit_web_view_get_type: g_type_from_name only
	// sees types that have already been registered, and nothing has touched the
	// WebKit type system yet at this point.
	viewType := fn.webkitWebViewGetType()
	if viewType == 0 {
		fn.gObjectUnref(session)
		return fmt.Errorf("loginwin: the WebKitWebView type is not registered")
	}

	var propValue gvalue
	fn.gValueInit(&propValue, gtypeObject)
	fn.gValueSetObject(&propValue, session)

	namePtrs, keepName := cstrings("network-session")
	view := fn.gObjectNewWithProperties(viewType, 1, &namePtrs[0], &propValue)
	fn.gValueUnset(&propValue)
	runtime.KeepAlive(keepName)
	if view == 0 {
		fn.gObjectUnref(session)
		return fmt.Errorf("loginwin: could not create the web view")
	}

	// g_object_new takes its own reference on the session, so the local one is
	// dropped here; the view releases its reference when it is destroyed.
	fn.gObjectUnref(session)

	window := fn.gtkWindowNew()
	if window == 0 {
		fn.gObjectUnref(view)
		return fmt.Errorf("loginwin: could not create the window")
	}

	title, keepTitle := cstrings("Sign in to YouTube Music")
	fn.gtkWindowSetTitle(window, title[0])
	runtime.KeepAlive(keepTitle)
	fn.gtkWindowSetDefaultSize(window, 520, 760)
	fn.gtkWindowSetChild(window, view)

	s.mu.Lock()
	s.window, s.view, s.manager = window, view, manager
	s.mu.Unlock()

	fn.gtkWindowPresent(window)

	uri, keepURI := cstrings(s.opts.URL)
	fn.webkitWebViewLoadURI(view, uri[0])
	runtime.KeepAlive(keepURI)
	return nil
}

// Wait blocks until the jar holds a usable session, then returns it.
//
// It polls the jar rather than watching load events: a page can finish loading on
// accounts.google.com and still hold no session.
//
// The caller's context owns the deadline. When it has none, Options.Timeout is
// applied instead, so a caller that forgets one still cannot wait forever.
func (s *Session) Wait(ctx context.Context) (Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Timeout)
		defer cancel()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		cookies, err := s.harvest(ctx)
		if err != nil {
			return Result{}, err
		}
		if header := cookieHeader(cookies); HasSessionCookies(header) {
			return Result{
				URL:          s.currentURL(),
				SAPISID:      sapiSID(cookies),
				CookieHeader: header,
				Cookies:      cookies,
			}, nil
		}

		select {
		case <-ctx.Done():
			return Result{}, ErrTimeout
		case <-s.closing:
			return Result{}, ErrClosed
		case <-ticker.C:
		}
	}
}

// currentURL reports the page the window is showing, for diagnostics.
func (s *Session) currentURL() string {
	s.mu.Lock()
	view := s.view
	s.mu.Unlock()
	if view == 0 {
		return ""
	}
	return goString(fn.webkitWebViewGetURI(view))
}

// harvestCall carries the reply channel to the async cookie callback.
type harvestCall struct {
	ch     chan harvestResult
	handle uintptr
}

type harvestResult struct {
	cookies []Cookie
	err     error
}

// harvest asks the cookie manager for the jar and waits for the async reply.
//
// The finish call runs inside the callback: the loop that delivers the reply is
// the same thread that would otherwise be blocked waiting for it.
func (s *Session) harvest(ctx context.Context) ([]Cookie, error) {
	s.mu.Lock()
	manager := s.manager
	s.mu.Unlock()
	if manager == 0 {
		return nil, ErrClosed
	}

	call := &harvestCall{ch: make(chan harvestResult, 1)}
	call.handle = retain(call)
	postToMain(func() {
		fn.webkitCookieManagerGetAllCookies(manager, 0, cookieCallback, call.handle)
	})

	select {
	case res := <-call.ch:
		release(call.handle)
		return res.cookies, res.err
	case <-ctx.Done():
		release(call.handle)
		return nil, ctx.Err()
	}
}

// cookieCallback is the GAsyncReadyCallback for get_all_cookies.
var cookieCallback = purego.NewCallback(func(source, result, data uintptr) {
	call, ok := lookup(data).(*harvestCall)
	if !ok {
		return
	}

	var gerr uintptr
	list := fn.webkitCookieManagerGetAllCookiesFin(source, result, &gerr)
	if gerr != 0 {
		fn.gErrorFree(gerr)
		call.ch <- harvestResult{err: fmt.Errorf("loginwin: reading the cookie jar failed")}
		return
	}
	if list == 0 {
		call.ch <- harvestResult{}
		return
	}

	cookies := listToCookies(list)
	fn.gListFree(list)
	call.ch <- harvestResult{cookies: cookies}
})

// listToCookies converts a GList of SoupCookie into Go values.
func listToCookies(list uintptr) []Cookie {
	n := int(fn.gListLength(list))
	out := make([]Cookie, 0, n)
	for i := 0; i < n; i++ {
		ptr := fn.gListNthData(list, uint32(i))
		if ptr == 0 {
			continue
		}
		out = append(out, readCookie(ptr))
	}
	return out
}

// readCookie copies one SoupCookie. Nothing borrowed is retained.
func readCookie(ptr uintptr) Cookie {
	c := Cookie{
		Name:     goString(fn.soupCookieGetName(ptr)),
		Value:    goString(fn.soupCookieGetValue(ptr)),
		Domain:   goString(fn.soupCookieGetDomain(ptr)),
		Path:     goString(fn.soupCookieGetPath(ptr)),
		Secure:   fn.soupCookieGetSecure(ptr) != 0,
		HTTPOnly: fn.soupCookieGetHTTPOnly(ptr) != 0,
	}
	if expires := fn.soupCookieGetExpires(ptr); expires != 0 {
		c.Expires = time.Unix(fn.gDateTimeToUnix(expires), 0)
	}
	switch fn.soupCookieGetSameSite(ptr) {
	case 1:
		c.SameSite = "lax"
	case 2:
		c.SameSite = "strict"
	}
	return c
}

// restoreCall carries the reply channel to the async replace callback.
type restoreCall struct {
	ch     chan error
	handle uintptr
}

// Restore seeds the jar from a saved set, so a relaunch can reuse the session.
func (s *Session) Restore(cookies []Cookie) error {
	s.mu.Lock()
	manager := s.manager
	s.mu.Unlock()
	if manager == 0 {
		return ErrClosed
	}
	if len(cookies) == 0 {
		return nil
	}

	// The call is created here rather than inside the closure so the timeout path
	// can reach it and undo what the closure registered.
	call := &restoreCall{ch: make(chan error, 1)}
	call.handle = retain(call)

	postToMain(func() {
		var list uintptr
		var built []uintptr
		for _, c := range cookies {
			ptr := buildSoupCookie(c)
			if ptr == 0 {
				continue
			}
			built = append(built, ptr)
			list = fn.gListAppend(list, ptr)
		}
		if list == 0 {
			release(call.handle)
			call.ch <- nil
			return
		}

		// The cookies and the list stay alive until the reply arrives: replace is
		// asynchronous, so freeing them here could pull them out from under it.
		restorePending.Store(call.handle, built)
		fn.webkitCookieManagerReplaceCookies(manager, list, 0, restoreCallback, call.handle)
	})

	// Without the loop running there is no reply, so the wait is bounded rather
	// than depending on the caller having a live GTK thread.
	select {
	case err := <-call.ch:
		return err
	case <-time.After(30 * time.Second):
		// The callback will never run, so the cookies it was meant to free are
		// released here instead, and the handle is dropped.
		if built, loaded := restorePending.LoadAndDelete(call.handle); loaded {
			for _, ptr := range built.([]uintptr) {
				fn.soupCookieFree(ptr)
			}
		}
		release(call.handle)
		return fmt.Errorf("loginwin: the GTK main loop did not answer the cookie restore")
	}
}

// restorePending keeps the SoupCookie pointers alive across the async reply.
var restorePending sync.Map

var restoreCallback = purego.NewCallback(func(source, result, data uintptr) {
	call, ok := lookup(data).(*restoreCall)
	if !ok {
		return
	}
	if built, loaded := restorePending.LoadAndDelete(data); loaded {
		for _, ptr := range built.([]uintptr) {
			fn.soupCookieFree(ptr)
		}
	}
	release(call.handle)
	call.ch <- nil
})

// buildSoupCookie creates one SoupCookie from a Go value.
func buildSoupCookie(c Cookie) uintptr {
	if c.Name == "" || c.Domain == "" {
		return 0
	}
	path := c.Path
	if path == "" {
		path = "/"
	}

	strs, keep := cstrings(c.Name, c.Value, c.Domain, path)
	// -1 makes the cookie a session cookie.
	ptr := fn.soupCookieNew(strs[0], strs[1], strs[2], strs[3], -1)
	runtime.KeepAlive(keep)
	if ptr == 0 {
		return 0
	}

	if c.Secure {
		fn.soupCookieSetSecure(ptr, 1)
	}
	if c.HTTPOnly {
		fn.soupCookieSetHTTPOnly(ptr, 1)
	}
	switch c.SameSite {
	case "lax":
		fn.soupCookieSetSameSite(ptr, 1)
	case "strict":
		fn.soupCookieSetSameSite(ptr, 2)
	}
	return ptr
}

// Close destroys the window and drops the session.
func (s *Session) Close() {
	s.closeOn.Do(func() {
		s.mu.Lock()
		window := s.window
		s.window, s.view, s.manager = 0, 0, 0
		s.mu.Unlock()

		close(s.closing)

		postToMain(func() {
			// Destroying the window destroys the child it owns, and the view
			// releases the network session on its way out. Unref'ing the view
			// here as well is a double free, which GTK reports as a failed
			// G_IS_OBJECT assertion and later turns into a crash.
			if window != 0 {
				fn.gtkWindowDestroy(window)
			}
			release(s.handle)
		})
	})
}

// Job and reference registries.
//
// A Go value cannot be handed to C as a pointer, so each one gets an integer
// handle that C passes back untouched.
var (
	refMu   sync.Mutex
	refs    = map[uintptr]any{}
	refNext uintptr
)

func retain(v any) uintptr {
	refMu.Lock()
	refNext++
	handle := refNext
	refs[handle] = v
	refMu.Unlock()
	return handle
}

func lookup(handle uintptr) any {
	refMu.Lock()
	v := refs[handle]
	refMu.Unlock()
	return v
}

func release(handle uintptr) {
	refMu.Lock()
	delete(refs, handle)
	refMu.Unlock()
}

// idleCallback runs a queued job on the GTK thread.
var idleCallback = purego.NewCallback(func(data uintptr) uintptr {
	job, ok := lookup(data).(func())
	release(data)
	if ok {
		job()
	}
	// G_SOURCE_REMOVE: the source is one shot.
	return 0
})

// postToMain queues fn on the GTK main loop. g_idle_add is thread safe, which is
// what makes it safe to call from a service goroutine.
func postToMain(job func()) {
	handle := retain(job)
	fn.gIdleAdd(idleCallback, handle)
}

// cstrings returns NUL-terminated copies of the given strings plus a keepalive
// slice the caller must hold across the call.
func cstrings(ss ...string) ([]*byte, []byte) {
	buf := make([]byte, 0, 64)
	for _, s := range ss {
		buf = append(buf, s...)
		buf = append(buf, 0)
	}
	ptrs := make([]*byte, len(ss))
	offset := 0
	for i, s := range ss {
		ptrs[i] = &buf[offset]
		offset += len(s) + 1
	}
	return ptrs, buf
}

// cstr returns a NUL-terminated copy of one string.
func cstr(s string) *byte {
	buf := append([]byte(s), 0)
	return &buf[0]
}

// goString copies a NUL-terminated C string. The FFI signatures return *byte
// rather than uintptr so this never has to convert an integer into a pointer,
// which vet rightly flags.
func goString(p *byte) string {
	if p == nil {
		return ""
	}
	const maxCString = 1 << 16
	buf := unsafe.Slice(p, maxCString)
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}
