package main

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const macAppKit = "/System/Library/Frameworks/AppKit.framework/AppKit"

// See the Apple Blocks ABI. This is a WebKit-owned completion block, not a Go
// block (objc.Block.Invoke only accepts Go-created blocks). It is called once,
// synchronously, and never retained by Go.
type macDecisionBlock struct {
	isa             uintptr
	flags, reserved uint32
	invoke          uintptr
}

type macNavigation struct {
	desktop    *desktop
	downstream objc.ID
}

var (
	macNavigationOnce  sync.Once
	macNavigationClass objc.Class
	macNavigationErr   error
	macNavigationKey   objc.SEL
	macAssociate       func(objc.ID, objc.SEL, objc.ID, uintptr)
	macNavigations     sync.Map // proxy ID -> owner and retained Wails delegate
)

func sendMac(id objc.ID, name string, args ...any) objc.ID {
	return id.Send(objc.RegisterName(name), args...)
}

func macResponds(id objc.ID, selector objc.SEL) bool {
	return id != 0 && objc.Send[bool](id, objc.RegisterName("respondsToSelector:"), selector)
}

func registerMacNavigation() (objc.Class, error) {
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, err
	}
	associate, err := purego.Dlsym(lib, "objc_setAssociatedObject")
	if err != nil {
		return 0, err
	}
	purego.RegisterFunc(&macAssociate, associate)
	macNavigationKey = objc.RegisterName("vistaNavigationPolicy")
	nsObject := objc.GetClass("NSObject")
	if nsObject == 0 {
		return 0, errors.New("Foundation is not loaded")
	}
	class, err := objc.RegisterClass("VistaNavigationPolicy", nsObject, nil, nil, []objc.MethodDef{
		{Cmd: objc.RegisterName("respondsToSelector:"), Fn: func(self objc.ID, cmd, target objc.SEL) bool {
			if objc.SendSuper[bool](self, cmd, target) {
				return true
			}
			value, ok := macNavigations.Load(self)
			return ok && macResponds(value.(macNavigation).downstream, target)
		}},
		{Cmd: objc.RegisterName("forwardingTargetForSelector:"), Fn: func(self objc.ID, cmd, target objc.SEL) objc.ID {
			if value, ok := macNavigations.Load(self); ok {
				downstream := value.(macNavigation).downstream
				if macResponds(downstream, target) {
					return downstream
				}
			}
			return self.SendSuper(cmd, target)
		}},
		{Cmd: objc.RegisterName("dealloc"), Fn: func(self objc.ID, cmd objc.SEL) {
			if value, ok := macNavigations.LoadAndDelete(self); ok {
				sendMac(value.(macNavigation).downstream, "release")
			}
			self.SendSuper(cmd)
		}},
	})
	if err != nil {
		return 0, err
	}
	// The last argument is an Objective-C block (@?), not an object or a
	// Go-created block. Keep its explicit Objective-C encoding.
	if !class.AddMethod(objc.RegisterName("webView:decidePolicyForNavigationAction:decisionHandler:"), objc.NewIMP(decideMacNavigation), "v@:@@@?") {
		return 0, errors.New("could not register the WKWebView navigation decision")
	}
	return class, nil
}

func decideMacNavigation(self objc.ID, _ objc.SEL, _ objc.ID, action objc.ID, decide *macDecisionBlock) {
	if decide == nil {
		return
	}
	policy := uintptr(0) // WKNavigationActionPolicyCancel
	if value, ok := macNavigations.Load(self); ok {
		url := sendMac(sendMac(sendMac(action, "request"), "URL"), "absoluteString")
		uri := objc.Send[string](url, objc.RegisterName("UTF8String"))
		popup := sendMac(action, "targetFrame") == 0
		clicked := objc.Send[int](action, objc.RegisterName("navigationType")) == 0 // WKNavigationTypeLinkActivated
		if uri != "" && value.(macNavigation).desktop.navigate(uri, popup, clicked) {
			policy = 1 // WKNavigationActionPolicyAllow
		}
	}
	purego.SyscallN(decide.invoke, uintptr(unsafe.Pointer(decide)), policy)
}

func findMacWebView(view objc.ID, class objc.Class) objc.ID {
	if objc.Send[bool](view, objc.RegisterName("isKindOfClass:"), class) {
		return view
	}
	children := sendMac(view, "subviews")
	n := objc.Send[uintptr](children, objc.RegisterName("count"))
	for i := uintptr(0); i < n; i++ {
		if found := findMacWebView(sendMac(children, "objectAtIndex:", i), class); found != 0 {
			return found
		}
	}
	return 0
}

func attachMacNavigation(window unsafe.Pointer, d *desktop) bool {
	macNavigationOnce.Do(func() { macNavigationClass, macNavigationErr = registerMacNavigation() })
	if window == nil || macNavigationErr != nil {
		return false
	}
	webViewClass := objc.GetClass("WKWebView")
	if webViewClass == 0 {
		return false
	}
	webView := findMacWebView(sendMac(objc.ID(uintptr(window)), "contentView"), webViewClass)
	downstream := sendMac(webView, "navigationDelegate")
	manager := sendMac(sendMac(webView, "configuration"), "userContentController")
	if downstream == 0 || manager == 0 {
		return false
	}
	proxy := sendMac(objc.ID(macNavigationClass), "new")
	if proxy == 0 {
		return false
	}
	macNavigations.Store(proxy, macNavigation{desktop: d, downstream: sendMac(downstream, "retain")})
	// WKWebView's navigationDelegate is weak. Associate the proxy with the
	// view, relay the other delegate methods, and disable the native bridge.
	key := macNavigationKey
	const retainNonatomic = 1 // OBJC_ASSOCIATION_RETAIN_NONATOMIC
	macAssociate(webView, key, proxy, retainNonatomic)
	sendMac(manager, "removeScriptMessageHandlerForName:", macString("external"))
	sendMac(webView, "setNavigationDelegate:", proxy)
	sendMac(proxy, "release")
	return true
}

func macString(text string) objc.ID {
	return sendMac(objc.ID(objc.GetClass("NSString")), "stringWithUTF8String:", text)
}

func showMacStartupError(message string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := purego.Dlopen(macAppKit, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return // The CLI still prints the startup error.
	}
	pool := sendMac(objc.ID(objc.GetClass("NSAutoreleasePool")), "new")
	defer sendMac(pool, "drain")
	sendMac(objc.ID(objc.GetClass("NSApplication")), "sharedApplication")
	alert := sendMac(objc.ID(objc.GetClass("NSAlert")), "new")
	defer sendMac(alert, "release")
	sendMac(alert, "setMessageText:", macString("Vista"))
	sendMac(alert, "setInformativeText:", macString(message))
	sendMac(alert, "addButtonWithTitle:", macString("OK"))
	sendMac(alert, "runModal")
}
