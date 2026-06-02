//go:build darwin

package bridge

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#import <Cocoa/Cocoa.h>

// showConsent shows a native modal alert (title + detail) with Deny and
// Allow buttons — Deny is the default for safety. Returns 1 only if the
// user clicks Allow. AppKit requires UI on the main thread, so the work
// is dispatched onto the main queue (serviced by the tray's run loop);
// the calling goroutine blocks on dispatch_sync until the user answers.
// The calling goroutine is never the main thread (it's the WS handler),
// so dispatch_sync can't deadlock.
static int showConsent(const char *title, const char *detail) {
    __block int allowed = 0;
    dispatch_sync(dispatch_get_main_queue(), ^{
        NSAlert *alert = [[NSAlert alloc] init];
        [alert setMessageText:[NSString stringWithUTF8String:title]];
        [alert setInformativeText:[NSString stringWithUTF8String:detail]];
        [alert setAlertStyle:NSAlertStyleWarning];
        [alert addButtonWithTitle:@"Deny"];   // first button = default
        [alert addButtonWithTitle:@"Allow"];  // second button
        [NSApp activateIgnoringOtherApps:YES];
        NSModalResponse resp = [alert runModal];
        allowed = (resp == NSAlertSecondButtonReturn) ? 1 : 0;
    });
    return allowed;
}
*/
import "C"

import "unsafe"

// nativeConfirm shows a native Allow/Deny alert from the bridge agent's
// own NSApplication. Reliable from a menu-bar (LSUIElement) agent —
// unlike osascript, which a background process may be blocked from
// showing. Returns true only on Allow. Blocks the calling goroutine
// until the user answers.
func nativeConfirm(title, detail string) bool {
	ct := C.CString(title)
	defer C.free(unsafe.Pointer(ct))
	cd := C.CString(detail)
	defer C.free(unsafe.Pointer(cd))
	return C.showConsent(ct, cd) == 1
}
