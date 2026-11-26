package cd2

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeAPI implements only the CloudDrive2 calls the adapter issues. Every other
// method of the embedded interface panics, which keeps the fake honest about
// which RPCs production code actually reaches for.
type fakeAPI struct {
	gen.CloudDriveFileSrvClient

	mu sync.Mutex

	token *gen.JWTToken
	// tokenErr fails GetToken instead of returning token.
	tokenErr error
	// root lists the CloudDrive2 root, including one entry per cloud account.
	root []*gen.CloudDriveFile
	// listings maps an absolute path to its directory entries.
	listings map[string][]*gen.CloudDriveFile
	// listingErr fails GetSubFiles for a path.
	listingErr map[string]error
	// finds maps "parent/base" to an entry returned by FindFileByPath.
	finds map[string]*gen.CloudDriveFile
	// downloads maps an absolute path to the GetDownloadUrlPath reply.
	downloads map[string]*gen.DownloadUrlPathInfo
	// spaces maps an absolute path to the GetSpaceInfo reply.
	spaces map[string]*gen.SpaceInfo
	// addOffline replies to AddOfflineFiles; a nil value means success.
	addOffline *gen.FileOperationResult
	// addOfflineErr fails AddOfflineFiles.
	addOfflineErr error
	// removeOffline replies to RemoveOfflineFiles; a nil value means success.
	removeOffline *gen.FileOperationResult
	// offlinePages maps a page number to a ListAllOfflineFiles reply.
	offlinePages map[uint32]*gen.OfflineFileListAllResult
	// offlineErr fails ListAllOfflineFiles.
	offlineErr error

	calls     []string
	authCalls []string
}

func (f *fakeAPI) record(call, authorization string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.authCalls = append(f.authCalls, authorization)
}

func bearer(ctx context.Context) string {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 0 {
		sent, _ := metadata.FromOutgoingContext(ctx)
		values = sent.Get("authorization")
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (f *fakeAPI) GetToken(ctx context.Context, request *gen.GetTokenRequest, _ ...grpc.CallOption) (*gen.JWTToken, error) {
	if f.tokenErr != nil {
		return nil, f.tokenErr
	}
	if request.GetUserName() != "miyabi" || request.GetPassword() != "secret" {
		return &gen.JWTToken{Success: false, ErrorMessage: "invalid credentials"}, nil
	}
	return f.token, nil
}

func (f *fakeAPI) GetSubFiles(ctx context.Context, request *gen.ListSubFileRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[gen.SubFilesReply], error) {
	f.record("GetSubFiles "+request.GetPath(), bearer(ctx))
	if err := f.listingErr[request.GetPath()]; err != nil {
		return nil, err
	}
	entries, ok := f.listings[request.GetPath()]
	if !ok {
		if request.GetPath() == "/" {
			entries = f.root
		} else {
			return nil, notFound("find by path: " + request.GetPath())
		}
	}
	return &fakeStream{replies: splitReplies(entries)}, nil
}

// splitReplies spreads entries over several stream messages so aggregation is
// exercised even for small fixtures.
func splitReplies(entries []*gen.CloudDriveFile) []*gen.SubFilesReply {
	switch {
	case len(entries) > 2:
		half := len(entries) / 2
		return []*gen.SubFilesReply{{SubFiles: entries[:half]}, {SubFiles: entries[half:]}}
	case len(entries) > 0:
		return []*gen.SubFilesReply{{SubFiles: entries[:1]}, {SubFiles: entries[1:]}}
	default:
		return []*gen.SubFilesReply{{}}
	}
}

func (f *fakeAPI) FindFileByPath(ctx context.Context, request *gen.FindFileByPathRequest, _ ...grpc.CallOption) (*gen.CloudDriveFile, error) {
	key := request.GetParentPath() + "/" + request.GetPath()
	f.record("FindFileByPath "+key, bearer(ctx))
	entry, ok := f.finds[key]
	if !ok {
		return nil, notFound("find by path: \"" + request.GetPath() + "\" not found under \"" + request.GetParentPath() + "\"")
	}
	return entry, nil
}

func (f *fakeAPI) GetDownloadUrlPath(ctx context.Context, request *gen.GetDownloadUrlPathRequest, _ ...grpc.CallOption) (*gen.DownloadUrlPathInfo, error) {
	f.record("GetDownloadUrlPath "+request.GetPath(), bearer(ctx))
	info, ok := f.downloads[request.GetPath()]
	if !ok {
		return nil, notFound("find by path: " + request.GetPath())
	}
	return info, nil
}

func (f *fakeAPI) GetSpaceInfo(ctx context.Context, request *gen.FileRequest, _ ...grpc.CallOption) (*gen.SpaceInfo, error) {
	f.record("GetSpaceInfo "+request.GetPath(), bearer(ctx))
	space, ok := f.spaces[request.GetPath()]
	if !ok {
		return nil, notFound("find by path: " + request.GetPath())
	}
	return space, nil
}

func (f *fakeAPI) AddOfflineFiles(ctx context.Context, request *gen.AddOfflineFileRequest, _ ...grpc.CallOption) (*gen.FileOperationResult, error) {
	f.record("AddOfflineFiles "+request.GetUrls()+" -> "+request.GetToFolder(), bearer(ctx))
	if f.addOfflineErr != nil {
		return nil, f.addOfflineErr
	}
	if f.addOffline != nil {
		return f.addOffline, nil
	}
	return &gen.FileOperationResult{Success: true}, nil
}

func (f *fakeAPI) RemoveOfflineFiles(ctx context.Context, request *gen.RemoveOfflineFilesRequest, _ ...grpc.CallOption) (*gen.FileOperationResult, error) {
	f.record("RemoveOfflineFiles "+request.GetCloudName()+":"+request.GetCloudAccountId()+" "+strings.Join(request.GetInfoHashes(), ","), bearer(ctx))
	if f.removeOffline != nil {
		return f.removeOffline, nil
	}
	return &gen.FileOperationResult{Success: true}, nil
}

func (f *fakeAPI) ListAllOfflineFiles(ctx context.Context, request *gen.OfflineFileListAllRequest, _ ...grpc.CallOption) (*gen.OfflineFileListAllResult, error) {
	f.record("ListAllOfflineFiles "+request.GetCloudName()+":"+request.GetCloudAccountId()+" page "+strconv.Itoa(int(request.GetPage())), bearer(ctx))
	if f.offlineErr != nil {
		return nil, f.offlineErr
	}
	if result, ok := f.offlinePages[request.GetPage()]; ok {
		return result, nil
	}
	return &gen.OfflineFileListAllResult{PageNo: request.GetPage(), PageCount: request.GetPage()}, nil
}

func (f *fakeAPI) recordOf() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeAPI) authRecordOf() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authCalls...)
}

// fakeStream replays canned replies and then reports io.EOF.
type fakeStream struct {
	grpc.ClientStream
	replies []*gen.SubFilesReply
	next    int
}

func (s *fakeStream) Recv() (*gen.SubFilesReply, error) {
	if s.next >= len(s.replies) {
		return nil, io.EOF
	}
	reply := s.replies[s.next]
	s.next++
	return reply, nil
}

func (s *fakeStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeStream) Trailer() metadata.MD         { return nil }
func (s *fakeStream) CloseSend() error             { return nil }
func (s *fakeStream) Context() context.Context     { return context.Background() }
func (s *fakeStream) SendMsg(any) error            { return nil }
func (s *fakeStream) RecvMsg(any) error            { return nil }

// testDrive builds a Drive wired to a fake API with a seeded API token, so
// tests never reach a real CloudDrive2 instance.
func testDrive(t *testing.T, api *fakeAPI) *Drive {
	t.Helper()
	c := &client{api: api, scheme: "http", host: "cd2.example:19798", media: http.DefaultClient}
	c.configureToken("fixture-token")
	t.Cleanup(c.close)
	return &Drive{client: c}
}

// entry builds a CloudDrive2 entry with the fields the adapter reads.
func entry(name, fullPath string, directory bool, size int64) *gen.CloudDriveFile {
	fileType := gen.CloudDriveFile_File
	if directory {
		fileType = gen.CloudDriveFile_Directory
	}
	return &gen.CloudDriveFile{
		Name: name, FullPathName: fullPath, Size: size, FileType: fileType,
		IsDirectory: directory, WriteTime: timestamppb.New(time.Unix(1700000000, 0)),
	}
}

// accountEntry builds a cloud root entry as CloudDrive2 reports it at "/".
func accountEntry(cloudName, userName, nickName, fullPath string) *gen.CloudDriveFile {
	result := entry(nickName, fullPath, true, 0)
	result.IsCloudRoot = true
	result.CloudAPI = &gen.CloudAPI{Name: cloudName, UserName: userName, NickName: nickName, Path: &fullPath}
	return result
}

func notFound(message string) error {
	return status.Error(codes.NotFound, message)
}
