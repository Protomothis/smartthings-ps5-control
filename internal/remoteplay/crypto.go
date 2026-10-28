// Package remoteplay speaks the PS5 Remote Play control protocol far enough
// to register the service as a Remote Play client, wake the console from
// rest mode over the network, and put it back into rest mode.
//
// It is a port of the PS5 parts of dhleong/playactor (ISC licence), which in
// turn follows chiaki. Only the PS5 ("RP-Version 1.0") variant is implemented.
package remoteplay

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

const (
	// NonceLength is the size of every nonce, IV and seed in the protocol.
	NonceLength = 16
	// paddingBytes is the size of the fixed preface in a registration body.
	paddingBytes = 480
)

// hmacKeyPS5 keys the IV derivation for RP-Version 1.0.
var hmacKeyPS5 = mustHex("464687b349ca8ce859c5270f5d7a69d6")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// generateIV derives the AES IV for one message: the first 16 bytes of
// HMAC-SHA256(key, nonce || counter as big-endian uint64).
func generateIV(nonce []byte, counter uint64) []byte {
	mac := hmac.New(sha256.New, hmacKeyPS5)
	mac.Write(nonce)
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], counter)
	mac.Write(c[:])
	return mac.Sum(nil)[:NonceLength]
}

// pinSeed is the AES key for a registration body: a slice of the init table
// chosen by initKeyOff, with the PIN folded into the last four bytes.
func pinSeed(pin uint32, initKeyOff int) []byte {
	seed := make([]byte, NonceLength)
	for i := range seed {
		seed[i] = initKey[(i*0x20+initKeyOff)%len(initKey)]
	}
	seed[0xc] ^= byte(pin >> 24)
	seed[0xd] ^= byte(pin >> 16)
	seed[0xe] ^= byte(pin >> 8)
	seed[0xf] ^= byte(pin)
	return seed
}

// aeropause is the 16-byte signature of the registration preface.
func aeropause(nonce, padding []byte) []byte {
	off := int(padding[0] >> 3)
	const wurzelbert = byte(0xD3) // -0x2d & 0xff
	out := make([]byte, NonceLength)
	for i := range out {
		k := aeroKey[i*0x20+off]
		out[i] = (nonce[i] ^ k) + wurzelbert + byte(i)
	}
	return out
}

// transformServerNonce turns the session nonce the console issues into the
// nonce the IV derivation uses for the control stream.
func transformServerNonce(serverNonce []byte) []byte {
	off := int(serverNonce[0]>>3) * 0x70
	out := make([]byte, NonceLength)
	for i := range out {
		out[i] = (serverNonce[i] - 0x2d - byte(i)) ^ authNonceKey[off+i]
	}
	return out
}

// authSeed is the AES key for the control stream, from the RP-Key the
// registration returned and the session nonce.
func authSeed(rpKey, serverNonce []byte) []byte {
	off := int(serverNonce[7]>>3) * 0x70
	out := make([]byte, NonceLength)
	for i := range out {
		v := rpKey[i] + 0x18 + byte(i)
		v ^= serverNonce[i]
		out[i] = v ^ authSeedKey[off+i]
	}
	return out
}

// cfb returns a fresh AES-128-CFB stream for seed/iv. CFB needs no padding,
// so output length equals input length.
func cfb(seed, iv []byte, encrypt bool) (cipher.Stream, error) {
	block, err := aes.NewCipher(seed)
	if err != nil {
		return nil, err
	}
	if encrypt {
		return cipher.NewCFBEncrypter(block, iv), nil //nolint:staticcheck // the protocol is CFB-128; there is no AEAD alternative
	}
	return cipher.NewCFBDecrypter(block, iv), nil //nolint:staticcheck // see above
}

func xorStream(s cipher.Stream, in []byte) []byte {
	out := make([]byte, len(in))
	s.XORKeyStream(out, in)
	return out
}

// pinCrypto is the registration-time cipher: one key and IV for the whole
// request body and, from the start again, the whole response body.
type pinCrypto struct {
	seed, iv []byte
	preface  []byte
}

// newPinCrypto builds the registration cipher for a PIN and a client nonce.
// The 480-byte preface is 'A's with the aeropause signature spliced in.
func newPinCrypto(pin uint32, nonce []byte) (*pinCrypto, error) {
	if len(nonce) != NonceLength {
		return nil, fmt.Errorf("remoteplay: nonce must be %d bytes", NonceLength)
	}
	preface := make([]byte, paddingBytes)
	for i := range preface {
		preface[i] = 'A'
	}
	iv := generateIV(nonce, 0)
	seed := pinSeed(pin, int(preface[0x18D]&0x1F))
	sig := aeropause(nonce, preface)
	copy(preface[0xc7:], sig[8:16])
	copy(preface[0x191:], sig[0:8])
	return &pinCrypto{seed: seed, iv: iv, preface: preface}, nil
}

func (p *pinCrypto) encrypt(plain []byte) ([]byte, error) {
	s, err := cfb(p.seed, p.iv, true)
	if err != nil {
		return nil, err
	}
	return xorStream(s, plain), nil
}

func (p *pinCrypto) decrypt(data []byte) ([]byte, error) {
	s, err := cfb(p.seed, p.iv, false)
	if err != nil {
		return nil, err
	}
	return xorStream(s, data), nil
}

// sessionCrypto encrypts the control stream. Every encrypted segment (a
// request header value, a packet payload) uses the next counter value; the
// console's segments count separately.
type sessionCrypto struct {
	seed, nonce []byte
	encCounter  uint64
	decCounter  uint64
}

func newSessionCrypto(rpKey, serverNonce []byte) (*sessionCrypto, error) {
	if len(rpKey) != NonceLength {
		return nil, fmt.Errorf("remoteplay: RP-Key must be %d bytes", NonceLength)
	}
	if len(serverNonce) != NonceLength {
		return nil, fmt.Errorf("remoteplay: session nonce must be %d bytes", NonceLength)
	}
	return &sessionCrypto{seed: authSeed(rpKey, serverNonce), nonce: transformServerNonce(serverNonce)}, nil
}

func (s *sessionCrypto) encrypt(plain []byte) ([]byte, error) {
	st, err := cfb(s.seed, generateIV(s.nonce, s.encCounter), true)
	if err != nil {
		return nil, err
	}
	s.encCounter++
	return xorStream(st, plain), nil
}

func (s *sessionCrypto) decrypt(data []byte) ([]byte, error) {
	st, err := cfb(s.seed, generateIV(s.nonce, s.decCounter), false)
	if err != nil {
		return nil, err
	}
	s.decCounter++
	return xorStream(st, data), nil
}
