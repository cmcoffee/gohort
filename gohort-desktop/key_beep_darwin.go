//go:build darwin

package main

// No beep for a key the page does not mark as handled.
//
// WebKit sends a key to the page first. When the page's script does not call
// preventDefault (an app's "R" shortcut that runs and simply returns), the web
// view hands the key back to Cocoa, which passes it up the responder chain. At
// the top nothing takes it, and the window's noResponderFor: plays the system
// alert sound. So the shortcut works AND the Mac beeps, on every press. Safari
// keeps quiet in the same spot; a bare WKWebView does not.
//
// This gives Wails' window class its own noResponderFor:, which drops an
// unhandled key quietly and leaves every other unhandled action to NSWindow's
// own, as before. Menu shortcuts (Cmd+R, Cmd+F) never get here: Cocoa's menu
// takes them first.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

// keyBeepSuper is NSWindow's own noResponderFor:, for everything but a key.
static IMP keyBeepSuper;

static void quietNoResponderFor(id self, SEL _cmd, SEL action) {
    if (action == @selector(keyDown:)) {
        return;
    }
    ((void (*)(id, SEL, SEL))keyBeepSuper)(self, _cmd, action);
}

// installQuietKeys adds the method to Wails' window class: 1 when added, 0
// when the class is not there, -1 when it already has one of its own, which
// then stays.
static int installQuietKeys(void) {
    Class c = objc_getClass("WailsWindow");
    if (!c) {
        return 0;
    }
    keyBeepSuper = class_getMethodImplementation([NSWindow class], @selector(noResponderFor:));
    return class_addMethod(c, @selector(noResponderFor:), (IMP)quietNoResponderFor, "v@::") ? 1 : -1;
}
*/
import "C"

import "github.com/cmcoffee/gohort/gohort-desktop/core"

// At package init, with the page-dialog methods: the class is compiled into
// the binary, and nothing has made a window from it yet.
func init() { installQuietKeys() }

// installQuietKeys stops the system beep on a key the page leaves unhandled.
func installQuietKeys() {
	switch int(C.installQuietKeys()) {
	case 0:
		core.Warn("[gohort-desktop] quiet keys: the window class was not found, so a key a page leaves unhandled still beeps")
	case -1:
		core.Log("[gohort-desktop] quiet keys: the window class has its own noResponderFor:, left as it is")
	default:
		core.Log("[gohort-desktop] quiet keys: a key a page leaves unhandled no longer beeps")
	}
}
