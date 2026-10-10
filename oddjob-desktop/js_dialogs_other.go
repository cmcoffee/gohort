//go:build !darwin

package main

// installJSDialogs is macOS-only: WebKitGTK and WebView2 show a page's
// alert, confirm and prompt on their own.
func installJSDialogs() {}
