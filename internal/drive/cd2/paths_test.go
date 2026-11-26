package cd2

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"
	"github.com/ppxb/miyabi/internal/pan"
)

func TestPathIDsRoundTrip(t *testing.T) {
	for _, test := range []struct{ name, wantID string }{
		{name: "/", wantID: RootID},
		{name: "", wantID: RootID},
		{name: "/智勤", wantID: "L-aZuuWLpA"},
		{name: "/智勤/2021年前", wantID: "L-aZuuWLpC8yMDIx5bm05YmN"},
		{name: "/智勤/2024-05-14 80529.jpg", wantID: "L-aZuuWLpC8yMDI0LTA1LTE0IDgwNTI5LmpwZw"},
		{name: "/with space/dir name/file nfo.nfo", wantID: "L3dpdGggc3BhY2UvZGlyIG5hbWUvZmlsZSBuZm8ubmZv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := EncodePath(test.name)
			if id != test.wantID {
				t.Fatalf("EncodePath(%q) = %q, want %q", test.name, id, test.wantID)
			}
			decoded, err := DecodePath(id)
			if err != nil {
				t.Fatalf("DecodePath(%q) error = %v", id, err)
			}
			if want := CleanPath(test.name); decoded != want {
				t.Fatalf("DecodePath(EncodePath(%q)) = %q, want %q", test.name, decoded, want)
			}
		})
	}
}

func TestCleanPathPreservesCaseAndSpaces(t *testing.T) {
	for _, test := range []struct{ in, want string }{
		{"/智勤/", "/智勤"},
		{"智勤", "/智勤"},
		{"/A/B/../B", "/A/B"},
		{"//A//B//", "/A/B"},
		{"/A/./B/", "/A/B"},
		{"/ Mixed Case / File .txt", "/ Mixed Case / File .txt"},
		{"/尾随空格 ", "/尾随空格 "},
	} {
		if got := CleanPath(test.in); got != test.want {
			t.Errorf("CleanPath(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestDecodePathRejectsForeignIDs(t *testing.T) {
	for _, id := range []string{
		"",                    // empty
		"1184404884787683448", // CloudDrive2 internal ID
		"{115open-80005515}0", // CloudDrive2 cloud root ID
		"!!!",                 // invalid base64
		"YWJj",                // decodes to a relative path
		"d2ViL2ZpbGUubXA0",    // decodes to a relative path
	} {
		if _, err := DecodePath(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("DecodePath(%q) error = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestPathDirectoriesAnchorsAtRoot(t *testing.T) {
	for _, test := range []struct {
		name string
		want []pan.Directory
	}{
		{name: "/", want: []pan.Directory{{ID: RootID}}},
		{name: "/智勤", want: []pan.Directory{
			{ID: RootID},
			{ID: "L-aZuuWLpA", Name: "智勤"},
		}},
		{name: "/智勤/2021年前/video.mp4", want: []pan.Directory{
			{ID: RootID},
			{ID: "L-aZuuWLpA", Name: "智勤"},
			{ID: "L-aZuuWLpC8yMDIx5bm05YmN", Name: "2021年前"},
			{ID: EncodePath("/智勤/2021年前/video.mp4"), Name: "video.mp4"},
		}},
	} {
		if got := pathDirectories(test.name); !reflect.DeepEqual(got, test.want) {
			t.Errorf("pathDirectories(%q) = %#v, want %#v", test.name, got, test.want)
		}
	}
}

func TestPageOfSlicesListing(t *testing.T) {
	files := make([]*gen.CloudDriveFile, 5)
	for i := range files {
		files[i] = entry(string(rune('a'+i)), "/"+string(rune('a'+i)), false, 1)
	}
	for _, test := range []struct {
		name          string
		offset, limit int
		want          []string
	}{
		{name: "first page", offset: 0, limit: 2, want: []string{"a", "b"}},
		{name: "partial last page", offset: 4, limit: 2, want: []string{"e"}},
		{name: "offset at end", offset: 5, limit: 2},
		{name: "offset past end", offset: 99, limit: 2},
		{name: "negative offset", offset: -1, limit: 2},
		{name: "zero limit", offset: 0, limit: 0},
		{name: "negative limit", offset: 0, limit: -3},
		{name: "page larger than listing", offset: 0, limit: 100, want: []string{"a", "b", "c", "d", "e"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := pageOf(files, test.offset, test.limit)
			names := make([]string, len(got))
			for i, file := range got {
				names[i] = file.GetName()
			}
			if len(names) == 0 {
				names = nil
			}
			if !reflect.DeepEqual(names, test.want) {
				t.Fatalf("pageOf(offset=%d, limit=%d) = %v, want %v", test.offset, test.limit, names, test.want)
			}
		})
	}
}

// timeAgo returns a deadline that has already passed.
func timeAgo() time.Time { return time.Now().Add(-time.Second) }

func TestListingCacheExpiryAndBounds(t *testing.T) {
	var cache listingCache
	files := []*gen.CloudDriveFile{entry("a", "/a", false, 1)}
	cache.put("/dir", files)
	if got, ok := cache.get("/dir"); !ok || len(got) != 1 {
		t.Fatalf("cached listing = %v, %t", got, ok)
	}

	// Expiry is checked on read, so an aged entry is dropped instead of served.
	cache.mu.Lock()
	cache.entries["/dir"] = listing{files: files, expires: timeAgo()}
	cache.mu.Unlock()
	if _, ok := cache.get("/dir"); ok {
		t.Fatal("expired listing was served from the cache")
	}

	for i := range listingLimit + 10 {
		cache.put("/dir"+string(rune('A'+i%26))+string(rune('a'+i/26)), files)
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size > listingLimit {
		t.Fatalf("cache holds %d listings, want at most %d", size, listingLimit)
	}
}

func TestListingCacheLookupResolvesParentEntries(t *testing.T) {
	var cache listingCache
	file := entry("movie.mp4", "/智勤/2021年前/movie.mp4", false, 10)
	cache.put("/智勤/2021年前", []*gen.CloudDriveFile{file})

	if got, ok := cache.lookup("/智勤/2021年前/movie.mp4"); !ok || got != file {
		t.Fatalf("lookup hit = %v, %t", got, ok)
	}
	for _, name := range []string{"/智勤/2021年前/missing.mp4", "/other/movie.mp4", "/", "/智勤"} {
		if _, ok := cache.lookup(name); ok {
			t.Errorf("lookup(%q) unexpectedly hit", name)
		}
	}
}
