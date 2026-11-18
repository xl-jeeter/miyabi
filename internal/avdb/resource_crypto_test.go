package avdb

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

// buildEncryptedLibrary mirrors what AVDB publishes: an outer zip holding a
// manifest plus the AES-256-GCM ciphertext of the real (CSV-carrying) zip.
func buildEncryptedLibrary(t *testing.T, csv string) []byte {
	t.Helper()

	inner := &bytes.Buffer{}
	innerZip := zip.NewWriter(inner)
	writer, err := innerZip.Create("All_X1080X_1_2026-11-18-04-00-00.csv")
	if err != nil {
		t.Fatalf("create inner csv: %v", err)
	}
	if _, err := writer.Write([]byte(csv)); err != nil {
		t.Fatalf("write inner csv: %v", err)
	}
	if err := innerZip.Close(); err != nil {
		t.Fatalf("close inner zip: %v", err)
	}

	digest, err := hex.DecodeString(resourceLibraryPasswordDigestHex)
	if err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("salt: %v", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}

	key := pbkdf2.Key(digest, salt, resourceLibraryDefaultIterations, resourceLibraryKeyLength, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	sealed := gcm.Seal(nil, nonce, inner.Bytes(), nil)
	ciphertext := sealed[:len(sealed)-gcm.Overhead()]
	tag := sealed[len(sealed)-gcm.Overhead():]

	manifest, err := json.Marshal(resourceLibraryManifest{
		Format:           resourceLibraryFormat,
		Version:          1,
		Algorithm:        "AES-256-GCM",
		KDF:              "PBKDF2-HMAC-SHA256",
		Iterations:       resourceLibraryDefaultIterations,
		Salt:             base64.StdEncoding.EncodeToString(salt),
		Nonce:            base64.StdEncoding.EncodeToString(nonce),
		Tag:              base64.StdEncoding.EncodeToString(tag),
		Payload:          "avdb-resource-library.bin",
		OriginalFilename: "All_X1080X_1_2026-11-18-04-00-00.zip",
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	outer := &bytes.Buffer{}
	outerZip := zip.NewWriter(outer)
	for name, payload := range map[string][]byte{
		resourceLibraryManifestName: manifest,
		"avdb-resource-library.bin": ciphertext,
	} {
		entry, err := outerZip.Create(name)
		if err != nil {
			t.Fatalf("create outer entry: %v", err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatalf("write outer entry: %v", err)
		}
	}
	if err := outerZip.Close(); err != nil {
		t.Fatalf("close outer zip: %v", err)
	}
	return outer.Bytes()
}

func TestDecryptResourceLibraryRoundTrip(t *testing.T) {
	csv := "tid,number,title\n1,ABC-001,样例\n"
	archive := buildEncryptedLibrary(t, csv)

	plain, innerName, encrypted, err := decryptResourceLibrary(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !encrypted {
		t.Fatal("expected encrypted package to be detected")
	}
	if innerName != "All_X1080X_1_2026-11-18-04-00-00.zip" {
		t.Fatalf("unexpected inner name: %s", innerName)
	}

	inner, err := zip.NewReader(bytes.NewReader(plain), int64(len(plain)))
	if err != nil {
		t.Fatalf("open decrypted zip: %v", err)
	}
	reader, err := inner.File[0].Open()
	if err != nil {
		t.Fatalf("open decrypted csv: %v", err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read decrypted csv: %v", err)
	}
	if string(got) != csv {
		t.Fatalf("csv mismatch: %q", string(got))
	}
}

func TestDecryptResourceLibraryIgnoresPlainExport(t *testing.T) {
	buffer := &bytes.Buffer{}
	archive := zip.NewWriter(buffer)
	entry, err := archive.Create("export.csv")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := entry.Write([]byte("tid,number\n1,A-1\n")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, _, encrypted, err := decryptResourceLibrary(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encrypted {
		t.Fatal("plain export must not be treated as encrypted")
	}
}
