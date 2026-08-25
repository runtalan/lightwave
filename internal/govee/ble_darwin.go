//go:build darwin

package govee

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework CoreBluetooth

#include <stdlib.h>

void lw_ble_init(void);
void lw_ble_scan(int on);
void lw_ble_harvest(void);
void lw_ble_connect(const char *uuid);
void lw_ble_cancel(const char *uuid);
void lw_ble_write(const char *uuid, const void *buf, int len);
*/
import "C"

import (
	"unsafe"
)

func blePlatformInit() { C.lw_ble_init() }

func blePlatformScan(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.lw_ble_scan(v)
}

func blePlatformHarvest() { C.lw_ble_harvest() }

func blePlatformConnect(id string) {
	cu := C.CString(id)
	C.lw_ble_connect(cu)
	C.free(unsafe.Pointer(cu))
}

func blePlatformCancel(id string) {
	cu := C.CString(id)
	C.lw_ble_cancel(cu)
	C.free(unsafe.Pointer(cu))
}

func blePlatformWrite(id string, pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	cu := C.CString(id)
	C.lw_ble_write(cu, unsafe.Pointer(&pkt[0]), C.int(len(pkt)))
	C.free(unsafe.Pointer(cu))
}

//export goBLEState
func goBLEState(state C.int) { bleOnState(int(state)) }

//export goBLEMsg
func goBLEMsg(m *C.char) { bleOnMsg(C.GoString(m)) }

//export goBLENeedScan
func goBLENeedScan(cu *C.char) { bleOnNeedScan(C.GoString(cu)) }

//export goBLEWriteFail
func goBLEWriteFail(cu *C.char) { bleOnWriteFail(C.GoString(cu)) }

//export goBLEFound
func goBLEFound(cu, cn *C.char) { bleOnFound(C.GoString(cu), C.GoString(cn)) }

//export goBLEReady
func goBLEReady(cu *C.char) { bleOnReady(C.GoString(cu)) }

//export goBLEGone
func goBLEGone(cu *C.char) { bleOnGone(C.GoString(cu)) }
