//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework AppKit
#import <Cocoa/Cocoa.h>

// Bring the app to the front. WindowShow unhides the window but leaves the
// process unactivated, so a restored HUD could otherwise appear behind
// whatever the user was working in.
static void lw_activateApp(void) {
	void (^run)(void) = ^{
		[NSApp unhide:nil];
		[NSApp activateIgnoringOtherApps:YES];
		for (NSWindow *w in [NSApp windows]) {
			if ([w isVisible]) {
				[w makeKeyAndOrderFront:nil];
				break;
			}
		}
	};
	if ([NSThread isMainThread]) {
		run();
		return;
	}
	dispatch_async(dispatch_get_main_queue(), run);
}

// Hide the application itself, not just the window. This hands focus back to
// whatever the user was working in. That deactivation is what makes reopen
// detection sound: once hidden, Lightwave can only become active again through
// a deliberate summon (Dock click, Cmd-Tab), never as leftover focus from the
// keystroke that hid it.
static void lw_hideApp(void) {
	void (^run)(void) = ^{
		[NSApp hide:nil];
		// Frameless windows sometimes leave activation stuck on the app even
		// after hide:. Deactivate explicitly so focus reliably returns to the
		// previous app — the reopen watcher needs a real inactive->active
		// transition to recognise a Dock click.
		if ([NSApp isActive]) {
			[NSApp deactivate];
		}
	};
	if ([NSThread isMainThread]) {
		run();
		return;
	}
	dispatch_async(dispatch_get_main_queue(), run);
}

// Reports whether Lightwave is the active (frontmost) application.
static int lw_isFrontmost(void) {
	__block int result = 0;
	void (^check)(void) = ^{
		result = [NSApp isActive] ? 1 : 0;
	};
	if ([NSThread isMainThread]) {
		check();
	} else {
		dispatch_sync(dispatch_get_main_queue(), check);
	}
	return result;
}
*/
import "C"

// activateApp raises Lightwave above other applications and focuses its window.
func activateApp() {
	C.lw_activateApp()
}

// hideApp hides the application and returns focus to the previous app.
func hideApp() {
	C.lw_hideApp()
}

// appFrontmost is true while Lightwave is the active application.
func appFrontmost() bool {
	return C.lw_isFrontmost() == 1
}
