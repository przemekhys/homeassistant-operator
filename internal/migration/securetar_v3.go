package migration

import (
	"archive/tar"
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	blake2b "github.com/minio/blake2b-simd"
	"github.com/openziti/secretstream"
	"golang.org/x/crypto/argon2"
)

// authenticateSecureTarV3 verifies every ciphertext chunk of the single HA
// payload. This experimental primitive does not validate the decrypted gzip
// or tar, authenticate outer metadata, or prove which backup produced it. It
// must not by itself be used as evidence for BackupReady.
func authenticateSecureTarV3(r io.Reader, password []byte) error {
	invalid := errors.New("stored archive authentication failed")
	if len(password) == 0 {
		return invalid
	}
	t := tar.NewReader(r)
	found := false
	for {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name != "homeassistant.tar" && name != "homeassistant.tar.gz" {
			continue
		}
		if found || h.Typeflag != tar.TypeReg || h.Size < 153 {
			return invalid
		}
		found = true
		payload, err := newSecureTarV3Reader(t, h.Size, password)
		if err != nil {
			return invalid
		}
		if _, err := io.Copy(io.Discard, payload); err != nil {
			return invalid
		}
	}
	if !found {
		return invalid
	}
	return nil
}

var errSecureTarAuthentication = errors.New("stored archive authentication failed")

type secureTarV3Reader struct {
	source    io.Reader
	stream    secretstream.Decryptor
	remaining uint64
	buffer    []byte
}

func newSecureTarV3Reader(source io.Reader, entrySize int64, password []byte) (*secureTarV3Reader, error) {
	if source == nil || len(password) == 0 || entrySize < 153 {
		return nil, errSecureTarAuthentication
	}
	var header [136]byte
	magic := []byte{'S', 'e', 'c', 'u', 'r', 'e', 'T', 'a', 'r', 3, 0, 0, 0, 0, 0, 0}
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return nil, errSecureTarAuthentication
	}
	if !bytes.Equal(header[:16], magic) {
		if bytes.Equal(header[:9], []byte("SecureTar")) && header[9] != 3 {
			return nil, errUnsupportedArchiveProfile
		}
		return nil, errSecureTarAuthentication
	}
	size := binary.BigEndian.Uint64(header[16:24])
	if size == 0 || size > uint64(entrySize-136) || binary.BigEndian.Uint64(header[24:32]) != 0 {
		return nil, errSecureTarAuthentication
	}
	chunks := (size-1)/(1<<20) + 1
	if size+chunks*17 != uint64(entrySize-136) {
		return nil, errSecureTarAuthentication
	}
	root := argon2.IDKey(password, header[32:48], 8, 16*1024, 1, 32)
	derive := func(salt []byte) []byte {
		hash, _ := blake2b.New(&blake2b.Config{Size: 32, Key: root, Salt: salt, Person: []byte("SecureTarv3")})
		return hash.Sum(nil)
	}
	if subtle.ConstantTimeCompare(derive(header[48:64]), header[64:96]) != 1 {
		return nil, errSecureTarAuthentication
	}
	stream, err := secretstream.NewDecryptor(derive(header[96:112]), header[112:136])
	if err != nil {
		return nil, errSecureTarAuthentication
	}
	return &secureTarV3Reader{source: source, stream: stream, remaining: size}, nil
}

func (r *secureTarV3Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.buffer) == 0 {
		if r.remaining == 0 {
			return 0, io.EOF
		}
		n := min(r.remaining, 1<<20)
		ciphertext := make([]byte, int(n)+17)
		if _, err := io.ReadFull(r.source, ciphertext); err != nil {
			return 0, errSecureTarAuthentication
		}
		plain, tag, err := r.stream.Pull(ciphertext)
		if err != nil || len(plain) != int(n) {
			return 0, errSecureTarAuthentication
		}
		r.remaining -= n
		if (r.remaining == 0 && tag != secretstream.TagFinal) || (r.remaining != 0 && tag != secretstream.TagMessage) {
			return 0, errSecureTarAuthentication
		}
		r.buffer = plain
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}
