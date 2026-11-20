//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework AppKit
#import <Cocoa/Cocoa.h>

extern void lwActivationChanged(int active);

// Tell Go each time Lightwave becomes, or stops being, the active app. The
// observers live for the life of the process.
static void lw_watchActivation(void) {
	NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
	[nc addObserverForName:NSApplicationDidBecomeActiveNotification object:nil queue:nil
		usingBlock:^(NSNotification *n) { lwActivationChanged(1); }];
	[nc addObserverForName:NSApplicationDidResignActiveNotification object:nil queue:nil
		usingBlock:^(NSNotification *n) { lwActivationChanged(0); }];
}
*/
import "C"

import "sync"

var (
	activationOnce   sync.Once
	activationEvents = make(chan bool, 16)
)

//export lwActivationChanged
func lwActivationChanged(active C.int) {
	// Runs on the AppKit main thread: never block it. The reopen loop only
	// needs the latest transitions, and a full buffer means it is behind
	// anyway.
	select {
	case activationEvents <- active != 0:
	default:
	}
}

// watchActivation reports activation changes: true when Lightwave becomes the
// active app, false when it stops being it.
func watchActivation() <-chan bool {
	activationOnce.Do(func() { C.lw_watchActivation() })
	return activationEvents
}
