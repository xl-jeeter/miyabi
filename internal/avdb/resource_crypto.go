package avdb

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

// AVDB publishes its online resource library as an AES-256-GCM encrypted zip:
//
//	<package>.zip
//	  ├── avdb-resource-library.json   manifest (kdf params, salt, nonce, tag)
//	  └── avdb-resource-library.bin    ciphertext of the real zip
//
// The AES key is derived from fixed key material baked into AVDB itself
// (utils/resource_zip_crypto.py:_RESOURCE_LIBRARY_PASSWORD_DIGEST), not from a
// user passphrase, so any AVDB-compatible consumer can open the package.
const (
	resourceLibraryManifestName = "avdb-resource-library.json"
	resourceLibraryFormat       = "avdb-resource-library"

	resourceLibraryPasswordDigestHex = "ca42e687df5818e2e88da0ff5b9fd2c60f7e22721f682b66c3e50485a00d06d5"
	resourceLibraryDefaultIterations = 200000
	resourceLibraryKeyLength         = 32
)

type resourceLibraryManifest struct {
	Format           string `json:"format"`
	Version          int    `json:"version"`
	Algorithm        string `json:"algorithm"`
	KDF              string `json:"kdf"`
	Iterations       int    `json:"iterations"`
	Salt             string `json:"salt"`
	Nonce            string `json:"nonce"`
	Tag              string `json:"tag"`
	Payload          string `json:"payload"`
	OriginalFilename string `json:"original_filename"`
}

// decryptResourceLibrary reports whether the archive is an encrypted resource
// library and, when it is, returns the decrypted inner zip together with the
// inner file name to hand to ImportZip.
//
// A plaintext AVDB export returns encrypted=false so callers can keep importing
// it directly.
func decryptResourceLibrary(reader io.ReaderAt, size int64) (plain []byte, innerName string, encrypted bool, err error) {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, "", false, nil
	}

	var manifestFile *zip.File
	for _, file := range archive.File {
		if file.Name == resourceLibraryManifestName {
			manifestFile = file
			break
		}
	}
	if manifestFile == nil {
		return nil, "", false, nil
	}

	manifestBytes, err := readZipFile(manifestFile)
	if err != nil {
		return nil, "", true, fmt.Errorf("读取资源库清单失败: %w", err)
	}
	var manifest resourceLibraryManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, "", true, fmt.Errorf("解析资源库清单失败: %w", err)
	}
	if manifest.Format != resourceLibraryFormat {
		return nil, "", false, nil
	}
	if manifest.Algorithm != "AES-256-GCM" || manifest.KDF != "PBKDF2-HMAC-SHA256" {
		return nil, "", true, fmt.Errorf("不支持的加密方式: %s / %s", manifest.Algorithm, manifest.KDF)
	}

	payloadName := manifest.Payload
	if payloadName == "" {
		payloadName = "avdb-resource-library.bin"
	}
	var payloadFile *zip.File
	for _, file := range archive.File {
		if file.Name == payloadName {
			payloadFile = file
			break
		}
	}
	if payloadFile == nil {
		return nil, "", true, fmt.Errorf("资源库包缺少载荷 %s", payloadName)
	}
	payload, err := readZipFile(payloadFile)
	if err != nil {
		return nil, "", true, fmt.Errorf("读取资源库载荷失败: %w", err)
	}

	salt, err := base64.StdEncoding.DecodeString(manifest.Salt)
	if err != nil {
		return nil, "", true, fmt.Errorf("资源库盐值无效: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(manifest.Nonce)
	if err != nil {
		return nil, "", true, fmt.Errorf("资源库 nonce 无效: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(manifest.Tag)
	if err != nil {
		return nil, "", true, fmt.Errorf("资源库校验标签无效: %w", err)
	}
	digest, err := hex.DecodeString(resourceLibraryPasswordDigestHex)
	if err != nil {
		return nil, "", true, fmt.Errorf("内置密钥无效: %w", err)
	}

	iterations := manifest.Iterations
	if iterations <= 0 {
		iterations = resourceLibraryDefaultIterations
	}
	key := pbkdf2.Key(digest, salt, iterations, resourceLibraryKeyLength, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", true, fmt.Errorf("初始化解密失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", true, fmt.Errorf("初始化 GCM 失败: %w", err)
	}

	sealed := make([]byte, 0, len(payload)+len(tag))
	sealed = append(sealed, payload...)
	sealed = append(sealed, tag...)
	plain, err = gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, "", true, fmt.Errorf("资源库解密失败（密钥或密文不匹配）: %w", err)
	}

	innerName = manifest.OriginalFilename
	if innerName == "" {
		innerName = "avdb-resource-library.zip"
	}
	return plain, innerName, true, nil
}

func readZipFile(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}

// newReaderAt buffers the source so both the outer zip check and the plain
// fallback path can read it without seeking the original stream.
func newReaderAt(data []byte) io.ReaderAt { return bytes.NewReader(data) }
