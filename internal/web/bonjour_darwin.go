//go:build darwin

package web

/*
#include <dns_sd.h>
#include <arpa/inet.h>

// DNSServiceRegister rejects a NULL callback, though nothing here needs the
// result.
static void lw_registered(DNSServiceRef ref, DNSServiceFlags flags,
		DNSServiceErrorType err, const char *name, const char *type,
		const char *domain, void *ctx) {}

// lw_register advertises the control server over Bonjour. The registration is
// live as soon as the call returns; mDNSResponder keeps it until the ref is
// deallocated, so there is no result loop to pump.
// A NULL name tells mDNSResponder to use the Mac's own name ("Renato's Mac
// Studio"), which is what the phone should show.
static DNSServiceRef lw_register(uint16_t port) {
	DNSServiceRef ref = NULL;
	DNSServiceErrorType err = DNSServiceRegister(&ref, 0, 0, NULL, "_lightwave._tcp",
		NULL, NULL, htons(port), 0, NULL, lw_registered, NULL);
	if (err != kDNSServiceErr_NoError) {
		return NULL;
	}
	return ref;
}
*/
import "C"

// advertise publishes the server as _lightwave._tcp so the iOS app finds it
// without typing an address. The returned func withdraws it.
func advertise(port int) func() {
	ref := C.lw_register(C.uint16_t(port))
	if ref == nil {
		return func() {}
	}
	return func() { C.DNSServiceRefDeallocate(ref) }
}
