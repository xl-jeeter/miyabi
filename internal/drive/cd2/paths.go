package cd2

import (
	"encoding/base64"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"
	"github.com/ppxb/miyabi/internal/pan"
)

// RootID identifies the virtual root. CloudDrive2 has no numeric root ID, but
// the mount dialog, file walkers and the 115 client all use "0" for the root,
// so the adapter keeps that contract.
const RootID = "0"

const (
	// listingTTL bounds how long one directory listing is reused. It only saves
	// repeated GetSubFiles calls, so short staleness is acceptable.
	listingTTL = 30 * time.Second
	// listingLimit bounds the number of cached directories, not their size.
	listingLimit = 128
)

// CleanPath normalizes an absolute CloudDrive2 path: forward slashes, no
// trailing separator and no "." or ".." segments. Case and whitespace are
// preserved because CloudDrive2 paths are case sensitive and file names may
// legitimately start or end with spaces.
func CleanPath(name string) string {
	return path.Clean("/" + name)
}

// EncodePath derives the stateless file ID of an absolute path. CloudDrive2
// addresses files by path only, so IDs are reversible encodings of that path
// instead of CloudDrive2's internal IDs, which cannot be resolved back to a
// path. Renaming or moving a file therefore changes its ID; a rescan converges.
func EncodePath(name string) string {
	cleaned := CleanPath(name)
	if cleaned == "/" {
		return RootID
	}
	return base64.RawURLEncoding.EncodeToString([]byte(cleaned))
}

// DecodePath maps a file ID back to an absolute CloudDrive2 path.
func DecodePath(id string) (string, error) {
	if id == RootID {
		return "/", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(decoded) == 0 || decoded[0] != '/' {
		return "", ErrInvalidID
	}
	return CleanPath(string(decoded)), nil
}

// filePath returns the absolute path of an entry. CloudDrive2 fills
// fullPathName for listed entries; the fallback keeps locally built values
// (such as the root) usable.
func filePath(file *gen.CloudDriveFile) string {
	if file.GetFullPathName() != "" {
		return CleanPath(file.GetFullPathName())
	}
	return CleanPath(file.GetName())
}

// pathDirectories returns the ancestor chain of name, root first, in the shape
// miyabi expects from pan.FilePage.Path and pan.FileInfo.Path.
func pathDirectories(name string) []pan.Directory {
	cleaned := CleanPath(name)
	if cleaned == "/" {
		return []pan.Directory{{ID: RootID}}
	}
	segments := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	directories := make([]pan.Directory, 0, len(segments)+1)
	directories = append(directories, pan.Directory{ID: RootID})
	current := ""
	for _, segment := range segments {
		current += "/" + segment
		directories = append(directories, pan.Directory{ID: EncodePath(current), Name: segment})
	}
	return directories
}

// pageOf applies miyabi's offset/limit pagination to a complete listing.
// A non-positive limit yields an empty page, so a caller bug surfaces as an
// incomplete traversal instead of an endless one.
func pageOf(files []*gen.CloudDriveFile, offset, limit int) []*gen.CloudDriveFile {
	if limit <= 0 || offset < 0 || offset >= len(files) {
		return nil
	}
	return files[offset:min(offset+limit, len(files))]
}

// listingCache keeps recent directory listings so browsing a folder or
// resolving many files from one folder does not repeat GetSubFiles.
type listingCache struct {
	mu      sync.Mutex
	entries map[string]listing
}

type listing struct {
	files   []*gen.CloudDriveFile
	expires time.Time
}

func (c *listingCache) get(name string) ([]*gen.CloudDriveFile, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[name]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(entry.expires) {
		delete(c.entries, name)
		return nil, false
	}
	return entry.files, true
}

func (c *listingCache) put(name string, files []*gen.CloudDriveFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]listing)
	}
	now := time.Now()
	var oldest string
	var deadline time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
			continue
		}
		if deadline.IsZero() || entry.expires.Before(deadline) {
			oldest, deadline = key, entry.expires
		}
	}
	if len(c.entries) >= listingLimit && oldest != "" {
		delete(c.entries, oldest)
	}
	c.entries[name] = listing{files: files, expires: now.Add(listingTTL)}
}

// lookup resolves one entry from its parent's cached listing.
func (c *listingCache) lookup(name string) (*gen.CloudDriveFile, bool) {
	cleaned := CleanPath(name)
	parent, base := path.Split(cleaned)
	if base == "" {
		return nil, false
	}
	files, ok := c.get(CleanPath(parent))
	if !ok {
		return nil, false
	}
	for _, file := range files {
		if file.GetName() == base {
			return file, true
		}
	}
	return nil, false
}
