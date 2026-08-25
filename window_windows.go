//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procReleaseCapture           = user32.NewProc("ReleaseCapture")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procShowWindow               = user32.NewProc("ShowWindow")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindow                = user32.NewProc("GetWindow")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
)

const (
	swShow          = 5
	swRestore       = 9
	gwOwner         = 4
	wmNCLButtonDown = 0x00A1
	htCaption       = 2
)

func startNativeWindowDrag() {
	hwnd := appHWND()
	if hwnd == 0 {
		return
	}
	procReleaseCapture.Call()
	procSendMessageW.Call(hwnd, wmNCLButtonDown, htCaption, 0)
}

func activateApp() {
	hwnd := appHWND()
	if hwnd == 0 {
		return
	}
	iconic, _, _ := procIsIconic.Call(hwnd)
	if iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, swShow)
	}
	forceForeground(hwnd)
}

func hideApp() {
	// runtime.WindowHide already called ShowWindow(SW_HIDE). Windows hands
	// focus to the previous window on its own; extra deactivation is a macOS
	// NSApp quirk we do not need here.
}

func appFrontmost() bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	return windows.HWND(fg) == windows.HWND(appHWND())
}

func forceForeground(hwnd uintptr) {
	procAllowSetForegroundWindow.Call(^uintptr(0)) // ASFW_ANY
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == hwnd {
		return
	}
	var fgTid, ourTid uintptr
	if fg != 0 {
		fgTid, _, _ = procGetWindowThreadProcessId.Call(fg, 0)
	}
	ourTid, _, _ = procGetCurrentThreadId.Call()
	if fgTid != 0 && fgTid != ourTid {
		procAttachThreadInput.Call(ourTid, fgTid, 1)
		procBringWindowToTop.Call(hwnd)
		procSetForegroundWindow.Call(hwnd)
		procAttachThreadInput.Call(ourTid, fgTid, 0)
		return
	}
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
}

func appHWND() uintptr {
	pid := windows.GetCurrentProcessId()
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		var wpid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&wpid)))
		if wpid != pid {
			return 1
		}
		owner, _, _ := procGetWindow.Call(hwnd, gwOwner)
		if owner != 0 {
			return 1
		}
		found = hwnd
		return 0
	})
	procEnumWindows.Call(cb, 0)
	return found
}
