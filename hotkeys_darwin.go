//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework ApplicationServices -framework Foundation
#import <ApplicationServices/ApplicationServices.h>
#import <Foundation/Foundation.h>

extern void goGlobalNumpadKey(int keycode);

static CFMachPortRef lw_key_tap = NULL;
static CFRunLoopSourceRef lw_key_source = NULL;

static bool lw_is_lightwave_key(CGKeyCode key) {
	// macOS virtual key codes for the numeric keypad: 0–9, +, −, ×, and ÷.
	switch (key) {
		case 82: case 83: case 84: case 85: case 86:
		case 87: case 88: case 89: case 91: case 92:
		case 67: case 69: case 75: case 78:
			return true;
		default:
			return false;
	}
}

static CGEventRef lw_key_callback(CGEventTapProxy proxy, CGEventType type,
		CGEventRef event, void *refcon) {
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (lw_key_tap != NULL) CGEventTapEnable(lw_key_tap, true);
		return event;
	}
	if (type != kCGEventKeyDown ||
		CGEventGetIntegerValueField(event, kCGKeyboardEventAutorepeat)) return event;
	CGKeyCode key = (CGKeyCode)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
	if (!lw_is_lightwave_key(key)) return event;
	goGlobalNumpadKey((int)key);
	// These keys are explicitly Lightwave controls, so don't also type into the
	// foreground application while they change a light.
	return NULL;
}

static int lw_start_global_numpad(void) {
	__block int started = 0;
	void (^run)(void) = ^{
		if (lw_key_tap != NULL) { started = 1; return; }
		NSDictionary *options = @{(__bridge id)kAXTrustedCheckOptionPrompt: @YES};
		if (!AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)options)) return;
		CGEventMask mask = CGEventMaskBit(kCGEventKeyDown);
		lw_key_tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
			kCGEventTapOptionDefault, mask, lw_key_callback, NULL);
		if (lw_key_tap == NULL) return;
		lw_key_source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, lw_key_tap, 0);
		CFRunLoopAddSource(CFRunLoopGetMain(), lw_key_source, kCFRunLoopCommonModes);
		CGEventTapEnable(lw_key_tap, true);
		started = 1;
	};
	if ([NSThread isMainThread]) run(); else dispatch_sync(dispatch_get_main_queue(), run);
	return started;
}

static void lw_stop_global_numpad(void) {
	void (^run)(void) = ^{
		if (lw_key_source != NULL) {
			CFRunLoopRemoveSource(CFRunLoopGetMain(), lw_key_source, kCFRunLoopCommonModes);
			CFRelease(lw_key_source); lw_key_source = NULL;
		}
		if (lw_key_tap != NULL) { CFMachPortInvalidate(lw_key_tap); CFRelease(lw_key_tap); lw_key_tap = NULL; }
	};
	if ([NSThread isMainThread]) run(); else dispatch_sync(dispatch_get_main_queue(), run);
}
*/
import "C"

import (
	"log"
	"sync"
)

var globalNumpad struct {
	sync.RWMutex
	handler func(int)
}

func startGlobalNumpad(handler func(int)) {
	globalNumpad.Lock()
	globalNumpad.handler = handler
	globalNumpad.Unlock()
	if C.lw_start_global_numpad() == 0 {
		log.Printf("global numpad unavailable: enable Lightwave in System Settings > Privacy & Security > Accessibility")
	}
}

func stopGlobalNumpad() {
	C.lw_stop_global_numpad()
	globalNumpad.Lock()
	globalNumpad.handler = nil
	globalNumpad.Unlock()
}

//export goGlobalNumpadKey
func goGlobalNumpadKey(key C.int) {
	globalNumpad.RLock()
	h := globalNumpad.handler
	globalNumpad.RUnlock()
	if h != nil {
		go h(int(key))
	}
}
