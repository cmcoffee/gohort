//go:build darwin

package main

// The microphone for a page's dictation, on macOS.
//
// Two things stand between a page and the microphone in a WKWebView. The
// app's Info.plist must say why it wants it (NSMicrophoneUsageDescription,
// in build/darwin/Info.plist): without that line WebKit refuses every page,
// and the chat's 🎤 fails with "the microphone was not allowed". And WebKit
// asks the web view's UI delegate whether the page may have it, which Wails'
// delegate does not answer. This answers yes for the desktop's own pages,
// the ones served from its loopback address, and leaves any other page to
// WebKit's own prompt. macOS still asks once, the first time, whether
// Gohort may use the microphone at all.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

// WKMediaCaptureType and WKPermissionDecision values, by number: the names
// arrived in the macOS 12 SDK, and the build targets older systems too.
enum { micCaptureMicrophone = 1 };
enum { micDecisionPrompt = 0, micDecisionGrant = 1 };

static void micPermission(id self, SEL _cmd, WKWebView *wv, WKSecurityOrigin *origin, WKFrameInfo *frame, NSInteger type, void (^handler)(NSInteger)) {
    if (type == micCaptureMicrophone && [origin.host isEqualToString:@"127.0.0.1"]) {
        handler(micDecisionGrant);
        return;
    }
    handler(micDecisionPrompt);
}

// installMicPermission adds the method to Wails' delegate class: -1 when the
// class is not there, 0 when Wails has since grown its own, which then stays.
static int installMicPermission(void) {
    Class c = objc_getClass("WailsContext");
    if (!c) {
        return -1;
    }
    SEL sel = @selector(webView:requestMediaCapturePermissionForOrigin:initiatedByFrame:type:decisionHandler:);
    return class_addMethod(c, sel, (IMP)micPermission, "v@:@@@q@?") ? 1 : 0;
}
*/
import "C"

import "github.com/cmcoffee/gohort/gohort-desktop/core"

// At package init for the same reason as the page dialogs: WebKit asks the
// delegate which methods it answers once, when Wails assigns it.
func init() {
	switch C.installMicPermission() {
	case -1:
		core.Warn("[gohort-desktop] microphone: the web view's delegate class was not found, so a page's dictation is left to WebKit")
	case 1:
		core.Log("[gohort-desktop] microphone: granted to the desktop's own pages")
	}
}
