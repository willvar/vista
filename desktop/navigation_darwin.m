#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#include <stdint.h>

extern int vistaMacNavigate(uintptr_t handle, char *uri, int popup, int clicked);
extern void vistaReleaseMacNavigation(uintptr_t handle);

@interface VistaNavigation : NSObject <WKNavigationDelegate> {
@public
    uintptr_t handle;
    id<WKNavigationDelegate> downstream;
}
@end

@implementation VistaNavigation
- (BOOL)respondsToSelector:(SEL)selector {
    return [super respondsToSelector:selector] || [downstream respondsToSelector:selector];
}
- (id)forwardingTargetForSelector:(SEL)selector {
    return [downstream respondsToSelector:selector] ? downstream : [super forwardingTargetForSelector:selector];
}
- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action
    decisionHandler:(void (^)(WKNavigationActionPolicy))decide {
    NSURL *url = action.request.URL;
    BOOL allowed = url && vistaMacNavigate(handle, (char *)url.absoluteString.UTF8String,
        action.targetFrame == nil, action.navigationType == WKNavigationTypeLinkActivated);
    decide(allowed ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}
- (void)dealloc {
    vistaReleaseMacNavigation(handle);
    [downstream release];
    [super dealloc];
}
@end

static WKWebView *findWebView(NSView *view) {
    if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
    for (NSView *child in view.subviews) {
        WKWebView *found = findWebView(child);
        if (found) return found;
    }
    return nil;
}

int vistaAttachMacNavigation(void *window, uintptr_t handle) {
    WKWebView *webView = findWebView([(NSWindow *)window contentView]);
    if (!webView || !webView.navigationDelegate) return 0;
    // Preserve Wails' navigation lifecycle and file picker while limiting the
    // WebView to the local service and disabling its native message bridge.
    VistaNavigation *policy = [[VistaNavigation alloc] init];
    policy->handle = handle;
    policy->downstream = [webView.navigationDelegate retain];
    [webView.configuration.userContentController removeScriptMessageHandlerForName:@"external"];
    static char key;
    objc_setAssociatedObject(webView, &key, policy, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
    webView.navigationDelegate = policy;
    [policy release];
    return 1;
}

void vistaShowMacStartupError(char *message) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"Vista Desktop";
        alert.informativeText = [NSString stringWithUTF8String:message] ?: @"Could not start Vista.";
        [alert addButtonWithTitle:@"OK"];
        [alert runModal];
        [alert release];
    }
}
