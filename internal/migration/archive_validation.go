package migration

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"path"
	"strings"

	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
)

var errInvalidArchiveStructure = errors.New("archive structure is invalid or exceeds limits")
var errArchiveValidationLimit = errors.New("ArchiveValidationLimitExceeded")
var errUnsupportedArchiveProfile = errors.New("UnsupportedArchiveProfile")

// archiveMetadataExpectation contains only fields independently observed at
// the source. This helper does not establish the trustworthiness of those
// observations; callers must persist and bind the source evidence separately.
type archiveMetadataExpectation struct {
	Slug            string
	IncludeDatabase bool
	Date            string
}

// validateArchiveProfile validates a bounded profile, hashing exactly the
// input bytes consumed. It must not be used alone to authorize BackupReady.
func validateArchiveProfile(
	ctx context.Context, src io.Reader, password []byte, expected archiveMetadataExpectation,
) (string, int64, error) {
	if ctx == nil || src == nil || len(password) == 0 || expected.Slug == "" {
		return "", 0, errInvalidArchiveStructure
	}
	hash := sha256.New()
	count := &countingReader{reader: io.TeeReader(&contextReader{ctx: ctx, reader: src}, hash)}
	outer := tar.NewReader(count)
	var metadataFound, payloadFound bool
	for {
		before := count.n
		h, err := outer.Next()
		if err == io.EOF {
			if count.n-before < 1024 {
				return "", 0, errInvalidArchiveStructure
			}
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || !validTarExtensions(h) {
			return "", 0, errInvalidArchiveStructure
		}
		switch h.Name {
		case "./backup.json", "backup.json":
			if h.Size > 1<<20 {
				return "", 0, errArchiveValidationLimit
			}
			if metadataFound || h.Size <= 0 {
				return "", 0, errInvalidArchiveStructure
			}
			metadataFound = true
			data, err := io.ReadAll(outer)
			if err != nil {
				return "", 0, errInvalidArchiveStructure
			}
			if err := validateArchiveMetadata(data, expected); err != nil {
				return "", 0, err
			}
		case "homeassistant.tar.gz":
			if payloadFound {
				return "", 0, errInvalidArchiveStructure
			}
			payloadFound = true
			payload, err := newSecureTarV3Reader(outer, h.Size, password)
			if err != nil {
				if errors.Is(err, errUnsupportedArchiveProfile) {
					return "", 0, err
				}
				return "", 0, errInvalidArchiveStructure
			}
			if err := validateBackupPayload(payload, 64<<30, 1000000); err != nil {
				return "", 0, err
			}
		default:
			return "", 0, errInvalidArchiveStructure
		}
	}
	if !metadataFound || !payloadFound {
		return "", 0, errInvalidArchiveStructure
	}
	if drainZeroPadding(count) != nil {
		return "", 0, errInvalidArchiveStructure
	}
	if count.n%512 != 0 || count.trailingZeros < 1024 {
		return "", 0, errInvalidArchiveStructure
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), count.n, nil
}

func archiveMetadataMatches(data []byte, expected archiveMetadataExpectation) bool {
	return validateArchiveMetadata(data, expected) == nil
}

func validateArchiveMetadata(data []byte, expected archiveMetadataExpectation) error {
	var m struct {
		Slug          string `json:"slug"`
		Version       int    `json:"version"`
		Type          string `json:"type"`
		Compressed    *bool  `json:"compressed"`
		Protected     *bool  `json:"protected"`
		Date          string `json:"date"`
		Name          string `json:"name"`
		HomeAssistant struct {
			Version         string `json:"version"`
			ExcludeDatabase *bool  `json:"exclude_database"`
		} `json:"homeassistant"`
		Extra struct {
			Automatic  *bool  `json:"with_automatic_settings"`
			InstanceID string `json:"instance_id"`
		} `json:"extra"`
	}
	if migrationprotocol.Decode(data, &m) != nil {
		return errInvalidArchiveStructure
	}
	if m.Version != 2 || m.HomeAssistant.Version != "2026.11.0" {
		return errUnsupportedArchiveProfile
	}
	if (expected.Date != "" && m.Date != expected.Date) || m.Slug != expected.Slug || m.Type != "partial" ||
		m.Compressed == nil || !*m.Compressed || m.Protected == nil || !*m.Protected ||
		m.HomeAssistant.ExcludeDatabase == nil || *m.HomeAssistant.ExcludeDatabase == expected.IncludeDatabase ||
		m.Extra.Automatic == nil || !*m.Extra.Automatic {
		return errInvalidArchiveStructure
	}
	return nil
}

type countingReader struct {
	reader        io.Reader
	n             int64
	trailingZeros int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.n += int64(n)
	for _, b := range p[:n] {
		if b == 0 {
			r.trailingZeros++
		} else {
			r.trailingZeros = 0
		}
	}
	return n, err
}

// validateBackupPayload checks a decrypted gzip/TAR stream without extracting
// it. It is only one layer of archive verification, not proof of provenance,
// successful authentication, or permission to mark a backup ready.
func validateBackupPayload(src io.Reader, maxBytes int64, maxEntries int) error {
	if src == nil || maxBytes <= 0 || maxBytes == math.MaxInt64 || maxEntries <= 0 {
		return errInvalidArchiveStructure
	}
	buffered := bufio.NewReader(src)
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return errInvalidArchiveStructure
	}
	defer func() { _ = gz.Close() }()
	gz.Multistream(false)
	limited := &io.LimitedReader{R: gz, N: maxBytes + 1}
	count := &countingReader{reader: limited}
	tr := tar.NewReader(count)
	seen := make(map[string]struct{})
	var nameBytes int64
	for {
		before := count.n
		h, err := tr.Next()
		if err == io.EOF {
			if count.n-before < 1024 {
				return errInvalidArchiveStructure
			}
			break
		}
		if limited.N <= 0 || len(seen) >= maxEntries && err != io.EOF {
			return errArchiveValidationLimit
		}
		if err != nil {
			return errInvalidArchiveStructure
		}
		name := strings.TrimSuffix(h.Name, "/")
		nameBytes += int64(len(name))
		if len(name) > 4096 || nameBytes > 64<<20 {
			return errArchiveValidationLimit
		}
		if !validPayloadHeader(h, name) {
			return errInvalidArchiveStructure
		}
		if _, ok := seen[name]; ok {
			return errInvalidArchiveStructure
		}
		seen[name] = struct{}{}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			if limited.N <= 0 {
				return errArchiveValidationLimit
			}
			return errInvalidArchiveStructure
		}
	}
	// Drain the gzip stream so that truncation and CRC errors cannot be hidden
	// behind the TAR end marker. Nonzero bytes after the TAR are not accepted.
	if drainZeroPadding(count) != nil {
		return errInvalidArchiveStructure
	}
	if limited.N <= 0 {
		return errArchiveValidationLimit
	}
	if len(seen) == 0 || count.n%512 != 0 || count.trailingZeros < 1024 {
		return errInvalidArchiveStructure
	}
	if _, err := buffered.ReadByte(); err != io.EOF {
		return errInvalidArchiveStructure
	}
	return nil
}

func validPayloadHeader(h *tar.Header, name string) bool {
	if !validTarExtensions(h) {
		return false
	}
	clean := path.Clean(name)
	if h.Typeflag == tar.TypeDir && h.Size != 0 || clean == "." && h.Typeflag != tar.TypeDir {
		return false
	}
	return name != "" && !strings.HasPrefix(name, "/") && clean != ".." && !strings.HasPrefix(clean, "../") &&
		clean == name && (h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeDir)
}

func validTarExtensions(h *tar.Header) bool {
	if h.Format == tar.FormatGNU {
		return false
	}
	for key := range h.PAXRecords {
		switch key {
		case "path", "size", "mtime", "atime", "ctime":
		default:
			return false
		}
	}
	return true
}

func drainZeroPadding(r io.Reader) error {
	var buf [32768]byte
	for {
		n, err := r.Read(buf[:])
		for _, b := range buf[:n] {
			if b != 0 {
				return errInvalidArchiveStructure
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errInvalidArchiveStructure
		}
	}
}
