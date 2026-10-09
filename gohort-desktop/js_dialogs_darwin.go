//go:build darwin

package main

// A page's own dialogs on macOS.
//
// WebKit shows nothing for alert(), confirm() or prompt() by itself: it asks
// the web view's UI delegate, and Wails' delegate (WailsContext) answers
// none of the three. So a dialog went nowhere, in any frame: the shim's
// override reaches the outer page, but an app's page runs in a sandboxed
// frame with no origin, which nothing on the outer page can reach into and
// which has no synchronous way out. This adds the three delegate methods
// to Wails' class at startup, so the web view itself shows a native alert
// and hands the answer back, for every frame, with no help from the page.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

static NSAlert *jsDialog(WKWebView *wv, NSString *msg) {
    NSAlert *a = [[NSAlert alloc] init];
    NSString *host = wv.URL.host;
    a.messageText = (host && host.length) ? host : @"Gohort";
    a.informativeText = msg ? msg : @"";
    return a;
}

static void jsAlertPanel(id self, SEL _cmd, WKWebView *wv, NSString *msg, WKFrameInfo *frame, void (^handler)(void)) {
    NSAlert *a = jsDialog(wv, msg);
    [a addButtonWithTitle:@"OK"];
    [a runModal];
    handler();
}

static void jsConfirmPanel(id self, SEL _cmd, WKWebView *wv, NSString *msg, WKFrameInfo *frame, void (^handler)(BOOL)) {
    NSAlert *a = jsDialog(wv, msg);
    [a addButtonWithTitle:@"OK"];
    [a addButtonWithTitle:@"Cancel"];
    handler([a runModal] == NSAlertFirstButtonReturn);
}

static void jsPromptPanel(id self, SEL _cmd, WKWebView *wv, NSString *msg, NSString *def, WKFrameInfo *frame, void (^handler)(NSString *)) {
    NSAlert *a = jsDialog(wv, msg);
    NSTextField *field = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 320, 24)];
    field.stringValue = def ? def : @"";
    a.accessoryView = field;
    [a addButtonWithTitle:@"OK"];
    [a addButtonWithTitle:@"Cancel"];
    [a.window setInitialFirstResponder:field];
    if ([a runModal] == NSAlertFirstButtonReturn) {
        handler(field.stringValue);
    } else {
        handler(nil);
    }
}

// installJSDialogs adds the three methods to Wails' delegate class and
// reports how many it added: 0 when the class is not there, fewer than 3
// when Wails has since grown one of its own, which then stays.
static int installJSDialogs(void) {
    Class c = objc_getClass("WailsContext");
    if (!c) {
        return 0;
    }
    int n = 0;
    n += class_addMethod(c, @selector(webView:runJavaScriptAlertPanelWithMessage:initiatedByFrame:completionHandler:), (IMP)jsAlertPanel, "v@:@@@@?") ? 1 : 0;
    n += class_addMethod(c, @selector(webView:runJavaScriptConfirmPanelWithMessage:initiatedByFrame:completionHandler:), (IMP)jsConfirmPanel, "v@:@@@@?") ? 1 : 0;
    n += class_addMethod(c, @selector(webView:runJavaScriptTextInputPanelWithPrompt:defaultText:initiatedByFrame:completionHandler:), (IMP)jsPromptPanel, "v@:@@@@@?") ? 1 : 0;
    return n;
}
*/
import "C"

import "github.com/cmcoffee/gohort/gohort-desktop/core"

// The methods go in before Wails makes its window. WebKit asks the delegate
// which of these it answers ONCE, when the delegate is assigned, and keeps
// the answer: a method added after that is never called. Wails assigns it
// while building the window, before the startup callback, so this runs at
// package init, when the class already exists (it is compiled into the
// binary) and nothing has asked it anything yet.
func init() { installJSDialogs() }

// installJSDialogs gives the web view native alert, confirm and prompt
// dialogs.
func installJSDialogs() {
	n := int(C.installJSDialogs())
	if n == 0 {
		core.Warn("[gohort-desktop] page dialogs: the web view's delegate class was not found, so alert/confirm/prompt in a page show nothing")
		return
	}
	core.Log("[gohort-desktop] page dialogs: %d native handler(s) installed", n)
}
