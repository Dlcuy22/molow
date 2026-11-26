//go:build windows

package loginwin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	targetYTMCookies = "https://music.youtube.com/"

	cookiePollTimerID = 1
	cookiePollMs      = 500

	// Disable Device-Bound Session Credentials so Google issues standard
	// long-lived cookies rather than TPM-bound __Secure-1PSIDRTS tokens.
	webviewAdditionalArgs = "--disable-features=DeviceBoundSessions,EnableBoundSessionCredentials"
)

var (
	modUser32               = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW    = modUser32.NewProc("RegisterClassExW")
	procUnregisterClassW    = modUser32.NewProc("UnregisterClassW")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW      = modUser32.NewProc("DefWindowProcW")
	procShowWindow          = modUser32.NewProc("ShowWindow")
	procUpdateWindow        = modUser32.NewProc("UpdateWindow")
	procGetClientRect       = modUser32.NewProc("GetClientRect")
	procSetTimer            = modUser32.NewProc("SetTimer")
	procKillTimer           = modUser32.NewProc("KillTimer")
	procGetMessageW         = modUser32.NewProc("GetMessageW")
	procTranslateMessage    = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW    = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage     = modUser32.NewProc("PostQuitMessage")
	procPostMessageW        = modUser32.NewProc("PostMessageW")
	procSetWindowPos        = modUser32.NewProc("SetWindowPos")
	procBringWindowToTop    = modUser32.NewProc("BringWindowToTop")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procLoadCursorW         = modUser32.NewProc("LoadCursorW")
	procSetProcessDPIAware  = modUser32.NewProc("SetProcessDPIAware")

	modOle32           = windows.NewLazySystemDLL("ole32.dll")
	procCoTaskMemAlloc = modOle32.NewProc("CoTaskMemAlloc")

	modKernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procRtlMoveMemory = modKernel32.NewProc("RtlMoveMemory")
)

const (
	wmDestroy = 0x0002
	wmMove    = 0x0003
	wmSize    = 0x0005
	wmClose   = 0x0010
	wmTimer   = 0x0113

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	cwUseDefault       = 0x80000000
	swShow             = 5
	idcArrow           = 32512

	hwndTopMost   = ^uintptr(0)
	hwndNoTopMost = ^uintptr(1)
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpShowWindow = 0x0040
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type winPoint struct {
	X int32
	Y int32
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

var (
	hostsMu   sync.Mutex
	hostsByHw = map[uintptr]*loginHost{}
	wndProcCB = windows.NewCallback(loginWndProc)
)

func loginWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	hostsMu.Lock()
	h := hostsByHw[hwnd]
	hostsMu.Unlock()

	if h != nil {
		switch msg {
		case wmSize:
			h.resize()
			return 0
		case wmMove:
			if h.controller != nil {
				_ = h.controller.NotifyParentWindowPositionChanged()
			}
			return 0
		case wmTimer:
			if wParam == cookiePollTimerID {
				h.onPollTimer()
			}
			return 0
		case wmClose:
			_, _, _ = procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			_, _, _ = procKillTimer.Call(hwnd, cookiePollTimerID)
			_, _, _ = procPostQuitMessage.Call(0)
			return 0
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}

type waitOutcome struct {
	res Result
	err error
}

// Session represents one isolated Windows WebView2 sign-in window.
type Session struct {
	opts    Options
	doneCh  chan waitOutcome
	closeOn sync.Once

	mu          sync.Mutex
	hwnd        uintptr
	closedByApp bool
	lastURL     string
	lastCookies []Cookie
}

// Open launches an isolated WebView2 sign-in window on a dedicated STA thread
// and begins polling its private cookie jar for a YouTube Music session.
func Open(opts Options) (*Session, error) {
	return openSession(opts, false)
}

func openSession(opts Options, hidden bool) (*Session, error) {
	if opts.JarDir == "" {
		return nil, errors.New("loginwin: JarDir is required")
	}
	if opts.URL == "" {
		opts.URL = DefaultLoginURL
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	dllPath, err := findWebView2ClientDLL()
	if err != nil {
		return nil, err
	}
	dll, err := windows.LoadDLL(dllPath)
	if err != nil {
		return nil, fmt.Errorf("loginwin: load %s: %w", dllPath, err)
	}
	createEnvProc, err := dll.FindProc("CreateWebViewEnvironmentWithOptionsInternal")
	if err != nil {
		return nil, fmt.Errorf("loginwin: find CreateWebViewEnvironmentWithOptionsInternal: %w", err)
	}

	s := &Session{
		opts:    opts,
		doneCh:  make(chan waitOutcome, 1),
		lastURL: opts.URL,
	}
	readyCh := make(chan error, 1)
	go s.runSTA(createEnvProc, hidden, readyCh)

	if err := <-readyCh; err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) runSTA(createEnvProc *windows.Proc, hidden bool, readyCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	_, _, _ = procSetProcessDPIAware.Call()

	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		readyCh <- fmt.Errorf("loginwin: CoInitializeEx: %w", err)
		return
	}
	defer windows.CoUninitialize()

	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)

	classNameStr := fmt.Sprintf("MolowYTMLogin_%d", time.Now().UnixNano())
	className, err := windows.UTF16PtrFromString(classNameStr)
	if err != nil {
		readyCh <- err
		return
	}
	title, err := windows.UTF16PtrFromString("Sign in to YouTube Music")
	if err != nil {
		readyCh <- err
		return
	}
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)

	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProcCB,
		hInstance:     hinst,
		hCursor:       windows.Handle(cursor),
		lpszClassName: className,
	}
	atom, _, regErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		readyCh <- fmt.Errorf("loginwin: RegisterClassExW: %w", regErr)
		return
	}
	defer procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), uintptr(hinst))

	style := uintptr(wsOverlappedWindow)
	if !hidden {
		style |= wsVisible
	}

	hwnd, _, createErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		style,
		cwUseDefault,
		cwUseDefault,
		520,
		760,
		0,
		0,
		uintptr(hinst),
		0,
	)
	if hwnd == 0 {
		readyCh <- fmt.Errorf("loginwin: CreateWindowExW: %w", createErr)
		return
	}

	host := newLoginHost(s, hwnd, s.opts.URL)
	hostsMu.Lock()
	hostsByHw[hwnd] = host
	hostsMu.Unlock()

	defer func() {
		hostsMu.Lock()
		delete(hostsByHw, hwnd)
		hostsMu.Unlock()
		host.close()
	}()

	if !hidden {
		_, _, _ = procShowWindow.Call(hwnd, swShow)
		_, _, _ = procUpdateWindow.Call(hwnd)
		bringToFront(hwnd)
	}

	userDataPtr, err := windows.UTF16PtrFromString(s.opts.JarDir)
	if err != nil {
		_, _, _ = procDestroyWindow.Call(hwnd)
		readyCh <- err
		return
	}

	hr, _, _ := createEnvProc.Call(
		1,
		0,
		uintptr(unsafe.Pointer(userDataPtr)),
		uintptr(unsafe.Pointer(host.envOptions)),
		uintptr(unsafe.Pointer(host.envHandler)),
	)
	if int32(hr) < 0 {
		_, _, _ = procDestroyWindow.Call(hwnd)
		readyCh <- fmt.Errorf("loginwin: CreateWebViewEnvironmentWithOptionsInternal failed: 0x%08x", uint32(hr))
		return
	}

	s.mu.Lock()
	s.hwnd = hwnd
	s.mu.Unlock()
	_, _, _ = procSetTimer.Call(hwnd, cookiePollTimerID, cookiePollMs, 0)
	readyCh <- nil

	var m winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	s.mu.Lock()
	s.hwnd = 0
	s.mu.Unlock()

	switch {
	case host.err != nil:
		s.doneCh <- waitOutcome{err: host.err}
	case host.done:
		s.doneCh <- waitOutcome{res: Result{
			URL:          host.capturedURL,
			SAPISID:      host.sapisid,
			CookieHeader: host.captured,
		}}
	default:
		s.doneCh <- waitOutcome{err: ErrClosed}
	}
}

// Wait blocks until the window captures a YouTube Music session, the user closes
// the window, or the context/Timeout expires.
func (s *Session) Wait(ctx context.Context) (Result, error) {
	if _, ok := ctx.Deadline(); !ok && s.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Timeout)
		defer cancel()
	}

	select {
	case out := <-s.doneCh:
		return out.res, out.err
	case <-ctx.Done():
		s.Close()
		<-s.doneCh

		s.mu.Lock()
		cookies := s.lastCookies
		s.mu.Unlock()

		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%w (%s)", ErrTimeout, describeJar(cookies))
		}
		return Result{}, ctx.Err()
	}
}

// Close closes the login window and shuts down its STA loop.
func (s *Session) Close() {
	s.closeOn.Do(func() {
		s.mu.Lock()
		hwnd := s.hwnd
		s.closedByApp = true
		s.mu.Unlock()
		if hwnd != 0 {
			_, _, _ = procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
	})
}

func (s *Session) currentURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

// bringToFront lifts the login window above the active Wails window across
// threads without leaving it permanently always-on-top.
func bringToFront(hwnd uintptr) {
	flags := uintptr(swpNoMove | swpNoSize | swpShowWindow)
	_, _, _ = procSetWindowPos.Call(hwnd, hwndTopMost, 0, 0, 0, 0, flags)
	_, _, _ = procSetWindowPos.Call(hwnd, hwndNoTopMost, 0, 0, 0, 0, flags)
	_, _, _ = procBringWindowToTop.Call(hwnd)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

// --- COM plumbing ---

var (
	iidUnknown = windows.GUID{
		Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000,
		Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
	}
	iidAgileObject = windows.GUID{
		Data1: 0x94ea2b94, Data2: 0xe9cc, Data3: 0x49e0,
		Data4: [8]byte{0xc0, 0xff, 0xee, 0x64, 0xca, 0x8f, 0x5b, 0x90},
	}
	iidEnvOptions = windows.GUID{
		Data1: 0x2fde08a8, Data2: 0x1e9a, Data3: 0x4766,
		Data4: [8]byte{0x8c, 0x05, 0x95, 0xa9, 0xce, 0xb9, 0xd1, 0xc5},
	}
	iidEnvCompletedHandler = windows.GUID{
		Data1: 0x4e8a3389, Data2: 0xc9d8, Data3: 0x4bd2,
		Data4: [8]byte{0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d},
	}
	iidCtrlCompletedHandler = windows.GUID{
		Data1: 0x6c4819f3, Data2: 0xc9b7, Data3: 0x4260,
		Data4: [8]byte{0x81, 0x27, 0xc9, 0xf5, 0xbd, 0xe7, 0xf6, 0x8c},
	}
	iidCookiesCompletedHandler = windows.GUID{
		Data1: 0x5a4f5069, Data2: 0x5c15, Data3: 0x47c3,
		Data4: [8]byte{0x86, 0x46, 0xf4, 0xde, 0x1c, 0x11, 0x66, 0x70},
	}
	iidCoreWebView2_2 = windows.GUID{
		Data1: 0x9E8F0CF8, Data2: 0xE670, Data3: 0x4B5E,
		Data4: [8]byte{0xB2, 0xBC, 0x73, 0xE0, 0x61, 0xE3, 0x18, 0x4C},
	}
)

const (
	sOK          = uintptr(0)
	eNoInterface = uintptr(0x80004002)
)

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type comEnvOptionsVtbl struct {
	iUnknownVtbl
	GetAdditionalBrowserArguments             uintptr
	PutAdditionalBrowserArguments             uintptr
	GetLanguage                               uintptr
	PutLanguage                               uintptr
	GetTargetCompatibleBrowserVersion         uintptr
	PutTargetCompatibleBrowserVersion         uintptr
	GetAllowSingleSignOnUsingOSPrimaryAccount uintptr
	PutAllowSingleSignOnUsingOSPrimaryAccount uintptr
}

type comEnvOptions struct {
	vtbl *comEnvOptionsVtbl
	args string
	refs int32
}

type comCompletedHandlerVtbl struct {
	iUnknownVtbl
	Invoke uintptr
}

type comEnvCompletedHandler struct {
	vtbl *comCompletedHandlerVtbl
	host *loginHost
	refs int32
}

type comCtrlCompletedHandler struct {
	vtbl *comCompletedHandlerVtbl
	host *loginHost
	refs int32
}

type comCookiesCompletedHandler struct {
	vtbl *comCompletedHandlerVtbl
	host *loginHost
	refs int32
}

type comEnvironmentVtbl struct {
	iUnknownVtbl
	CreateCoreWebView2Controller uintptr
}

type comEnvironment struct {
	vtbl *comEnvironmentVtbl
}

type comControllerVtbl struct {
	iUnknownVtbl
	GetIsVisible                      uintptr
	PutIsVisible                      uintptr
	GetBounds                         uintptr
	PutBounds                         uintptr
	GetZoomFactor                     uintptr
	PutZoomFactor                     uintptr
	AddZoomFactorChanged              uintptr
	RemoveZoomFactorChanged           uintptr
	SetBoundsAndZoomFactor            uintptr
	MoveFocus                         uintptr
	AddMoveFocusRequested             uintptr
	RemoveMoveFocusRequested          uintptr
	AddGotFocus                       uintptr
	RemoveGotFocus                    uintptr
	AddLostFocus                      uintptr
	RemoveLostFocus                   uintptr
	AddAcceleratorKeyPressed          uintptr
	RemoveAcceleratorKeyPressed       uintptr
	GetParentWindow                   uintptr
	PutParentWindow                   uintptr
	NotifyParentWindowPositionChanged uintptr
	Close                             uintptr
	GetCoreWebView2                   uintptr
}

type comController struct {
	vtbl *comControllerVtbl
}

type comWebView2_2Vtbl struct {
	iUnknownVtbl
	GetSettings                       uintptr
	GetSource                         uintptr
	Navigate                          uintptr
	_rest                             [55]uintptr
	AddWebResourceResponseReceived    uintptr
	RemoveWebResourceResponseReceived uintptr
	NavigateWithWebResourceRequest    uintptr
	AddDomContentLoaded               uintptr
	RemoveDomContentLoaded            uintptr
	GetCookieManager                  uintptr
	GetEnvironment                    uintptr
}

type comWebView2_2 struct {
	vtbl *comWebView2_2Vtbl
}

type comCookieManagerVtbl struct {
	iUnknownVtbl
	CreateCookie                   uintptr
	CopyCookie                     uintptr
	GetCookies                     uintptr
	AddOrUpdateCookie              uintptr
	DeleteCookie                   uintptr
	DeleteCookies                  uintptr
	DeleteCookiesWithDomainAndPath uintptr
	DeleteAllCookies               uintptr
}

type comCookieManager struct {
	vtbl *comCookieManagerVtbl
}

type comCookieListVtbl struct {
	iUnknownVtbl
	GetCount uintptr
	GetItem  uintptr
}

type comCookieList struct {
	vtbl *comCookieListVtbl
}

type comCookieVtbl struct {
	iUnknownVtbl
	GetName       uintptr
	GetValue      uintptr
	PutValue      uintptr
	GetDomain     uintptr
	GetPath       uintptr
	GetExpires    uintptr
	PutExpires    uintptr
	GetIsHttpOnly uintptr
	PutIsHttpOnly uintptr
	GetSameSite   uintptr
	PutSameSite   uintptr
	GetIsSecure   uintptr
	PutIsSecure   uintptr
}

type comCookie struct {
	vtbl *comCookieVtbl
}

func (c *comController) Release() {
	_, _, _ = syscall.SyscallN(c.vtbl.Release, uintptr(unsafe.Pointer(c)))
}

func (c *comController) Close() {
	_, _, _ = syscall.SyscallN(c.vtbl.Close, uintptr(unsafe.Pointer(c)))
}

func (c *comController) PutIsVisible(vis bool) error {
	v := uintptr(0)
	if vis {
		v = 1
	}
	hr, _, _ := syscall.SyscallN(c.vtbl.PutIsVisible, uintptr(unsafe.Pointer(c)), v)
	if int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

func (c *comController) PutBounds(r winRect) error {
	hr, _, _ := syscall.SyscallN(c.vtbl.PutBounds, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&r)))
	if int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

func (c *comController) NotifyParentWindowPositionChanged() error {
	hr, _, _ := syscall.SyscallN(c.vtbl.NotifyParentWindowPositionChanged, uintptr(unsafe.Pointer(c)))
	if int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

func (c *comController) GetCoreWebView2() (*comWebView2_2, error) {
	var wv *comWebView2_2
	hr, _, _ := syscall.SyscallN(c.vtbl.GetCoreWebView2, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&wv)))
	if int32(hr) < 0 {
		return nil, windows.Errno(hr)
	}
	return wv, nil
}

func (w *comWebView2_2) Release() {
	_, _, _ = syscall.SyscallN(w.vtbl.Release, uintptr(unsafe.Pointer(w)))
}

func (w *comWebView2_2) Source() string {
	var p *uint16
	hr, _, _ := syscall.SyscallN(w.vtbl.GetSource, uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(&p)))
	if int32(hr) < 0 || p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s
}

func (w *comWebView2_2) Navigate(uri string) error {
	p, err := windows.UTF16PtrFromString(uri)
	if err != nil {
		return err
	}
	hr, _, _ := syscall.SyscallN(w.vtbl.Navigate, uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(p)))
	if int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

func (w *comWebView2_2) QueryInterface2() (*comWebView2_2, error) {
	var out *comWebView2_2
	hr, _, _ := syscall.SyscallN(
		w.vtbl.QueryInterface,
		uintptr(unsafe.Pointer(w)),
		uintptr(unsafe.Pointer(&iidCoreWebView2_2)),
		uintptr(unsafe.Pointer(&out)),
	)
	if int32(hr) < 0 {
		return nil, windows.Errno(hr)
	}
	return out, nil
}

func (w *comWebView2_2) GetCookieManager() (*comCookieManager, error) {
	var mgr *comCookieManager
	hr, _, _ := syscall.SyscallN(w.vtbl.GetCookieManager, uintptr(unsafe.Pointer(w)), uintptr(unsafe.Pointer(&mgr)))
	if int32(hr) < 0 {
		return nil, windows.Errno(hr)
	}
	return mgr, nil
}

func (m *comCookieManager) Release() {
	_, _, _ = syscall.SyscallN(m.vtbl.Release, uintptr(unsafe.Pointer(m)))
}

func (m *comCookieManager) GetCookies(uri string, handler *comCookiesCompletedHandler) error {
	p, err := windows.UTF16PtrFromString(uri)
	if err != nil {
		return err
	}
	hr, _, _ := syscall.SyscallN(
		m.vtbl.GetCookies,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(handler)),
	)
	if int32(hr) < 0 {
		return windows.Errno(hr)
	}
	return nil
}

func (l *comCookieList) GetCount() (uint32, error) {
	var n uint32
	hr, _, _ := syscall.SyscallN(l.vtbl.GetCount, uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(&n)))
	if int32(hr) < 0 {
		return 0, windows.Errno(hr)
	}
	return n, nil
}

func (l *comCookieList) GetItem(idx uint32) (*comCookie, error) {
	var ck *comCookie
	hr, _, _ := syscall.SyscallN(l.vtbl.GetItem, uintptr(unsafe.Pointer(l)), uintptr(idx), uintptr(unsafe.Pointer(&ck)))
	if int32(hr) < 0 {
		return nil, windows.Errno(hr)
	}
	return ck, nil
}

func (ck *comCookie) Release() {
	_, _, _ = syscall.SyscallN(ck.vtbl.Release, uintptr(unsafe.Pointer(ck)))
}

func (ck *comCookie) readString(proc uintptr) (string, error) {
	var p *uint16
	hr, _, _ := syscall.SyscallN(proc, uintptr(unsafe.Pointer(ck)), uintptr(unsafe.Pointer(&p)))
	if int32(hr) < 0 {
		return "", windows.Errno(hr)
	}
	if p == nil {
		return "", nil
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s, nil
}

func (ck *comCookie) toCookie() (Cookie, error) {
	name, err := ck.readString(ck.vtbl.GetName)
	if err != nil {
		return Cookie{}, err
	}
	val, err := ck.readString(ck.vtbl.GetValue)
	if err != nil {
		return Cookie{}, err
	}
	domain, err := ck.readString(ck.vtbl.GetDomain)
	if err != nil {
		return Cookie{}, err
	}
	path, err := ck.readString(ck.vtbl.GetPath)
	if err != nil {
		return Cookie{}, err
	}

	var expBits uint64
	_, _, _ = syscall.SyscallN(ck.vtbl.GetExpires, uintptr(unsafe.Pointer(ck)), uintptr(unsafe.Pointer(&expBits)))
	var exp time.Time
	if secs := math.Float64frombits(expBits); secs > 0 {
		exp = time.Unix(int64(secs), 0)
	}

	var httpOnly, secure int32
	_, _, _ = syscall.SyscallN(ck.vtbl.GetIsHttpOnly, uintptr(unsafe.Pointer(ck)), uintptr(unsafe.Pointer(&httpOnly)))
	_, _, _ = syscall.SyscallN(ck.vtbl.GetIsSecure, uintptr(unsafe.Pointer(ck)), uintptr(unsafe.Pointer(&secure)))

	return Cookie{
		Name:     name,
		Value:    val,
		Domain:   domain,
		Path:     path,
		HTTPOnly: httpOnly != 0,
		Secure:   secure != 0,
		Expires:  exp,
	}, nil
}

// --- loginHost & COM callbacks ---

type loginHost struct {
	session  *Session
	hwnd     uintptr
	startURL string

	envOptions     *comEnvOptions
	envHandler     *comEnvCompletedHandler
	ctrlHandler    *comCtrlCompletedHandler
	cookiesHandler *comCookiesCompletedHandler

	env        *comEnvironment
	controller *comController
	webview    *comWebView2_2
	cookieMgr  *comCookieManager

	pollInFlight bool
	done         bool
	captured     string
	sapisid      string
	capturedURL  string
	err          error
}

var (
	envOptionsVtbl = comEnvOptionsVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: windows.NewCallback(envOptionsQI),
			AddRef:         windows.NewCallback(envOptionsAddRef),
			Release:        windows.NewCallback(envOptionsRelease),
		},
		GetAdditionalBrowserArguments:             windows.NewCallback(envOptionsGetArgs),
		PutAdditionalBrowserArguments:             windows.NewCallback(comNotImpl),
		GetLanguage:                               windows.NewCallback(envOptionsGetEmptyStr),
		PutLanguage:                               windows.NewCallback(comNotImpl),
		GetTargetCompatibleBrowserVersion:         windows.NewCallback(envOptionsGetTargetVer),
		PutTargetCompatibleBrowserVersion:         windows.NewCallback(comNotImpl),
		GetAllowSingleSignOnUsingOSPrimaryAccount: windows.NewCallback(envOptionsGetSSO),
		PutAllowSingleSignOnUsingOSPrimaryAccount: windows.NewCallback(comNotImpl),
	}

	envHandlerVtbl = comCompletedHandlerVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: windows.NewCallback(envHandlerQI),
			AddRef:         windows.NewCallback(genericNoopRef),
			Release:        windows.NewCallback(genericNoopRef),
		},
		Invoke: windows.NewCallback(onEnvCompleted),
	}

	ctrlHandlerVtbl = comCompletedHandlerVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: windows.NewCallback(ctrlHandlerQI),
			AddRef:         windows.NewCallback(genericNoopRef),
			Release:        windows.NewCallback(genericNoopRef),
		},
		Invoke: windows.NewCallback(onCtrlCompleted),
	}

	cookiesHandlerVtbl = comCompletedHandlerVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: windows.NewCallback(cookiesHandlerQI),
			AddRef:         windows.NewCallback(genericNoopRef),
			Release:        windows.NewCallback(genericNoopRef),
		},
		Invoke: windows.NewCallback(onCookiesCompleted),
	}
)

func newLoginHost(session *Session, hwnd uintptr, startURL string) *loginHost {
	h := &loginHost{
		session:  session,
		hwnd:     hwnd,
		startURL: startURL,
	}
	h.envOptions = &comEnvOptions{vtbl: &envOptionsVtbl, args: webviewAdditionalArgs, refs: 1}
	h.envHandler = &comEnvCompletedHandler{vtbl: &envHandlerVtbl, host: h, refs: 1}
	h.ctrlHandler = &comCtrlCompletedHandler{vtbl: &ctrlHandlerVtbl, host: h, refs: 1}
	h.cookiesHandler = &comCookiesCompletedHandler{vtbl: &cookiesHandlerVtbl, host: h, refs: 1}
	return h
}

func (h *loginHost) resize() {
	if h.controller == nil {
		return
	}
	var r winRect
	_, _, _ = procGetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&r)))
	_ = h.controller.PutBounds(r)
}

func (h *loginHost) onPollTimer() {
	if h.done || h.err != nil {
		return
	}
	if h.webview != nil {
		if u := h.webview.Source(); u != "" {
			h.session.mu.Lock()
			h.session.lastURL = u
			h.session.mu.Unlock()
		}
	}
	if h.cookieMgr == nil || h.pollInFlight {
		return
	}
	h.pollInFlight = true
	if err := h.cookieMgr.GetCookies(targetYTMCookies, h.cookiesHandler); err != nil {
		h.pollInFlight = false
	}
}

func (h *loginHost) fail(err error) {
	h.err = err
	_, _, _ = procDestroyWindow.Call(h.hwnd)
}

func (h *loginHost) close() {
	if h.cookieMgr != nil {
		h.cookieMgr.Release()
		h.cookieMgr = nil
	}
	if h.webview != nil {
		h.webview.Release()
		h.webview = nil
	}
	if h.controller != nil {
		h.controller.Close()
		h.controller.Release()
		h.controller = nil
	}
	if h.env != nil {
		_, _, _ = syscall.SyscallN(h.env.vtbl.Release, uintptr(unsafe.Pointer(h.env)))
		h.env = nil
	}
}

func guidEqual(a, b *windows.GUID) bool {
	return a.Data1 == b.Data1 && a.Data2 == b.Data2 && a.Data3 == b.Data3 && a.Data4 == b.Data4
}

func handlerQI(this unsafe.Pointer, iid *windows.GUID, obj *unsafe.Pointer, selfIID *windows.GUID) uintptr {
	if guidEqual(iid, &iidUnknown) || guidEqual(iid, &iidAgileObject) || guidEqual(iid, selfIID) {
		*obj = this
		return sOK
	}
	*obj = nil
	return eNoInterface
}

func envHandlerQI(this unsafe.Pointer, iid *windows.GUID, obj *unsafe.Pointer) uintptr {
	return handlerQI(this, iid, obj, &iidEnvCompletedHandler)
}

func ctrlHandlerQI(this unsafe.Pointer, iid *windows.GUID, obj *unsafe.Pointer) uintptr {
	return handlerQI(this, iid, obj, &iidCtrlCompletedHandler)
}

func cookiesHandlerQI(this unsafe.Pointer, iid *windows.GUID, obj *unsafe.Pointer) uintptr {
	return handlerQI(this, iid, obj, &iidCookiesCompletedHandler)
}

func genericNoopRef(unsafe.Pointer) uintptr { return 1 }

func envOptionsQI(o *comEnvOptions, iid *windows.GUID, obj **comEnvOptions) uintptr {
	if guidEqual(iid, &iidUnknown) || guidEqual(iid, &iidAgileObject) || guidEqual(iid, &iidEnvOptions) {
		*obj = o
		o.refs++
		return sOK
	}
	*obj = nil
	return eNoInterface
}

func envOptionsAddRef(o *comEnvOptions) uintptr {
	o.refs++
	return uintptr(o.refs)
}

func envOptionsRelease(o *comEnvOptions) uintptr {
	o.refs--
	return uintptr(o.refs)
}

func allocCoTaskUTF16(s string) uintptr {
	w := utf16.Encode([]rune(s + "\x00"))
	p, _, _ := procCoTaskMemAlloc.Call(uintptr(len(w) * 2))
	if p != 0 {
		_, _, _ = procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&w[0])), uintptr(len(w)*2))
	}
	return p
}

func envOptionsGetArgs(o *comEnvOptions, val *uintptr) uintptr {
	*val = allocCoTaskUTF16(o.args)
	return sOK
}

func envOptionsGetEmptyStr(_ *comEnvOptions, val *uintptr) uintptr {
	*val = allocCoTaskUTF16("")
	return sOK
}

func envOptionsGetTargetVer(_ *comEnvOptions, val *uintptr) uintptr {
	*val = allocCoTaskUTF16("86.0.616.0")
	return sOK
}

func envOptionsGetSSO(_ *comEnvOptions, val *int32) uintptr {
	*val = 0
	return sOK
}

func comNotImpl(uintptr) uintptr {
	return uintptr(0x80004001) // E_NOTIMPL
}

func onEnvCompleted(this *comEnvCompletedHandler, hr uintptr, env *comEnvironment) uintptr {
	h := this.host
	if int32(hr) < 0 || env == nil {
		h.fail(fmt.Errorf("loginwin: WebView2 environment creation failed: 0x%08x", uint32(hr)))
		return sOK
	}
	_, _, _ = syscall.SyscallN(env.vtbl.AddRef, uintptr(unsafe.Pointer(env)))
	h.env = env

	ctrlHR, _, _ := syscall.SyscallN(
		env.vtbl.CreateCoreWebView2Controller,
		uintptr(unsafe.Pointer(env)),
		h.hwnd,
		uintptr(unsafe.Pointer(h.ctrlHandler)),
	)
	if int32(ctrlHR) < 0 {
		h.fail(fmt.Errorf("loginwin: CreateCoreWebView2Controller failed: 0x%08x", uint32(ctrlHR)))
	}
	return sOK
}

func onCtrlCompleted(this *comCtrlCompletedHandler, hr uintptr, ctrl *comController) uintptr {
	h := this.host
	if int32(hr) < 0 || ctrl == nil {
		h.fail(fmt.Errorf("loginwin: WebView2 controller creation failed: 0x%08x", uint32(hr)))
		return sOK
	}
	_, _, _ = syscall.SyscallN(ctrl.vtbl.AddRef, uintptr(unsafe.Pointer(ctrl)))
	h.controller = ctrl

	wv, err := ctrl.GetCoreWebView2()
	if err != nil {
		h.fail(fmt.Errorf("loginwin: GetCoreWebView2: %w", err))
		return sOK
	}
	wv2, err := wv.QueryInterface2()
	wv.Release()
	if err != nil {
		h.fail(fmt.Errorf("loginwin: QueryInterface(ICoreWebView2_2): %w", err))
		return sOK
	}
	h.webview = wv2

	mgr, err := wv2.GetCookieManager()
	if err != nil {
		h.fail(fmt.Errorf("loginwin: GetCookieManager: %w", err))
		return sOK
	}
	h.cookieMgr = mgr

	h.resize()
	_ = ctrl.PutIsVisible(true)
	if err := wv2.Navigate(h.startURL); err != nil {
		h.fail(fmt.Errorf("loginwin: Navigate: %w", err))
	}
	return sOK
}

func onCookiesCompleted(this *comCookiesCompletedHandler, hr uintptr, list *comCookieList) uintptr {
	h := this.host
	h.pollInFlight = false
	if h.done || int32(hr) < 0 || list == nil {
		return sOK
	}

	count, err := list.GetCount()
	if err != nil || count == 0 {
		return sOK
	}

	cookies := make([]Cookie, 0, count)
	for i := uint32(0); i < count; i++ {
		ck, err := list.GetItem(i)
		if err != nil || ck == nil {
			continue
		}
		c, err := ck.toCookie()
		ck.Release()
		if err == nil {
			cookies = append(cookies, c)
		}
	}

	h.session.mu.Lock()
	h.session.lastCookies = cookies
	h.session.mu.Unlock()

	if !hasYouTubeSession(cookies) {
		return sOK
	}
	header := cookieHeader(cookies)
	if header == "" {
		return sOK
	}

	if h.webview != nil {
		h.capturedURL = h.webview.Source()
	}
	h.sapisid = sapiSID(cookies)
	h.captured = header
	h.done = true
	_, _, _ = procKillTimer.Call(h.hwnd, cookiePollTimerID)
	_, _, _ = procDestroyWindow.Call(h.hwnd)
	return sOK
}

func findWebView2ClientDLL() (string, error) {
	const installKey = `Software\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	arch := "x64"
	switch runtime.GOARCH {
	case "arm64":
		arch = "arm64"
	case "386":
		arch = "x86"
	}

	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, installKey, registry.READ|registry.WOW64_32KEY)
		if err != nil {
			continue
		}
		ebFolder, _, err := k.GetStringValue("EBWebView")
		_ = k.Close()
		if err != nil || ebFolder == "" {
			continue
		}
		dll := filepath.Join(ebFolder, "EBWebView", arch, "EmbeddedBrowserWebView.dll")
		if _, err := os.Stat(dll); err == nil {
			return dll, nil
		}
	}
	return "", errors.New("loginwin: WebView2 runtime not found in Windows registry")
}
