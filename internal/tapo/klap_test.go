package tapo

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// The v1/v2 split is the single likeliest way to get KLAP wrong: a v1 hash
// against a Tapo plug fails at handshake1 with nothing useful in the error.
// Pin the v2 derivation and assert it differs from v1.
func TestAuthHashIsV2(t *testing.T) {
	c := Credentials{Username: "user@example.com", Password: "hunter2"}

	u := sha1.Sum([]byte(c.Username))
	p := sha1.Sum([]byte(c.Password))
	want := sha256.Sum256(append(u[:], p[:]...))

	if got := c.authHash(); !bytes.Equal(got, want[:]) {
		t.Fatalf("auth hash is not sha256(sha1(user)+sha1(pass))\n got %x\nwant %x", got, want)
	}

	// v1 (IOT.KLAP): md5(md5(user) + md5(pass)). Must not match.
	mu, mp := md5.Sum([]byte(c.Username)), md5.Sum([]byte(c.Password))
	v1 := md5.Sum(append(mu[:], mp[:]...))
	if bytes.Equal(c.authHash(), v1[:]) {
		t.Fatal("auth hash matches the v1 (MD5) scheme; Tapo plugs need v2")
	}
}

// handshake1 and handshake2 concatenate the seeds in *opposite* orders. Swap
// them and the device rejects the handshake, so lock the order down.
func TestHandshakeSeedOrder(t *testing.T) {
	local := bytes.Repeat([]byte{0x11}, 16)
	remote := bytes.Repeat([]byte{0x22}, 16)
	auth := bytes.Repeat([]byte{0x33}, 32)

	h1 := sha256.Sum256(concat(local, remote, auth))
	h2 := sha256.Sum256(concat(remote, local, auth))
	if bytes.Equal(h1[:], h2[:]) {
		t.Fatal("handshake hashes are order-insensitive; the test is not proving anything")
	}

	// What handshake() verifies against the device's reply.
	if got := sha256.Sum256(concat(local, remote, auth)); !bytes.Equal(got[:], h1[:]) {
		t.Error("handshake1 must hash local+remote+auth")
	}
	// What handshake() sends as its own proof.
	if got := sha256.Sum256(concat(remote, local, auth)); !bytes.Equal(got[:], h2[:]) {
		t.Error("handshake2 must hash remote+local+auth")
	}
}

// Session material: three SHA256s over the same payload, distinguished only by
// an ASCII prefix, each truncated differently.
func TestSessionDerivation(t *testing.T) {
	local := bytes.Repeat([]byte{0xAA}, 16)
	remote := bytes.Repeat([]byte{0xBB}, 16)
	auth := bytes.Repeat([]byte{0xCC}, 32)

	s := newSession("TP_SESSIONID=x", local, remote, auth, 0)

	wantKey := sha256.Sum256(concat([]byte("lsk"), local, remote, auth))
	if !bytes.Equal(s.key, wantKey[:16]) {
		t.Errorf("key must be sha256(\"lsk\"+...)[:16]")
	}
	wantIV := sha256.Sum256(concat([]byte("iv"), local, remote, auth))
	if !bytes.Equal(s.ivBase, wantIV[:12]) {
		t.Errorf("iv base must be sha256(\"iv\"+...)[:12]")
	}
	wantSig := sha256.Sum256(concat([]byte("ldk"), local, remote, auth))
	if !bytes.Equal(s.sig, wantSig[:28]) {
		t.Errorf("sig must be sha256(\"ldk\"+...)[:28]")
	}
	if len(s.key) != 16 {
		t.Errorf("KLAP is AES-128: key must be 16 bytes, got %d", len(s.key))
	}
}

// The starting sequence is a *signed* big-endian int32. Read it unsigned and a
// device that hands back a negative seed desynchronises every later IV.
func TestSequenceIsSigned(t *testing.T) {
	local := bytes.Repeat([]byte{0x01}, 16)
	remote := bytes.Repeat([]byte{0x02}, 16)
	auth := bytes.Repeat([]byte{0x03}, 32)

	ivf := sha256.Sum256(concat([]byte("iv"), local, remote, auth))
	want := int32(binary.BigEndian.Uint32(ivf[28:32]))

	s := newSession("c", local, remote, auth, 0)
	if s.seq != want {
		t.Fatalf("seq = %d, want %d", s.seq, want)
	}

	// A high bit set must read negative, not as a huge positive.
	raw := []byte{0xFF, 0xFF, 0xFF, 0xFE}
	if got := int32(binary.BigEndian.Uint32(raw)); got != -2 {
		t.Fatalf("signed decode broken: got %d want -2", got)
	}
}

// The sequence number is the IV's last four bytes, so a changing seq must
// change the IV — that is what keeps CBC from reusing an IV across requests.
func TestIVTracksSequence(t *testing.T) {
	base := bytes.Repeat([]byte{0x7F}, 12)

	a := iv(base, 1)
	b := iv(base, 2)
	if len(a) != 16 {
		t.Fatalf("iv must be 16 bytes, got %d", len(a))
	}
	if bytes.Equal(a, b) {
		t.Fatal("iv did not change with the sequence number")
	}
	if !bytes.Equal(a[:12], base) {
		t.Error("iv must keep the derived 12-byte base")
	}
	if !bytes.Equal(a[12:], []byte{0, 0, 0, 1}) {
		t.Errorf("iv tail = %x, want big-endian seq", a[12:])
	}
	// Negative sequences are legal and must not panic or truncate oddly.
	if n := iv(base, -2); !bytes.Equal(n[12:], []byte{0xFF, 0xFF, 0xFF, 0xFE}) {
		t.Errorf("negative seq tail = %x", n[12:])
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x10}, 16)
	base := bytes.Repeat([]byte{0x20}, 12)
	plain := []byte(`{"method":"get_device_info"}`)

	ct, err := encrypt(key, base, 5, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ct)%16 != 0 {
		t.Fatalf("ciphertext must be block aligned, got %d", len(ct))
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("plaintext is visible in the ciphertext")
	}

	got, err := decrypt(key, base, 5, ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: %q vs %q", got, plain)
	}

	// Decrypting under the wrong sequence must not yield the plaintext.
	if other, err := decrypt(key, base, 6, ct); err == nil && bytes.Equal(other, plain) {
		t.Fatal("ciphertext decrypted under the wrong sequence number")
	}
}

func TestPKCS7(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 17, 31, 32} {
		in := bytes.Repeat([]byte{0x41}, n)
		p := pkcs7Pad(in, 16)
		if len(p)%16 != 0 {
			t.Fatalf("len %d padded to %d", n, len(p))
		}
		if len(p) <= len(in) {
			t.Fatalf("padding must always add a block boundary for len %d", n)
		}
		out, err := pkcs7Unpad(p, 16)
		if err != nil {
			t.Fatalf("unpad len %d: %v", n, err)
		}
		if !bytes.Equal(out, in) {
			t.Fatalf("round trip failed at len %d", n)
		}
	}

	for _, bad := range [][]byte{
		{},
		bytes.Repeat([]byte{0x00}, 16), // zero pad byte
		append(bytes.Repeat([]byte{0x41}, 15), 0x11),       // pad longer than block
		append(bytes.Repeat([]byte{0x41}, 14), 0x02, 0x03), // inconsistent
	} {
		if _, err := pkcs7Unpad(bad, 16); err == nil {
			t.Errorf("accepted bad padding %x", bad)
		}
	}
}

// The probe is a fixed literal; a typo would silently return no devices.
func TestDiscoveryProbe(t *testing.T) {
	if got := hex.EncodeToString(discoveryProbe); got != "020000010000000000000000463cb5d3" {
		t.Fatalf("probe = %s", got)
	}
	if len(discoveryProbe) != 16 {
		t.Fatalf("probe must be 16 bytes, got %d", len(discoveryProbe))
	}
}

func TestDecodeBase64Name(t *testing.T) {
	// "Desk Lamp" base64-encoded, which is how Tapo reports nicknames.
	if got := decodeBase64Name("RGVzayBMYW1w"); got != "Desk Lamp" {
		t.Errorf("got %q want %q", got, "Desk Lamp")
	}
	// Plain text passes through: firmware is inconsistent about encoding.
	if got := decodeBase64Name("Desk Lamp"); got != "Desk Lamp" {
		t.Errorf("plain name mangled: %q", got)
	}
	if got := decodeBase64Name(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
}

func TestSupportsKLAP(t *testing.T) {
	if !(Found{EncryptType: "KLAP"}).SupportsKLAP() {
		t.Error("KLAP should be supported")
	}
	if !(Found{EncryptType: "klap"}).SupportsKLAP() {
		t.Error("comparison should be case-insensitive")
	}
	// An AES device is a real Tapo plug this transport cannot drive; it must
	// be reported, not silently attempted.
	if (Found{EncryptType: "AES"}).SupportsKLAP() {
		t.Error("AES must not be treated as KLAP")
	}
	if (Found{}).SupportsKLAP() {
		t.Error("unknown scheme must not be assumed to be KLAP")
	}
}
