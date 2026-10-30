package splithttp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	rxControlHeader     = "X-RX-Control"
	rxPayloadHashHeader = "X-RX-Payload-Hash" // internal; encrypted on the wire
	rxAuthSkew          = 120 * time.Second
	rxAuthMaxEnvelope   = 2048
	rxAuthWindow        = 1024
)

// The v1 handler only sees these headers after v2 authenticated decryption.
// Encoding in a fixed order avoids map iteration and HTTP canonicalization.
var rxAuthHeaders = [...]string{
	rxVersionHeader, rxTokenHeader, rxOffsetHeader, rxGenerationHeader,
	rxActionHeader, rxNonceHeader, rxEndHeader, rxDoneHeader, rxWindowHeader,
	rxUploadHeader, rxSeqHeader, rxSegmentHeader, rxSegmentEndHeader,
	rxParallelHeader, rxAckHeader, rxPayloadHashHeader,
}

func rxDecodeAuthKey(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("Xray-RX authKey must be a base64url-encoded random 32-byte key")
	}
	return key, nil
}

func rxVerifyPayload(expected string, payload []byte) bool {
	digest := sha256.Sum256(payload)
	decoded, err := hex.DecodeString(expected)
	return err == nil && subtle.ConstantTimeCompare(decoded, digest[:]) == 1
}

type rxAuthCodec struct {
	salt              [16]byte
	request, response cipher.AEAD
	next              atomic.Uint64
}

func newRXAuthCodec(key []byte, id string, salt [16]byte) (*rxAuthCodec, error) {
	c := &rxAuthCodec{salt: salt}
	for direction, destination := range map[string]*cipher.AEAD{"request": &c.request, "response": &c.response} {
		derived, err := hkdf.Key(sha256.New, key, salt[:], "xray-rx-v2\x00"+direction+"\x00"+id, 32)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(derived)
		if err != nil {
			return nil, err
		}
		if direction == "response" {
			// Replay state is intentionally volatile. A captured request may
			// reach a restarted server, so its response must not reuse the old
			// response nonce even though the request counter is unchanged.
			*destination, err = cipher.NewGCMWithRandomNonce(block)
		} else {
			*destination, err = cipher.NewGCM(block)
		}
		if err != nil {
			return nil, err
		}
	}
	return c, nil
}

func newRXClientAuth(value, id string) (*rxAuthCodec, error) {
	key, err := rxDecodeAuthKey(value)
	if err != nil || key == nil {
		return nil, err
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, err
	}
	return newRXAuthCodec(key, id, salt)
}

func rxAuthNonce(counter uint64) []byte {
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], counter)
	return nonce[:]
}

func rxAuthBinding(method, path, id, seq string, status int) []byte {
	return []byte(fmt.Sprintf("xray-rx-v2\x00%s\x00%s\x00%s\x00%s\x00%d", method, path, id, seq, status))
}

func rxAuthResponseBinding(method, path, id, seq string, status int, counter uint64) []byte {
	return binary.BigEndian.AppendUint64(rxAuthBinding(method, path, id, seq, status), counter)
}

func rxPackControl(headers http.Header, stamp int64) ([]byte, error) {
	data := binary.BigEndian.AppendUint64(nil, uint64(stamp))
	mask := uint16(0)
	data = append(data, 0, 0)
	for i, name := range rxAuthHeaders {
		values := headers.Values(name)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || len(values[0]) > 256 {
			return nil, errors.New("invalid RX control field")
		}
		mask |= 1 << i
		data = binary.AppendUvarint(data, uint64(len(values[0])))
		data = append(data, values[0]...)
	}
	binary.BigEndian.PutUint16(data[8:10], mask)
	return data, nil
}

func rxUnpackControl(data []byte) (http.Header, int64, error) {
	if len(data) < 10 {
		return nil, 0, errors.New("short RX control")
	}
	stamp := int64(binary.BigEndian.Uint64(data[:8]))
	mask := binary.BigEndian.Uint16(data[8:10])
	data = data[10:]
	headers := make(http.Header)
	for i, name := range rxAuthHeaders {
		if mask&(1<<i) == 0 {
			continue
		}
		length, n := binary.Uvarint(data)
		if n <= 0 || length > 256 || length > uint64(len(data)-n) {
			return nil, 0, errors.New("invalid RX control length")
		}
		data = data[n:]
		value := string(data[:length])
		data = data[length:]
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, 0, errors.New("invalid RX control value")
		}
		headers.Set(name, value)
	}
	if len(data) != 0 || headers.Get(rxVersionHeader) != "1" {
		return nil, 0, errors.New("invalid RX control encoding")
	}
	return headers, stamp, nil
}

func rxClearControl(headers http.Header) {
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-rx-") {
			delete(headers, name)
		}
	}
}

func rxWireControl(headers http.Header) (string, error) {
	for name, values := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-rx-") &&
			(!strings.EqualFold(name, rxVersionHeader) && !strings.EqualFold(name, rxControlHeader) || len(values) != 1) {
			return "", errors.New("mixed or duplicate RX control headers")
		}
	}
	value := headers.Get(rxControlHeader)
	if headers.Get(rxVersionHeader) != "2" || value == "" || len(value) > rxAuthMaxEnvelope {
		return "", errors.New("RX v2 authentication required")
	}
	return value, nil
}

func (c *rxAuthCodec) sealRequest(req *http.Request, id, seq string) (uint64, error) {
	counter := c.next.Add(1)
	if counter >= 1<<32 {
		return 0, errors.New("RX authentication request limit reached")
	}
	data, err := rxPackControl(req.Header, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	wire := append([]byte(nil), c.salt[:]...)
	wire = binary.BigEndian.AppendUint64(wire, counter)
	wire = c.request.Seal(wire, rxAuthNonce(counter), data, rxAuthBinding(req.Method, req.URL.EscapedPath(), id, seq, 0))
	rxClearControl(req.Header)
	req.Header.Set(rxVersionHeader, "2")
	req.Header.Set(rxControlHeader, base64.RawURLEncoding.EncodeToString(wire))
	return counter, nil
}

func (c *rxAuthCodec) openResponse(resp *http.Response, method, path, id, seq string, counter uint64) error {
	value, err := rxWireControl(resp.Header)
	if err != nil {
		return err
	}
	wire, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return errors.New("invalid RX response envelope")
	}
	data, err := c.response.Open(nil, nil, wire, rxAuthResponseBinding(method, path, id, seq, resp.StatusCode, counter))
	if err != nil {
		return errors.New("RX response authentication failed")
	}
	headers, _, err := rxUnpackControl(data)
	if err != nil {
		return err
	}
	rxClearControl(resp.Header)
	for name, values := range headers {
		resp.Header[name] = values
	}
	return nil
}

// A fixed bitmap tolerates reordered parallel GETs without a per-request map.
// Entries survive close/expiry until every captured request has become stale.
type rxAuthReplay struct {
	high    uint64
	bits    [rxAuthWindow / 64]uint64
	expires int64
}

func (r *rxAuthReplay) accept(counter uint64) bool {
	if counter == 0 || counter >= 1<<32 {
		return false
	}
	if counter > r.high {
		shift := counter - r.high
		if shift >= rxAuthWindow {
			clear(r.bits[:])
		} else {
			for i := len(r.bits) - 1; i >= 0; i-- {
				var value uint64
				source := i - int(shift/64)
				if source >= 0 {
					value = r.bits[source] << (shift % 64)
					if source > 0 && shift%64 != 0 {
						value |= r.bits[source-1] >> (64 - shift%64)
					}
				}
				r.bits[i] = value
			}
		}
		r.high = counter
	}
	index := r.high - counter
	if index >= rxAuthWindow {
		return false
	}
	word, bit := index/64, uint64(1)<<(index%64)
	if r.bits[word]&bit != 0 {
		return false
	}
	r.bits[word] |= bit
	return true
}

type rxAuthServer struct {
	key   []byte
	mu    sync.Mutex
	seen  map[string]*rxAuthReplay
	limit int
}

func (h *requestHandler) authenticateRX(w http.ResponseWriter, req *http.Request, id, seq string) (http.ResponseWriter, *http.Request, bool) {
	if !h.config.GetRx().GetEnabled() || h.config.GetRx().GetAuthKey() == "" {
		return w, req, true
	}
	h.rxAuthOnce.Do(func() {
		opts, err := h.config.normalizedRX()
		if err != nil {
			h.rxAuthErr = err
			return
		}
		key, err := rxDecodeAuthKey(opts.AuthKey)
		if err != nil {
			h.rxAuthErr = err
			return
		}
		h.rxAuth = &rxAuthServer{key: key, seen: make(map[string]*rxAuthReplay), limit: max(64, int(opts.MaxSessions)*4)}
	})
	refuse := func() (http.ResponseWriter, *http.Request, bool) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusForbidden)
		return w, req, false
	}
	if h.rxAuthErr != nil || id == "" || len(id) > 256 || len(seq) > 32 {
		return refuse()
	}
	value, err := rxWireControl(req.Header)
	if err != nil {
		return refuse()
	}
	wire, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(wire) < 24+16 {
		return refuse()
	}
	var salt [16]byte
	copy(salt[:], wire[:16])
	counter := binary.BigEndian.Uint64(wire[16:24])
	if counter == 0 || counter >= 1<<32 {
		return refuse()
	}
	codec, err := newRXAuthCodec(h.rxAuth.key, id, salt)
	if err != nil {
		return refuse()
	}
	data, err := codec.request.Open(nil, rxAuthNonce(counter), wire[24:], rxAuthBinding(req.Method, req.URL.EscapedPath(), id, seq, 0))
	if err != nil {
		return refuse()
	}
	headers, stamp, err := rxUnpackControl(data)
	if err != nil {
		return refuse()
	}
	now := time.Now().Unix()
	skew := int64(rxAuthSkew / time.Second)
	if stamp < now-skew || stamp > now+skew {
		return refuse()
	}
	if headers.Get(rxActionHeader) == "upload" && !validRXHex(headers.Get(rxPayloadHashHeader), sha256.Size) {
		return refuse()
	}
	cacheKey := id + "\x00" + string(salt[:])
	h.rxAuth.mu.Lock()
	entry := h.rxAuth.seen[cacheKey]
	if entry == nil {
		if len(h.rxAuth.seen) >= h.rxAuth.limit {
			for k, e := range h.rxAuth.seen {
				if e.expires < now {
					delete(h.rxAuth.seen, k)
				}
			}
		}
		if len(h.rxAuth.seen) >= h.rxAuth.limit {
			h.rxAuth.mu.Unlock()
			return refuse()
		}
		entry = &rxAuthReplay{}
		h.rxAuth.seen[cacheKey] = entry
	}
	accepted := entry.accept(counter)
	if accepted {
		entry.expires = max(entry.expires, stamp+skew)
	}
	h.rxAuth.mu.Unlock()
	if !accepted {
		return refuse()
	}
	// A fresh server-issued capability binds follow-up controls to this server
	// instance too. Replaying an old open after cache loss cannot authorize the
	// old upload/ACK/close records against the newly created session.
	openingToken := ""
	if headers.Get(rxActionHeader) == "open" {
		openingToken, err = rxRandomHex(32)
		if err != nil {
			return refuse()
		}
		headers.Set(rxTokenHeader, openingToken)
	}
	clone := req.Clone(req.Context())
	rxClearControl(clone.Header)
	for name, values := range headers {
		clone.Header[name] = values
	}
	return &rxAuthResponseWriter{ResponseWriter: w, codec: codec, method: req.Method, path: req.URL.EscapedPath(), id: id, seq: seq, counter: counter, openingToken: openingToken}, clone, true
}

type rxAuthResponseWriter struct {
	http.ResponseWriter
	codec                 *rxAuthCodec
	method, path, id, seq string
	counter               uint64
	wrote                 bool
	err                   error
	openingToken          string
}

func (w *rxAuthResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *rxAuthResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if status == http.StatusOK && w.openingToken != "" {
		w.Header().Set(rxTokenHeader, w.openingToken)
	}
	data, err := rxPackControl(w.Header(), time.Now().Unix())
	// Error responses may not set the internal version; they are still signed.
	if w.Header().Get(rxVersionHeader) == "" {
		w.Header().Set(rxVersionHeader, "1")
		data, err = rxPackControl(w.Header(), time.Now().Unix())
	}
	rxClearControl(w.Header())
	if err != nil {
		w.err = err
		w.ResponseWriter.WriteHeader(http.StatusInternalServerError)
		return
	}
	wire := w.codec.response.Seal(nil, nil, data, rxAuthResponseBinding(w.method, w.path, w.id, w.seq, status, w.counter))
	w.Header().Set(rxVersionHeader, "2")
	w.Header().Set(rxControlHeader, base64.RawURLEncoding.EncodeToString(wire))
	w.ResponseWriter.WriteHeader(status)
}
func (w *rxAuthResponseWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	return w.ResponseWriter.Write(data)
}
func (w *rxAuthResponseWriter) FlushError() error {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return w.err
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
