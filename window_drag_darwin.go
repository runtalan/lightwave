//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework AppKit
#import <Cocoa/Cocoa.h>

void startNativeWindowDrag(void) {
	void (^run)(void) = ^{
		NSWindow *window = [NSApp keyWindow];
		if (window == nil) {
			window = [NSApp mainWindow];
		}
		if (window == nil) {
			for (NSWindow *w in [NSApp windows]) {
				if ([w isVisible]) {
					window = w;
					break;
				}
			}
		}
		if (window == nil) {
			return;
		}

		NSEvent *event = [NSApp currentEvent];
		NSEventType t = event != nil ? [event type] : (NSEventType)0;
		BOOL usable = event != nil && (t == NSEventTypeLeftMouseDown || t == NSEventTypeLeftMouseDragged);
		if (!usable) {
			NSPoint loc = [window mouseLocationOutsideOfEventStream];
			event = [NSEvent mouseEventWithType:NSEventTypeLeftMouseDown
									   location:loc
								  modifierFlags:0
									  timestamp:[[NSProcessInfo processInfo] systemUptime]
								   windowNumber:[window windowNumber]
										context:nil
									eventNumber:0
									 clickCount:1
									   pressure:1.0];
		}
		if (event != nil) {
			[window performWindowDragWithEvent:event];
		}
	};

	if ([NSThread isMainThread]) {
		run();
		return;
	}
	dispatch_async(dispatch_get_main_queue(), run);
}
*/
import "C"

func startNativeWindowDrag() {
	C.startNativeWindowDrag()
}
