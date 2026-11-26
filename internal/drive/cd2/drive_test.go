package cd2

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"
	"github.com/ppxb/miyabi/internal/pan"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestListPaginatesAndAnchorsPath(t *testing.T) {
	api := &fakeAPI{listings: map[string][]*gen.CloudDriveFile{
		"/智勤/2021年前": {
			entry("a.mp4", "/智勤/2021年前/a.mp4", false, 10),
			entry("b", "/智勤/2021年前/b", true, 0),
			entry("c.nfo", "/智勤/2021年前/c.nfo", false, 30),
		},
	}}
	drive := testDrive(t, api)

	page, err := drive.List(t.Context(), "anything", EncodePath("/智勤/2021年前"), 0, 2)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if page.Total != 3 || len(page.Files) != 2 || !page.HasMore {
		t.Fatalf("first page = total %d, files %d, hasMore %t", page.Total, len(page.Files), page.HasMore)
	}
	if page.Files[0].ID != EncodePath("/智勤/2021年前/a.mp4") || page.Files[0].ParentID != EncodePath("/智勤/2021年前") {
		t.Fatalf("file IDs = %+v", page.Files[0])
	}
	if page.Files[1].Name != "b" || !page.Files[1].IsDirectory {
		t.Fatalf("directory entry = %+v", page.Files[1])
	}
	wantPath := []pan.Directory{
		{ID: RootID},
		{ID: EncodePath("/智勤"), Name: "智勤"},
		{ID: EncodePath("/智勤/2021年前"), Name: "2021年前"},
	}
	if !reflect.DeepEqual(page.Path, wantPath) {
		t.Fatalf("page path = %#v, want %#v", page.Path, wantPath)
	}

	last, err := drive.List(t.Context(), "", EncodePath("/智勤/2021年前"), 2, 2)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(last.Files) != 1 || last.Files[0].Name != "c.nfo" || last.HasMore {
		t.Fatalf("last page = %+v, hasMore %t", last.Files, last.HasMore)
	}

	// The listing is cached: three List calls issued one GetSubFiles.
	var listings int
	for _, call := range api.recordOf() {
		if strings.HasPrefix(call, "GetSubFiles ") {
			listings++
		}
	}
	if listings != 1 {
		t.Fatalf("GetSubFiles calls = %d, want 1", listings)
	}

	root, err := drive.List(t.Context(), "", RootID, 0, 100)
	if err != nil {
		t.Fatalf("List root error = %v", err)
	}
	if len(root.Files) != 0 || root.Total != 0 || root.HasMore {
		t.Fatalf("unexpected root listing for an empty fake: %+v", root)
	}
	if want := []pan.Directory{{ID: RootID}}; !reflect.DeepEqual(root.Path, want) {
		t.Fatalf("root path = %#v, want %#v", root.Path, want)
	}
}

func TestListRejectsForeignID(t *testing.T) {
	drive := testDrive(t, &fakeAPI{})
	if _, err := drive.List(t.Context(), "", "1184404884787683448", 0, 10); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("List error = %v, want ErrInvalidID", err)
	}
}

func TestListAggregatesStreamedReplies(t *testing.T) {
	api := &fakeAPI{listings: map[string][]*gen.CloudDriveFile{
		"/media": {
			entry("one.mp4", "/media/one.mp4", false, 1),
			entry("two.mp4", "/media/two.mp4", false, 2),
			entry("three", "/media/three", true, 0),
			entry("four.nfo", "/media/four.nfo", false, 4),
			entry("five.jpg", "/media/five.jpg", false, 5),
		},
	}}
	drive := testDrive(t, api)
	page, err := drive.List(t.Context(), "", EncodePath("/media"), 0, 100)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	var names []string
	for _, file := range page.Files {
		names = append(names, file.Name)
	}
	if want := []string{"one.mp4", "two.mp4", "three", "four.nfo", "five.jpg"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("aggregated names = %v, want %v", names, want)
	}
	if page.Total != 5 || page.HasMore {
		t.Fatalf("aggregated page = total %d, hasMore %t", page.Total, page.HasMore)
	}
}

func TestInfoMapsFieldsAndUsesCachedListing(t *testing.T) {
	target := entry("2024-05-14 80529.jpg", "/智勤/2024-05-14 80529.jpg", false, 667957)
	target.FileHashes = map[uint32]string{uint32(gen.CloudDriveFile_Sha1): "8a3c0647f446a931d4df63ba7270095a89e23648"}
	target.WriteTime = timestamppb.New(time.Unix(1700000000, 0))
	api := &fakeAPI{listings: map[string][]*gen.CloudDriveFile{"/智勤": {target}}}
	drive := testDrive(t, api)

	// Populate the cache the same way a folder listing does.
	if _, err := drive.List(t.Context(), "", EncodePath("/智勤"), 0, 100); err != nil {
		t.Fatalf("List error = %v", err)
	}
	info, err := drive.Info(t.Context(), "", EncodePath("/智勤/2024-05-14 80529.jpg"))
	if err != nil {
		t.Fatalf("Info error = %v", err)
	}
	want := pan.FileInfo{
		File: pan.File{
			ID:       EncodePath("/智勤/2024-05-14 80529.jpg"),
			ParentID: EncodePath("/智勤"),
			Name:     "2024-05-14 80529.jpg",
			Size:     667957,
			PickCode: EncodePath("/智勤/2024-05-14 80529.jpg"),
			SHA1:     "8A3C0647F446A931D4DF63BA7270095A89E23648",
		},
		Path: pathDirectories("/智勤/2024-05-14 80529.jpg"),
	}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("Info = %#v, want %#v", info, want)
	}
	for _, call := range api.recordOf() {
		if strings.HasPrefix(call, "FindFileByPath ") {
			t.Fatalf("Info re-queried CloudDrive2 despite the cached listing: %v", api.recordOf())
		}
	}
}

func TestInfoFallsBackToFindFileByPath(t *testing.T) {
	api := &fakeAPI{finds: map[string]*gen.CloudDriveFile{
		"/智勤/2021年前/movie.mp4": entry("movie.mp4", "/智勤/2021年前/movie.mp4", false, 42),
	}}
	drive := testDrive(t, api)
	info, err := drive.Info(t.Context(), "", EncodePath("/智勤/2021年前/movie.mp4"))
	if err != nil {
		t.Fatalf("Info error = %v", err)
	}
	if info.Name != "movie.mp4" || info.Size != 42 || info.ParentID != EncodePath("/智勤/2021年前") {
		t.Fatalf("Info = %+v", info)
	}
	if got := api.recordOf(); !reflect.DeepEqual(got, []string{"FindFileByPath /智勤/2021年前/movie.mp4"}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestInfoMapsNotFoundToSentinel(t *testing.T) {
	drive := testDrive(t, &fakeAPI{})
	_, err := drive.Info(t.Context(), "", EncodePath("/missing.mp4"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Info error = %v, want ErrNotFound", err)
	}
}

func TestResolveAccountSelection(t *testing.T) {
	twoAccounts := &fakeAPI{root: []*gen.CloudDriveFile{
		accountEntry("115open", "80005515", "甲", "/甲"),
		accountEntry("115open", "12960020", "乙", "/乙"),
	}}
	for _, test := range []struct {
		name     string
		config   string
		api      *fakeAPI
		wantID   string
		wantPath string
		wantErr  error
	}{
		{name: "single account is discovered", api: &fakeAPI{root: []*gen.CloudDriveFile{
			accountEntry("115open", "80005515", "甲", "/甲"),
		}}, wantID: "115open:80005515", wantPath: "/甲"},
		{name: "configured account wins", config: "115open:12960020", api: twoAccounts,
			wantID: "115open:12960020", wantPath: "/乙"},
		{name: "configured account must exist", config: "115open:missing", api: twoAccounts,
			wantErr: ErrAccountNotFound},
		{name: "ambiguous accounts require configuration", api: twoAccounts, wantErr: ErrAccountRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			drive := testDrive(t, test.api)
			drive.account = test.config
			account, err := drive.resolveAccount(t.Context())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("resolveAccount error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAccount error = %v", err)
			}
			if account.ID != test.wantID || account.Path != test.wantPath {
				t.Fatalf("account = %+v, want %s at %s", account, test.wantID, test.wantPath)
			}
		})
	}
}

func TestAccountReportsSpaceAndToleratesPermissionErrors(t *testing.T) {
	api := &fakeAPI{
		root:   []*gen.CloudDriveFile{accountEntry("115open", "80005515", "甲", "/甲")},
		spaces: map[string]*gen.SpaceInfo{"/甲": {TotalSpace: 3 << 30, UsedSpace: 1 << 30, FreeSpace: 2 << 30}},
	}
	drive := testDrive(t, api)
	account, err := drive.Account(t.Context(), "ignored")
	if err != nil {
		t.Fatalf("Account error = %v", err)
	}
	if account.ID != "115open:80005515" || account.Name != "甲" || account.Level != "115open" {
		t.Fatalf("account = %+v", account)
	}
	if account.Space.Total.Bytes != 3<<30 || account.Space.Remaining.Formatted != "2.00 GB" {
		t.Fatalf("space = %+v", account.Space)
	}

	// A token without space permissions must not make Account fail.
	noSpace := testDrive(t, &fakeAPI{root: api.root})
	account, err = noSpace.Account(t.Context(), "")
	if err != nil {
		t.Fatalf("Account error = %v", err)
	}
	if account.ID != "115open:80005515" || account.Space.Total.Bytes != 0 {
		t.Fatalf("account without space = %+v", account)
	}
}

func TestDownloadURLExpandsRelayPath(t *testing.T) {
	encoded := "%E6%99%BA%E5%8B%A4%2F2024-05-14%2080529.jpg"
	api := &fakeAPI{downloads: map[string]*gen.DownloadUrlPathInfo{
		"/智勤/2024-05-14 80529.jpg": {
			DownloadUrlPath: "/static/{SCHEME}/{HOST}/{PREVIEW}/" + encoded + "?token=abc",
		},
		"/智勤/direct.jpg": {DirectUrl: ptr("https://cdn.example/direct.jpg")},
		"/智勤/empty.jpg":  {},
	}}
	drive := testDrive(t, api)

	address, err := drive.DownloadURL(t.Context(), "", EncodePath("/智勤/2024-05-14 80529.jpg"), pan.MediaUserAgent)
	if err != nil {
		t.Fatalf("DownloadURL error = %v", err)
	}
	if want := "http://cd2.example:19798/static/http/cd2.example:19798/false/" + encoded + "?token=abc"; address != want {
		t.Fatalf("download URL = %q, want %q", address, want)
	}

	direct, err := drive.DownloadURL(t.Context(), "", EncodePath("/智勤/direct.jpg"), "")
	if err != nil || direct != "https://cdn.example/direct.jpg" {
		t.Fatalf("direct URL = %q, error = %v", direct, err)
	}

	if _, err := drive.DownloadURL(t.Context(), "", EncodePath("/智勤/empty.jpg"), ""); !errors.Is(err, pan.ErrDownloadUnavailable) {
		t.Fatalf("empty download URL error = %v, want pan.ErrDownloadUnavailable", err)
	}
	if _, err := drive.DownloadURL(t.Context(), "", "not-an-id", ""); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid ID error = %v, want ErrInvalidID", err)
	}
}

func TestDownloadURLUsesTLSOriginWhenConfigured(t *testing.T) {
	c := &client{api: &fakeAPI{downloads: map[string]*gen.DownloadUrlPathInfo{
		"/a.mp4": {DownloadUrlPath: "/static/{SCHEME}/{HOST}/false/a.mp4"},
	}}, scheme: "https", host: "cd2.example:19799", media: http.DefaultClient}
	c.configureToken("fixture-token")
	drive := &Drive{client: c}
	address, err := drive.DownloadURL(t.Context(), "", EncodePath("/a.mp4"), "")
	if err != nil {
		t.Fatalf("DownloadURL error = %v", err)
	}
	if want := "https://cd2.example:19799/static/https/cd2.example:19799/false/a.mp4"; address != want {
		t.Fatalf("download URL = %q, want %q", address, want)
	}
}

func TestTokenHandling(t *testing.T) {
	t.Run("configured token is sent as is", func(t *testing.T) {
		api := &fakeAPI{listings: map[string][]*gen.CloudDriveFile{"/d": {}}}
		drive := testDrive(t, api)
		if _, err := drive.List(t.Context(), "", EncodePath("/d"), 0, 10); err != nil {
			t.Fatalf("List error = %v", err)
		}
		if got := api.authRecordOf(); !reflect.DeepEqual(got, []string{"Bearer fixture-token"}) {
			t.Fatalf("authorization headers = %v", got)
		}
		if got := api.recordOf(); !reflect.DeepEqual(got, []string{"GetSubFiles /d"}) {
			t.Fatalf("calls = %v (GetToken must not run for a configured token)", got)
		}
	})

	t.Run("credentials are exchanged once and renewed before expiry", func(t *testing.T) {
		api := &fakeAPI{
			token: &gen.JWTToken{Success: true, Token: "jwt-1", Expiration: timestamppb.New(time.Now().Add(time.Hour))},
			listings: map[string][]*gen.CloudDriveFile{
				"/a": {}, "/b": {},
			},
		}
		c := &client{api: api, scheme: "http", host: "cd2.example:19798", media: http.DefaultClient,
			username: "miyabi", password: "secret"}
		drive := &Drive{client: c}
		for _, name := range []string{"/a", "/b"} {
			if _, err := drive.List(t.Context(), "", EncodePath(name), 0, 10); err != nil {
				t.Fatalf("List error = %v", err)
			}
		}
		if got := api.authRecordOf(); !reflect.DeepEqual(got, []string{"Bearer jwt-1", "Bearer jwt-1"}) {
			t.Fatalf("authorization headers = %v", got)
		}
	})

	t.Run("missing credentials fail before any call", func(t *testing.T) {
		api := &fakeAPI{}
		c := &client{api: api, scheme: "http", host: "cd2.example:19798", media: http.DefaultClient}
		drive := &Drive{client: c}
		_, err := drive.List(t.Context(), "", EncodePath("/a"), 0, 10)
		if !errors.Is(err, ErrTokenRequired) {
			t.Fatalf("List error = %v, want ErrTokenRequired", err)
		}
		if calls := api.recordOf(); len(calls) != 0 {
			t.Fatalf("calls = %v, want none", calls)
		}
	})

	t.Run("rejected credentials report ErrTokenRequired", func(t *testing.T) {
		api := &fakeAPI{token: &gen.JWTToken{Success: false, ErrorMessage: "invalid credentials"}}
		c := &client{api: api, scheme: "http", host: "cd2.example:19798", media: http.DefaultClient,
			username: "miyabi", password: "wrong"}
		if _, err := (&Drive{client: c}).Account(t.Context(), ""); !errors.Is(err, ErrTokenRequired) {
			t.Fatalf("Account error = %v, want ErrTokenRequired", err)
		}
	})

	t.Run("transport failures surface the gRPC status", func(t *testing.T) {
		api := &fakeAPI{listingErr: map[string]error{"/d": status.Error(codes.Unauthenticated, "No valid auth token")}}
		drive := testDrive(t, api)
		_, err := drive.List(t.Context(), "", EncodePath("/d"), 0, 10)
		if err == nil || !strings.Contains(err.Error(), "No valid auth token") {
			t.Fatalf("List error = %v", err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Fatalf("unauthenticated must not map to ErrNotFound: %v", err)
		}
	})
}

func TestReadMetadataUsesDownloadedPrefix(t *testing.T) {
	prefix := strings.Repeat("a", 128)
	recorder := &mediaServer{body: prefix, status: http.StatusOK}
	server := httptest.NewServer(recorder)
	defer server.Close()
	drive := mediaDrive(t, server.URL, "/media/movie.nfo")

	body, err := drive.ReadMetadata(t.Context(), "", EncodePath("/media/movie.nfo"), 1024)
	if err != nil {
		t.Fatalf("ReadMetadata error = %v", err)
	}
	if string(body) != prefix {
		t.Fatalf("body = %q", body)
	}
	if got := recorder.lastUserAgent; got != pan.MediaUserAgent {
		t.Fatalf("user agent = %q, want %q", got, pan.MediaUserAgent)
	}
	if recorder.lastURL != "/media/movie.nfo" {
		t.Fatalf("metadata URL = %q", recorder.lastURL)
	}

	// The limit is a hard ceiling, not a truncation point.
	recorder.body = strings.Repeat("b", 2048)
	if _, err := drive.ReadMetadata(t.Context(), "", EncodePath("/media/movie.nfo"), 1024); err == nil {
		t.Fatal("ReadMetadata accepted a body larger than the limit")
	}
}

func TestReadMetadataRejectsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(&mediaServer{body: "denied", status: http.StatusForbidden})
	defer server.Close()
	drive := mediaDrive(t, server.URL, "/media/movie.nfo")
	if _, err := drive.ReadMetadata(t.Context(), "", EncodePath("/media/movie.nfo"), 1024); err == nil {
		t.Fatal("ReadMetadata accepted an HTTP error response")
	}
}

func TestOpenMediaForwardsPlayerHeaders(t *testing.T) {
	recorder := &mediaServer{body: strings.Repeat("x", 4096), status: http.StatusPartialContent}
	server := httptest.NewServer(recorder)
	defer server.Close()
	drive := mediaDrive(t, server.URL, "/media/movie.mp4")
	address, err := drive.DownloadURL(t.Context(), "", EncodePath("/media/movie.mp4"), "")
	if err != nil {
		t.Fatalf("DownloadURL error = %v", err)
	}
	response, err := drive.OpenMedia(t.Context(), http.MethodGet, address, http.Header{
		"User-Agent": {"VidHub/1.8.0"}, "Range": {"bytes=0-99"}, "If-Range": {"etag"},
	})
	if err != nil {
		t.Fatalf("OpenMedia error = %v", err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("read media body: %v", err)
	}
	if recorder.lastRange != "bytes=0-99" || recorder.lastIfRange != "etag" || recorder.lastUserAgent != "VidHub/1.8.0" {
		t.Fatalf("forwarded headers = range %q, if-range %q, agent %q",
			recorder.lastRange, recorder.lastIfRange, recorder.lastUserAgent)
	}
	// API authorization must never leak to the media endpoint.
	if recorder.lastAuthorization != "" {
		t.Fatalf("media request carried authorization %q", recorder.lastAuthorization)
	}
}

// TestPlayURLReportsMissingTranscoding pins the contract strm.Relay relies on:
// CloudDrive2 serves no transcoded streams, so playback uses DownloadURL.
func TestPlayURLReportsMissingTranscoding(t *testing.T) {
	drive := testDrive(t, &fakeAPI{})
	if _, err := drive.PlayURL(t.Context(), "", "", ""); !errors.Is(err, pan.ErrTranscodeUnavailable) {
		t.Fatalf("PlayURL error = %v, want pan.ErrTranscodeUnavailable", err)
	}
}

func TestUnsupportedMethodsReportErrUnsupported(t *testing.T) {
	drive := testDrive(t, &fakeAPI{})
	ctx := t.Context()
	for _, test := range []struct {
		name string
		call func() error
	}{
		{"BeginLogin", func() error { _, err := drive.BeginLogin(ctx); return err }},
		{"LoginStatus", func() error { _, err := drive.LoginStatus(ctx, nil); return err }},
		{"ExchangeToken", func() error { _, err := drive.ExchangeToken(ctx, nil); return err }},
		{"RefreshToken", func() error { _, err := drive.RefreshToken(ctx, ""); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("%s error = %v, want ErrUnsupported", test.name, err)
			}
		})
	}
}

func TestNewRequiresEndpointAndCredentials(t *testing.T) {
	if _, err := New(Config{Token: "token"}); !errors.Is(err, ErrMissingEndpoint) {
		t.Fatalf("New without endpoint error = %v, want ErrMissingEndpoint", err)
	}
	if _, err := New(Config{Endpoint: "127.0.0.1:19798"}); !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("New without credentials error = %v, want ErrTokenRequired", err)
	}
	drive, err := New(Config{Endpoint: "https://127.0.0.1:19799", Token: "token", Account: "115open:1"})
	if err != nil {
		t.Fatalf("New error = %v", err)
	}
	defer drive.Close()
	if drive.client.scheme != "https" || drive.client.host != "127.0.0.1:19799" {
		t.Fatalf("endpoint parsed as %q %q", drive.client.scheme, drive.client.host)
	}
}

func TestSplitEndpoint(t *testing.T) {
	for _, test := range []struct {
		in     string
		scheme string
		host   string
		tls    bool
	}{
		{"127.0.0.1:19798", "http", "127.0.0.1:19798", false},
		{"http://127.0.0.1:19798", "http", "127.0.0.1:19798", false},
		{"https://cd2.example:19799", "https", "cd2.example:19799", true},
		{"cd2.example:19798", "http", "cd2.example:19798", false},
	} {
		scheme, host, useTLS := splitEndpoint(test.in)
		if scheme != test.scheme || host != test.host || useTLS != test.tls {
			t.Errorf("splitEndpoint(%q) = %q, %q, %t", test.in, scheme, host, useTLS)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for _, test := range []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{3 << 30, "3.00 GB"},
		{1 << 50, "1.00 PB"},
	} {
		if got := formatBytes(test.bytes); got != test.want {
			t.Errorf("formatBytes(%d) = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func ptr[T any](value T) *T { return &value }

// mediaServer is a minimal HTTP endpoint standing in for CloudDrive2's media
// handler, recording the headers a player sends.
type mediaServer struct {
	body                                                     string
	status                                                   int
	lastURL                                                  string
	lastUserAgent, lastRange, lastIfRange, lastAuthorization string
}

func (s *mediaServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.lastURL = r.URL.String()
	s.lastUserAgent = r.Header.Get("User-Agent")
	s.lastRange = r.Header.Get("Range")
	s.lastIfRange = r.Header.Get("If-Range")
	s.lastAuthorization = r.Header.Get("Authorization")
	w.WriteHeader(s.status)
	_, _ = io.WriteString(w, s.body)
}

// mediaDrive wires a Drive whose media endpoints point at a local test server.
func mediaDrive(t *testing.T, serverURL, mediaPath string) *Drive {
	t.Helper()
	api := &fakeAPI{downloads: map[string]*gen.DownloadUrlPathInfo{
		mediaPath: {DirectUrl: ptr(serverURL + mediaPath)},
	}}
	return testDrive(t, api)
}

func TestListReportsStreamErrors(t *testing.T) {
	api := &fakeAPI{listingErr: map[string]error{
		"/gone": status.Error(codes.NotFound, `find by path: "/gone" not found under "/"`),
	}}
	drive := testDrive(t, api)
	// A directory deleted between the ID encoding and this call must not look
	// like an empty folder, which would make a scan record a vanished library.
	if _, err := drive.List(t.Context(), "", EncodePath("/gone"), 0, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("List error = %v, want ErrNotFound", err)
	}
}
