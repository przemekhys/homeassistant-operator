package migration

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"

	blake2b "github.com/minio/blake2b-simd"
	"github.com/openziti/secretstream"
	"golang.org/x/crypto/argon2"
)

func secureTarFixture(t *testing.T, password []byte) []byte {
	return encryptSecureTarFixture(t, password, make([]byte, (1<<20)+10))
}

func encryptSecureTarFixture(t *testing.T, password, plaintext []byte) []byte {
	return encryptSecureTarFixtureWithFinalTag(t, password, plaintext, secretstream.TagFinal)
}

func encryptSecureTarFixtureWithFinalTag(t *testing.T, password, plaintext []byte, finalTag byte) []byte {
	t.Helper()
	header := make([]byte, 136)
	copy(header, "SecureTar\x03")
	size := len(plaintext)
	binary.BigEndian.PutUint64(header[16:24], uint64(size))
	copy(header[32:48], "0123456789abcdef")
	copy(header[48:64], "validation-salt!")
	copy(header[96:112], "derivation-salt!")
	root := argon2.IDKey(password, header[32:48], 8, 16*1024, 1, 32)
	derive := func(salt []byte) []byte {
		h, err := blake2b.New(&blake2b.Config{Size: 32, Key: root, Salt: salt, Person: []byte("SecureTarv3")})
		if err != nil {
			t.Fatal(err)
		}
		return h.Sum(nil)
	}
	copy(header[64:96], derive(header[48:64]))
	stream, nonce, err := secretstream.NewEncryptor(derive(header[96:112]))
	if err != nil {
		t.Fatal(err)
	}
	copy(header[112:], nonce)
	payload := header
	for len(plaintext) > 0 {
		n := min(len(plaintext), 1<<20)
		tag := byte(secretstream.TagMessage)
		if n == len(plaintext) {
			tag = finalTag
		}
		chunk, err := stream.Push(plaintext[:n], tag)
		if err != nil {
			t.Fatal(err)
		}
		payload = append(payload, chunk...)
		plaintext = plaintext[n:]
	}
	var result bytes.Buffer
	w := tar.NewWriter(&result)
	if err := w.WriteHeader(&tar.Header{Name: "homeassistant.tar.gz", Mode: 0600, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestSecureTarV3Authentication(t *testing.T) {
	password := []byte("synthetic-secret")
	fixture := secureTarFixture(t, password)
	if err := authenticateSecureTarV3(bytes.NewReader(fixture), password); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		data            []byte
		key             []byte
		missingFinalTag bool
	}{
		{name: "wrong key", data: fixture, key: []byte("wrong")},
		{name: "truncated", data: fixture[:len(fixture)/2], key: password},
		{name: "header", data: append([]byte(nil), fixture...), key: password},
		{name: "first chunk", data: append([]byte(nil), fixture...), key: password},
		{name: "last chunk", data: append([]byte(nil), fixture...), key: password},
		{name: "reserved header", data: append([]byte(nil), fixture...), key: password},
		{name: "missing final tag", key: password, missingFinalTag: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.missingFinalTag {
				tc.data = encryptSecureTarFixtureWithFinalTag(t, password, make([]byte, (1<<20)+10), secretstream.TagMessage)
			}
			switch tc.name {
			case "header":
				tc.data[512] ^= 1
			case "first chunk":
				tc.data[512+136+5] ^= 1
			case "last chunk":
				tc.data[512+136+(1<<20)+17+5] ^= 1
			case "reserved header":
				tc.data[512+24] ^= 1
			}
			err := authenticateSecureTarV3(bytes.NewReader(tc.data), tc.key)
			if err == nil || strings.Contains(err.Error(), string(password)) {
				t.Fatal("invalid archive accepted or secret exposed")
			}
		})
	}
}

func TestSecureTarV3ReaderPreservesAuthenticatedPlaintext(t *testing.T) {
	password := []byte("synthetic-secret")
	fixture := secureTarFixture(t, password)
	tr := tar.NewReader(bytes.NewReader(fixture))
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	r, err := newSecureTarV3Reader(tr, h.Size, password)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buf := make([]byte, 7777)
	if _, err := io.CopyBuffer(&out, r, buf); err != nil {
		t.Fatal(err)
	}
	if out.Len() != (1<<20)+10 || !bytes.Equal(out.Bytes(), make([]byte, out.Len())) {
		t.Fatal("decrypted plaintext differs")
	}
}

func TestSecureTarV3DisposableHAArchive(t *testing.T) {
	path := os.Getenv("HA_MIGRATION_ARCHIVE_FIXTURE")
	if path == "" {
		t.Skip("requires disposable HA archive")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := authenticateSecureTarV3(f, []byte("migration-disposable-backup-key")); err != nil {
		t.Fatal(err)
	}
}

func TestSecureTarV3DisposableHAPayload(t *testing.T) {
	path := os.Getenv("HA_MIGRATION_ARCHIVE_FIXTURE")
	if path == "" {
		t.Skip("requires disposable HA archive")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatal("secure payload unavailable", err)
		}
		if h.Name != "homeassistant.tar.gz" {
			continue
		}
		payload, err := newSecureTarV3Reader(tr, h.Size, []byte("migration-disposable-backup-key"))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateBackupPayload(payload, 64<<30, 1000000); err != nil {
			t.Fatal(err)
		}
		return
	}
}
