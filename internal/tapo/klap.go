// Package tapo speaks the local KLAP protocol used by TP-Link Tapo smart
// plugs (P100, P110 and kin), so plugs can be toggled over the LAN without
// the cloud API.
//
// This is KLAP v2 — the SMART.KLAP variant. The older IOT.KLAP v1 hashes with
// MD5 and concatenates its seeds differently; sending v1 hashes to a Tapo plug
// fails at handshake1 with no useful error. The two must not be mixed.
//
// Everything here is standard library. The only piece Go does not provide is
// PKCS#7 padding, which is a few lines at the bottom of this file.
package tapo

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// Port 80 for the KLAP endpoints; discovery lives on UDP 20002.
	httpPort = 80

	// sessionLifetime is how long a handshake is trusted before being redone.
	// Devices report their own TIMEOUT cookie (24h is typical) but their
	// clocks drift, so the session is retired early rather than discovering
	// the expiry through a failed toggle. python-kasa uses the same 20 minute
	// buffer against the reported timeout.
	sessionBuffer = 20 * time.Minute

	// A plug is a small embedded controller on the far side of a TCP
	// handshake. These are deliberately short: one unplugged device must not
	// hold up a status sweep of the others.
	dialTimeout    = 500 * time.Millisecond
	requestTimeout = 3 * time.Second
)

// Credentials are the TP-Link account the plugs were provisioned with. They
// are hashed locally and never sent anywhere but the plug itself.
type Credentials struct {
	Username string // the account email
	Password string
}

// authHash is the KLAP v2 identity hash: sha256(sha1(user) + sha1(pass)).
//
// KLAP v1 uses md5(md5(user) + md5(pass)) instead. Tapo plugs are v2.
func (c Credentials) authHash() []byte {
	u := sha1.Sum([]byte(c.Username))
	p := sha1.Sum([]byte(c.Password))
	sum := sha256.Sum256(append(u[:], p[:]...))
	return sum[:]
}

// session is one negotiated KLAP conversation with a single plug.
type session struct {
	cookie  string
	key     []byte // AES-128
	ivBase  []byte // 12 bytes; the sequence number supplies the last 4
	sig     []byte // 28 bytes, prefixed to every request body
	seq     int32
	expires time.Time
}

// Device is a single plug. It is safe for concurrent use: KLAP carries a
// sequence number that is also the IV tail, so requests must be serialised or
// the cipher stream desynchronises.
type Device struct {
	IP    string
	Creds Credentials

	mu   sync.Mutex
	sess *session
	http *http.Client
}

// New returns a device handle. No network traffic happens until the first
// call; the handshake is lazy so constructing a plug for every slot is cheap.
func New(ip string, creds Credentials) *Device {
	return &Device{
		IP:    strings.TrimSpace(ip),
		Creds: creds,
		http: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
				MaxIdleConnsPerHost: 1,
				DisableCompression:  true,
			},
			// KLAP tracks its session with a cookie we set by hand. Following
			// a redirect would drop it, and a plug never legitimately issues
			// one.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (d *Device) url(path string) string {
	return fmt.Sprintf("http://%s:%d%s", d.IP, httpPort, path)
}

// handshake performs handshake1 and handshake2 and installs a fresh session.
// Caller must hold d.mu.
func (d *Device) handshake() error {
	if d.IP == "" {
		return fmt.Errorf("tapo: no address")
	}
	auth := d.Creds.authHash()

	localSeed := make([]byte, 16)
	if _, err := rand.Read(localSeed); err != nil {
		return fmt.Errorf("tapo: seed: %w", err)
	}

	// --- handshake1: body is the raw local seed; the reply carries the
	// device's own seed plus a hash proving it holds the same credentials.
	req, err := http.NewRequest(http.MethodPost, d.url("/app/handshake1"), bytes.NewReader(localSeed))
	if err != nil {
		return err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("tapo: handshake1 %s: %w", d.IP, err)
	}
	body, err := readAllClose(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tapo: handshake1 %s: status %d", d.IP, resp.StatusCode)
	}
	if len(body) < 48 {
		return fmt.Errorf("tapo: handshake1 %s: short reply (%d bytes)", d.IP, len(body))
	}
	remoteSeed, serverHash := body[:16], body[16:48]

	// v2 order: local + remote + auth. (v1 omits remoteSeed entirely.)
	want := sha256.Sum256(concat(localSeed, remoteSeed, auth))
	if !bytes.Equal(serverHash, want[:]) {
		return fmt.Errorf("tapo: %s rejected the credentials (check TAPO_EMAIL / TAPO_PASSWORD)", d.IP)
	}

	// The session cookie rides on every later request. The device also sends
	// a TIMEOUT cookie that it will not accept back, so only TP_SESSIONID is
	// kept — a stock cookie jar would return both and break the session.
	cookie, timeout := sessionCookie(resp)
	if cookie == "" {
		return fmt.Errorf("tapo: handshake1 %s: no session cookie", d.IP)
	}

	// --- handshake2: prove we hold the credentials too. Note the seeds swap
	// order relative to handshake1.
	h2 := sha256.Sum256(concat(remoteSeed, localSeed, auth))
	req2, err := http.NewRequest(http.MethodPost, d.url("/app/handshake2"), bytes.NewReader(h2[:]))
	if err != nil {
		return err
	}
	req2.Header.Set("Cookie", cookie)
	resp2, err := d.http.Do(req2)
	if err != nil {
		return fmt.Errorf("tapo: handshake2 %s: %w", d.IP, err)
	}
	if _, err := readAllClose(resp2); err != nil {
		return err
	}
	if resp2.StatusCode != http.StatusOK {
		return fmt.Errorf("tapo: handshake2 %s: status %d", d.IP, resp2.StatusCode)
	}

	d.sess = newSession(cookie, localSeed, remoteSeed, auth, timeout)
	return nil
}

// newSession derives the cipher material from the two seeds and the auth hash.
// Each value is a SHA256 over the same payload with a different ASCII prefix.
func newSession(cookie string, local, remote, auth []byte, timeout time.Duration) *session {
	key := sha256.Sum256(concat([]byte("lsk"), local, remote, auth))
	ivf := sha256.Sum256(concat([]byte("iv"), local, remote, auth))
	sig := sha256.Sum256(concat([]byte("ldk"), local, remote, auth))

	// The trailing 4 bytes of the iv material are the starting sequence
	// number, and it is *signed*: a device can legitimately hand back a
	// negative seq that counts up toward zero.
	seq := int32(binary.BigEndian.Uint32(ivf[28:32]))

	life := timeout - sessionBuffer
	if life <= 0 {
		life = sessionBuffer
	}
	return &session{
		cookie:  cookie,
		key:     key[:16],
		ivBase:  ivf[:12],
		sig:     sig[:28],
		seq:     seq,
		expires: time.Now().Add(life),
	}
}

// request sends one JSON payload inside the encrypted envelope and returns the
// decrypted reply. A 403 means the session lapsed, so the handshake is redone
// and the call replayed once — sessions outlive a day, so this fires during
// ordinary use rather than only at edges.
func (d *Device) request(payload []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.sess == nil || time.Now().After(d.sess.expires) {
		if err := d.handshake(); err != nil {
			return nil, err
		}
	}
	out, status, err := d.send(payload)
	if err == nil && status != http.StatusForbidden {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	// Stale session: renegotiate and try the payload once more.
	d.sess = nil
	if err := d.handshake(); err != nil {
		return nil, err
	}
	out, status, err = d.send(payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("tapo: %s: status %d after re-handshake", d.IP, status)
	}
	return out, nil
}

// send performs one encrypted round trip. Caller must hold d.mu and have a
// live session.
func (d *Device) send(payload []byte) ([]byte, int, error) {
	s := d.sess
	s.seq++
	seq := s.seq

	ct, err := encrypt(s.key, s.ivBase, seq, payload)
	if err != nil {
		return nil, 0, err
	}
	// body = sha256(sig + seq + ciphertext) || ciphertext
	mac := sha256.Sum256(concat(s.sig, be32(seq), ct))
	body := append(mac[:], ct...)

	url := fmt.Sprintf("%s?seq=%d", d.url("/app/request"), seq)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Cookie", s.cookie)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("tapo: request %s: %w", d.IP, err)
	}
	raw, err := readAllClose(resp)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	if len(raw) <= 32 {
		return nil, resp.StatusCode, fmt.Errorf("tapo: %s: short response", d.IP)
	}
	plain, err := decrypt(s.key, s.ivBase, seq, raw[32:])
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return plain, resp.StatusCode, nil
}

// iv is the 12-byte base with the sequence number as its final 4 bytes, so
// every request encrypts under a different IV.
func iv(base []byte, seq int32) []byte {
	return concat(base, be32(seq))
}

func encrypt(key, ivBase []byte, seq int32, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv(ivBase, seq)).CryptBlocks(out, padded)
	return out, nil
}

func decrypt(key, ivBase []byte, seq int32, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ct)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("tapo: ciphertext is not a whole number of blocks")
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv(ivBase, seq)).CryptBlocks(out, ct)
	return pkcs7Unpad(out, block.BlockSize())
}

// --- wire payloads -------------------------------------------------------

type apiRequest struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
	// Spelled "milis" on the wire. That is the protocol's own typo, not ours.
	RequestTimeMillis int64 `json:"request_time_milis"`
}

type apiResponse struct {
	ErrorCode int             `json:"error_code"`
	Result    json.RawMessage `json:"result"`
}

// Status is what a plug reports about itself.
type Status struct {
	On    bool
	Name  string // decoded from the base64 nickname, when present
	Model string
}

type deviceInfo struct {
	DeviceOn bool   `json:"device_on"`
	Nickname string `json:"nickname"`
	Model    string `json:"model"`
}

func (d *Device) call(method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(apiRequest{
		Method:            method,
		Params:            params,
		RequestTimeMillis: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	raw, err := d.request(body)
	if err != nil {
		return nil, err
	}
	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("tapo: %s: bad reply: %w", d.IP, err)
	}
	if out.ErrorCode != 0 {
		return nil, fmt.Errorf("tapo: %s: %s failed (error_code %d)", d.IP, method, out.ErrorCode)
	}
	return out.Result, nil
}

// SetOn switches the plug.
func (d *Device) SetOn(on bool) error {
	_, err := d.call("set_device_info", map[string]any{"device_on": on})
	return err
}

// Status queries the plug's current state.
func (d *Device) Status() (Status, error) {
	raw, err := d.call("get_device_info", nil)
	if err != nil {
		return Status{}, err
	}
	var info deviceInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return Status{}, fmt.Errorf("tapo: %s: bad device info: %w", d.IP, err)
	}
	return Status{
		On:    info.DeviceOn,
		Name:  decodeBase64Name(info.Nickname),
		Model: info.Model,
	}, nil
}

// Close drops the session. The next call re-handshakes.
func (d *Device) Close() {
	d.mu.Lock()
	d.sess = nil
	d.mu.Unlock()
	d.http.CloseIdleConnections()
}

// --- helpers -------------------------------------------------------------

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func be32(v int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b
}

// sessionCookie pulls TP_SESSIONID out of a handshake reply, along with the
// device's advertised TIMEOUT. The TIMEOUT cookie is deliberately not echoed
// back: devices reject their own.
func sessionCookie(resp *http.Response) (string, time.Duration) {
	timeout := 24 * time.Hour
	var id string
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "TP_SESSIONID":
			id = c.Name + "=" + c.Value
		case "TIMEOUT":
			if secs := parseInt(c.Value); secs > 0 {
				timeout = time.Duration(secs) * time.Second
			}
		}
	}
	return id, timeout
}

func parseInt(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func readAllClose(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	// A plug's replies are small; the cap keeps a confused device from
	// growing the heap without bound.
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte, size int) ([]byte, error) {
	if len(b) == 0 || len(b)%size != 0 {
		return nil, fmt.Errorf("tapo: bad padding length")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > size || n > len(b) {
		return nil, fmt.Errorf("tapo: bad padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, fmt.Errorf("tapo: bad padding")
		}
	}
	return b[:len(b)-n], nil
}
