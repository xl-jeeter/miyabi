package cd2

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testHash = "7e9de8e62ffe9ab8f0c753b8d48fe82ff2837540"

// offlineDrive is a Drive pinned to a single fake cloud account.
func offlineDrive(t *testing.T, api *fakeAPI) *Drive {
	t.Helper()
	if api.root == nil {
		api.root = []*gen.CloudDriveFile{accountEntry("115open", "80005515", "甲", "/甲")}
	}
	drive := testDrive(t, api)
	drive.account = "115open:80005515"
	return drive
}

func TestInfoHashOfAcceptsHexAndBase32(t *testing.T) {
	raw, err := hex.DecodeString(testHash)
	if err != nil {
		t.Fatal(err)
	}
	base32Hash := strings.ToLower(base32.StdEncoding.EncodeToString(raw))
	for _, test := range []struct{ name, magnet string }{
		{name: "hex", magnet: "magnet:?xt=urn:btih:" + testHash + "&dn=video&tag=AVdb"},
		{name: "upper hex", magnet: "magnet:?xt=urn:btih:" + strings.ToUpper(testHash)},
		{name: "base32", magnet: "magnet:?xt=urn:btih:" + base32Hash},
		{name: "second exact topic", magnet: "magnet:?xt=urn:sha1:abc&xt=urn:btih:" + testHash},
	} {
		t.Run(test.name, func(t *testing.T) {
			hash, err := infoHashOf(test.magnet)
			if err != nil || hash != testHash {
				t.Fatalf("infoHashOf = %q, %v; want %q", hash, err, testHash)
			}
		})
	}

	for _, test := range []struct{ name, magnet string }{
		{name: "http URL", magnet: "https://example/movie.mp4"},
		{name: "no btih", magnet: "magnet:?dn=video"},
		{name: "short hash", magnet: "magnet:?xt=urn:btih:deadbeef"},
		{name: "non hex", magnet: "magnet:?xt=urn:btih:" + strings.Repeat("z", 40)},
		{name: "empty", magnet: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := infoHashOf(test.magnet); !errors.Is(err, ErrInvalidInfoHash) {
				t.Fatalf("infoHashOf(%q) error = %v, want ErrInvalidInfoHash", test.magnet, err)
			}
		})
	}
}

func TestAddOfflineSubmitsToDecodedFolder(t *testing.T) {
	api := &fakeAPI{}
	drive := offlineDrive(t, api)
	magnet := "magnet:?xt=urn:btih:" + testHash + "&dn=FC2-PPV-4964987"

	hash, err := drive.AddOffline(t.Context(), "ignored", magnet, EncodePath("/智勤/2021年前"))
	if err != nil {
		t.Fatalf("AddOffline error = %v", err)
	}
	if hash != testHash {
		t.Fatalf("AddOffline hash = %q, want %q", hash, testHash)
	}
	want := []string{"AddOfflineFiles " + magnet + " -> /智勤/2021年前"}
	if got := api.recordOf(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	// The submission destination is remembered for duplicate detection.
	if folder := drive.destinations.get(testHash); folder != EncodePath("/智勤/2021年前") {
		t.Fatalf("remembered destination = %q", folder)
	}

	if _, err := drive.AddOffline(t.Context(), "", magnet, "internal-id"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("AddOffline with a foreign folder error = %v, want ErrInvalidID", err)
	}
}

func TestAddOfflineReportsCloudDriveFailures(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + testHash
	for _, test := range []struct {
		name string
		api  *fakeAPI
		want string
	}{
		{name: "unsuccessful result", api: &fakeAPI{addOffline: &gen.FileOperationResult{
			Success: false, ErrorMessage: "no offline quota",
		}}, want: "no offline quota"},
		{name: "missing folder", api: &fakeAPI{addOfflineErr: notFound(`find by path: "/missing" not found under "/"`)},
			want: "CloudDrive2 中找不到该文件"},
	} {
		t.Run(test.name, func(t *testing.T) {
			drive := offlineDrive(t, test.api)
			_, err := drive.AddOffline(t.Context(), "", magnet, EncodePath("/missing"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AddOffline error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRemoveOfflineScopesToTheAccountCloud(t *testing.T) {
	api := &fakeAPI{}
	drive := offlineDrive(t, api)
	if err := drive.RemoveOffline(t.Context(), "", strings.ToUpper(testHash)); err != nil {
		t.Fatalf("RemoveOffline error = %v", err)
	}
	// The first call discovers the account from the CloudDrive2 root; later
	// calls reuse the cached account instead of listing again.
	want := []string{"GetSubFiles /", "RemoveOfflineFiles 115open:80005515 " + testHash}
	if got := api.recordOf(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if err := drive.RemoveOffline(t.Context(), "", testHash); err != nil {
		t.Fatalf("RemoveOffline error = %v", err)
	}
	if got := api.recordOf(); len(got) != 3 {
		t.Fatalf("account discovery repeated: calls = %v", got)
	}

	rejected := offlineDrive(t, &fakeAPI{removeOffline: &gen.FileOperationResult{
		Success: false, ErrorMessage: "task not found",
	}})
	if err := rejected.RemoveOffline(t.Context(), "", testHash); err == nil || !strings.Contains(err.Error(), "task not found") {
		t.Fatalf("RemoveOffline error = %v", err)
	}
}

func TestOfflineTasksMapsCloudDriveStatuses(t *testing.T) {
	api := &fakeAPI{offlinePages: map[uint32]*gen.OfflineFileListAllResult{
		1: {
			PageNo: 1, PageCount: 2, TotalCount: 4,
			OfflineFiles: []*gen.OfflineFile{
				{InfoHash: strings.ToUpper(testHash), Status: gen.OfflineFileStatus_OFFLINE_FINISHED, PercendDone: 100},
				{InfoHash: "b", Status: gen.OfflineFileStatus_OFFLINE_DOWNLOADING, PercendDone: 42.5},
				{InfoHash: "c", Status: gen.OfflineFileStatus_OFFLINE_ERROR, PercendDone: 0},
				{InfoHash: "d", Status: gen.OfflineFileStatus_OFFLINE_INIT, PercendDone: -3},
			},
		},
	}}
	drive := offlineDrive(t, api)
	if _, err := drive.resolveAccount(t.Context()); err != nil {
		t.Fatalf("resolveAccount error = %v", err)
	}
	page, err := drive.OfflineTasks(t.Context(), "", 1)
	if err != nil {
		t.Fatalf("OfflineTasks error = %v", err)
	}
	if page.PageCount != 2 || len(page.Tasks) != 4 {
		t.Fatalf("page = %+v", page)
	}
	wantStatuses := []int{offlineDone, offlineDownloading, offlineFailed, offlineWaiting}
	wantProgress := []int{100, 42, 0, 0}
	for i, task := range page.Tasks {
		if task.Status != wantStatuses[i] || task.Progress != wantProgress[i] {
			t.Fatalf("task %d = %+v, want status %d progress %d", i, task, wantStatuses[i], wantProgress[i])
		}
		if task.DirectoryID != "" || task.FileID != "" {
			t.Fatalf("task %d exposes an unusable CloudDrive2 location: %+v", i, task)
		}
	}
	if page.Tasks[0].Hash != testHash {
		t.Fatalf("hash was not lowercased: %q", page.Tasks[0].Hash)
	}
	if page.Tasks[1].RawProgress != 42.5 {
		t.Fatalf("raw progress = %v, want 42.5", page.Tasks[1].RawProgress)
	}

	want := []string{"GetSubFiles /", "ListAllOfflineFiles 115open:80005515 page 1"}
	if got := api.recordOf(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	empty, err := drive.OfflineTasks(t.Context(), "", 2)
	if err != nil || len(empty.Tasks) != 0 {
		t.Fatalf("page 2 = %+v, error = %v", empty, err)
	}
}

func TestOfflineTasksReportsCloudDriveFailures(t *testing.T) {
	drive := offlineDrive(t, &fakeAPI{offlineErr: status.Error(codes.Unauthenticated, "No valid auth token")})
	_, err := drive.OfflineTasks(t.Context(), "", 1)
	if err == nil || !strings.Contains(err.Error(), "No valid auth token") {
		t.Fatalf("OfflineTasks error = %v", err)
	}
}

func TestOfflineTasksRequiresKnownAccount(t *testing.T) {
	// Two accounts and no configuration must fail instead of guessing.
	drive := testDrive(t, &fakeAPI{root: []*gen.CloudDriveFile{
		accountEntry("115open", "1", "甲", "/甲"),
		accountEntry("115open", "2", "乙", "/乙"),
	}})
	if _, err := drive.OfflineTasks(t.Context(), "", 1); !errors.Is(err, ErrAccountRequired) {
		t.Fatalf("OfflineTasks error = %v, want ErrAccountRequired", err)
	}
	if err := drive.RemoveOffline(t.Context(), "", testHash); !errors.Is(err, ErrAccountRequired) {
		t.Fatalf("RemoveOffline error = %v, want ErrAccountRequired", err)
	}
}

func TestOfflineTaskProgressStaysWithinBounds(t *testing.T) {
	api := &fakeAPI{offlinePages: map[uint32]*gen.OfflineFileListAllResult{
		1: {PageNo: 1, PageCount: 1, OfflineFiles: []*gen.OfflineFile{
			{InfoHash: "a", Status: gen.OfflineFileStatus_OFFLINE_DOWNLOADING, PercendDone: 250},
			{InfoHash: "b", Status: gen.OfflineFileStatus_OFFLINE_UNKNOWN, PercendDone: 12},
		}},
	}}
	page, err := offlineDrive(t, api).OfflineTasks(t.Context(), "", 1)
	if err != nil {
		t.Fatalf("OfflineTasks error = %v", err)
	}
	if page.Tasks[0].Progress != 100 || page.Tasks[1].Status != offlineWaiting {
		t.Fatalf("tasks = %+v", page.Tasks)
	}
}
