package migration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

func profileFixture(t *testing.T, password, plaintext []byte, metadata map[string]any, extra ...tar.Header) []byte {
	t.Helper()
	encrypted := tar.NewReader(bytes.NewReader(encryptSecureTarFixture(t, password, plaintext)))
	h, err := encrypted.Next()
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, h.Size)
	if _, err := io.ReadFull(encrypted, payload); err != nil {
		t.Fatal(err)
	}
	if metadata == nil {
		metadata = profileMetadata()
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"./backup.json", b}, {"homeassistant.tar.gz", payload}} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	for i := range extra {
		if err := tw.WriteHeader(&extra[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func profileMetadata() map[string]any {
	return map[string]any{
		"slug": "backup-id", "version": 2, "type": "partial", "compressed": true, "protected": true,
		"date": "2026-11-15T22:18:00Z", "name": "test",
		"homeassistant": map[string]any{"version": "2026.11.0", "exclude_database": false},
		"extra":         map[string]any{"instance_id": "disposable", "with_automatic_settings": true},
	}
}

func TestArchiveProfileSynthetic(t *testing.T) {
	key := []byte("private-synthetic-key")
	plain := payloadFixture(t, tar.Header{Name: "data", Mode: 0600, Size: 32})
	valid := profileFixture(t, key, plain, nil)
	unsupportedCrypto := append([]byte(nil), valid...)
	cryptoOffset := bytes.Index(unsupportedCrypto, []byte("SecureTar\x03"))
	if cryptoOffset < 0 {
		t.Fatal("fixture payload missing")
	}
	unsupportedCrypto[cryptoOffset+9] = 4
	foreign := profileMetadata()
	foreign["slug"] = "foreign"
	unsupported := profileMetadata()
	unsupported["homeassistant"] = map[string]any{"version": "2026.10.4", "exclude_database": false}
	unknown := profileMetadata()
	unknown["unexpected"] = true
	crc := append([]byte(nil), plain...)
	crc[len(crc)-8] ^= 1
	var badTar bytes.Buffer
	gzipBadTar := gzip.NewWriter(&badTar)
	if _, err := gzipBadTar.Write([]byte("not a tar stream")); err != nil {
		t.Fatal(err)
	}
	if err := gzipBadTar.Close(); err != nil {
		t.Fatal(err)
	}
	link := tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}
	for _, tc := range []struct {
		name  string
		data  []byte
		key   []byte
		valid bool
	}{
		{"valid", valid, key, true},
		{"wrong key", valid, []byte("wrong"), false},
		{"unsupported crypto", unsupportedCrypto, key, false},
		{"foreign metadata", profileFixture(t, key, plain, foreign), key, false},
		{"unsupported version", profileFixture(t, key, plain, unsupported), key, false},
		{"unknown metadata", profileFixture(t, key, plain, unknown), key, false},
		{"trailing plaintext", profileFixture(t, key, append(append([]byte(nil), plain...), 1), nil), key, false},
		{"gzip CRC", profileFixture(t, key, crc, nil), key, false},
		{"not gzip", profileFixture(t, key, []byte("not gzip"), nil), key, false},
		{"invalid TAR", profileFixture(t, key, badTar.Bytes(), nil), key, false},
		{"duplicate payload", profileFixture(t, key, plain, nil, tar.Header{Name: "homeassistant.tar.gz"}), key, false},
		{"unknown outer", profileFixture(t, key, plain, nil, tar.Header{Name: "unknown"}), key, false},
		{"outer link", profileFixture(t, key, plain, nil, link), key, false},
		{"truncated", valid[:len(valid)/2], key, false},
		{"trailing data", append(append([]byte(nil), valid...), 1), key, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := archiveMetadataExpectation{Slug: "backup-id", IncludeDatabase: true}
			_, _, err := validateArchiveProfile(context.Background(), bytes.NewReader(tc.data), tc.key, expected)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestQualifiedMetadataFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/ha-container-2026.11.0-backup.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := archiveMetadataExpectation{Slug: "backup-id", IncludeDatabase: true, Date: "2026-11-15T22:18:00Z"}
	if !archiveMetadataMatches(raw, expected) {
		t.Fatal("qualified metadata fixture rejected")
	}
	duplicate := bytes.Replace(raw, []byte(`"version": 2`), []byte(`"version": 2, "version": 2`), 1)
	if archiveMetadataMatches(duplicate, expected) {
		t.Fatal("duplicate metadata field accepted")
	}
	expected.IncludeDatabase = false
	if archiveMetadataMatches(raw, expected) {
		t.Fatal("foreign database scope accepted")
	}
}

func payloadFixture(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for i := range headers {
		if err := tw.WriteHeader(&headers[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(make([]byte, headers[i].Size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArchiveProfileDisposableFixture(t *testing.T) {
	name := os.Getenv("HA_MIGRATION_ARCHIVE_FIXTURE")
	if name == "" {
		t.Skip("requires disposable HA archive")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("migration-disposable-backup-key")
	expected := archiveMetadataExpectation{Slug: "42dbd6cd", IncludeDatabase: true}
	digest, size, err := validateArchiveProfile(context.Background(), bytes.NewReader(data), key, expected)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if digest != want || size != int64(len(data)) {
		t.Fatalf("incorrect archive byte identity")
	}
	foreign := expected
	foreign.Slug = "foreign"
	if _, _, err := validateArchiveProfile(context.Background(), bytes.NewReader(data), key, foreign); err == nil {
		t.Fatal("foreign metadata accepted")
	}
	if _, _, err := validateArchiveProfile(context.Background(), bytes.NewReader(data), []byte("wrong"), expected); err == nil {
		t.Fatal("wrong key accepted")
	}
	truncated := bytes.NewReader(bytes.TrimRight(data, "\x00"))
	if _, _, err := validateArchiveProfile(context.Background(), truncated, key, expected); err == nil {
		t.Fatal("truncated archive accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := validateArchiveProfile(ctx, bytes.NewReader(data), key, expected); err == nil {
		t.Fatal("cancelled validation accepted")
	}
}

func TestValidateBackupPayload(t *testing.T) {
	file := tar.Header{Name: "config/settings.yaml", Mode: 0600, Size: 42}
	valid := payloadFixture(t, file)
	badCRC := append([]byte(nil), valid...)
	badCRC[len(badCRC)-8] ^= 1
	// A zero-filled final file must not masquerade as TAR end markers when
	// the stream ends after file data and omits the required terminator blocks.
	var truncatedTar bytes.Buffer
	zw := gzip.NewWriter(&truncatedTar)
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "data", Mode: 0600, Size: 2048}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	link := tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}
	xattr := tar.Header{Name: "data", Mode: 0600, PAXRecords: map[string]string{"SCHILY.xattr.user.test": "synthetic"}}
	for _, tc := range []struct {
		name    string
		data    []byte
		limit   int64
		entries int
		valid   bool
	}{
		{"valid", valid, 1 << 20, 5, true},
		{"crc", badCRC, 1 << 20, 5, false},
		{"missing TAR end marker", truncatedTar.Bytes(), 1 << 20, 5, false},
		{"truncated", valid[:len(valid)-4], 1 << 20, 5, false},
		{"size limit", valid, 100, 5, false},
		{"entry limit", payloadFixture(t, file, tar.Header{Name: "other", Mode: 0600}), 1 << 20, 1, false},
		{"duplicate", payloadFixture(t, file, file), 1 << 20, 5, false},
		{"escape", payloadFixture(t, tar.Header{Name: "../escape", Mode: 0600}), 1 << 20, 5, false},
		{"absolute", payloadFixture(t, tar.Header{Name: "/absolute", Mode: 0600}), 1 << 20, 5, false},
		{"symlink", payloadFixture(t, link), 1 << 20, 5, false},
		{"FIFO", payloadFixture(t, tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}), 1 << 20, 5, false},
		{"xattr", payloadFixture(t, xattr), 1 << 20, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBackupPayload(bytes.NewReader(tc.data), tc.limit, tc.entries)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
