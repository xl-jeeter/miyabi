// Package cd2 adapts a CloudDrive2 instance to the miyabi drive.Client
// interface, so the online library can read from CloudDrive2 instead of the
// 115 API.
package cd2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"
	"github.com/ppxb/miyabi/internal/pan"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// accountCacheTTL bounds how long the resolved cloud account is reused.
	accountCacheTTL = 60 * time.Second
	// mediaTimeout bounds waiting for media response headers. Bodies stream for
	// as long as the player keeps reading, so they carry no total timeout.
	mediaTimeout = 60 * time.Second
	// metadataLimit caps sidecar reads independently of the caller's limit.
	metadataLimit = 8 << 20
)

// Drive implements drive.Client on top of CloudDrive2's gRPC API.
//
// CloudDrive2 addresses files by path while miyabi passes opaque IDs, so the
// adapter derives IDs from absolute paths (see EncodePath). The account
// argument of every method is ignored: one adapter serves exactly one cloud
// account, selected by Config.Account.
type Drive struct {
	client  *client
	account string

	cache        listingCache
	destinations destCache
	accounts     struct {
		sync.Mutex
		value   []cloudAccount
		fetched time.Time
	}
}

// cloudAccount is one cloud account mounted at the CloudDrive2 root.
type cloudAccount struct {
	// ID is "<cloud name>:<userName>", the miyabi account ID.
	ID string
	// Name labels the account in the UI.
	Name string
	// Path is the account's root directory inside CloudDrive2.
	Path string
}

// New connects to CloudDrive2. The connection is established lazily by gRPC,
// so a temporarily unreachable instance does not prevent startup.
func New(cfg Config) (*Drive, error) {
	conn, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return newDrive(cfg, conn), nil
}

func newDrive(cfg Config, c *client) *Drive {
	c.configureToken(strings.TrimSpace(cfg.Token))
	return &Drive{client: c, account: strings.TrimSpace(cfg.Account)}
}

// Tokenless reports that CloudDrive2 owns the cloud credentials, so miyabi
// never has a 115 access token to gate requests on.
func (d *Drive) Tokenless() bool { return true }

// Close releases the gRPC connection and idle media connections.
func (d *Drive) Close() {
	d.client.close()
}

// Account reports the CloudDrive2 cloud account served by this adapter,
// including the space of that cloud when CloudDrive2 exposes it.
func (d *Drive) Account(ctx context.Context, _ string) (pan.Account, error) {
	account, err := d.resolveAccount(ctx)
	if err != nil {
		return pan.Account{}, err
	}
	result := pan.Account{ID: account.ID, Name: account.Name, Level: CloudName(account.ID)}

	// Space reporting is a convenience for the settings page: a CloudDrive2
	// token without space permissions must not make the account unverifiable.
	if space, err := d.spaceInfo(ctx, account.Path); err == nil {
		result.Space = space
	}
	return result, nil
}

// CloudName returns the cloud provider part of an account ID.
func CloudName(accountID string) string {
	name, _, _ := strings.Cut(accountID, ":")
	return name
}

// UserName returns the CloudDrive2 user part of an account ID.
func UserName(accountID string) string {
	_, userName, _ := strings.Cut(accountID, ":")
	return userName
}

func (d *Drive) spaceInfo(ctx context.Context, accountPath string) (pan.AccountSpace, error) {
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return pan.AccountSpace{}, err
	}
	defer cancel()
	space, err := d.client.api.GetSpaceInfo(request, &gen.FileRequest{Path: accountPath})
	if err != nil {
		return pan.AccountSpace{}, err
	}
	total, used, remaining := space.GetTotalSpace(), space.GetUsedSpace(), space.GetFreeSpace()
	return pan.AccountSpace{
		Total:     pan.SpaceAmount{Bytes: total, Formatted: formatBytes(total)},
		Used:      pan.SpaceAmount{Bytes: used, Formatted: formatBytes(used)},
		Remaining: pan.SpaceAmount{Bytes: remaining, Formatted: formatBytes(remaining)},
	}, nil
}

// resolveAccount selects the cloud account this adapter serves. Accounts come
// from the CloudDrive2 root listing, which reports the owning CloudAPI for each
// mounted cloud root; GetAllCloudApis would report the same set but requires an
// extra token permission that read-only API tokens usually lack.
func (d *Drive) resolveAccount(ctx context.Context) (cloudAccount, error) {
	d.accounts.Lock()
	accounts, fetched := d.accounts.value, d.accounts.fetched
	d.accounts.Unlock()
	if len(accounts) == 0 || time.Since(fetched) >= accountCacheTTL {
		var err error
		if accounts, err = d.listAccounts(ctx); err != nil {
			return cloudAccount{}, err
		}
		d.accounts.Lock()
		d.accounts.value, d.accounts.fetched = accounts, time.Now()
		d.accounts.Unlock()
	}

	if d.account != "" {
		for _, account := range accounts {
			if account.ID == d.account {
				return account, nil
			}
		}
		return cloudAccount{}, fmt.Errorf("%w：%s，可用账号：%s", ErrAccountNotFound, d.account, accountIDs(accounts))
	}
	if len(accounts) != 1 {
		return cloudAccount{}, fmt.Errorf("%w，可用账号：%s", ErrAccountRequired, accountIDs(accounts))
	}
	return accounts[0], nil
}

func accountIDs(accounts []cloudAccount) string {
	if len(accounts) == 0 {
		return "（无）"
	}
	ids := make([]string, len(accounts))
	for i, account := range accounts {
		ids[i] = account.ID
	}
	sort.Strings(ids)
	return strings.Join(ids, "、")
}

func (d *Drive) listAccounts(ctx context.Context) ([]cloudAccount, error) {
	entries, err := d.listing(ctx, "/")
	if err != nil {
		return nil, fmt.Errorf("list CloudDrive2 cloud accounts: %w", err)
	}
	accounts := make([]cloudAccount, 0, len(entries))
	for _, entry := range entries {
		cloud := entry.GetCloudAPI()
		if cloud.GetName() == "" || cloud.GetUserName() == "" {
			continue
		}
		accounts = append(accounts, cloudAccount{
			ID:   cloud.GetName() + ":" + cloud.GetUserName(),
			Name: cloud.GetNickName(),
			Path: filePath(entry),
		})
	}
	return accounts, nil
}

// List returns one miyabi page of a CloudDrive2 directory. CloudDrive2 always
// returns the complete folder, so the page is sliced locally.
func (d *Drive) List(ctx context.Context, _, dirID string, offset, limit int) (pan.FilePage, error) {
	dirPath, err := DecodePath(dirID)
	if err != nil {
		return pan.FilePage{}, err
	}
	entries, err := d.listing(ctx, dirPath)
	if err != nil {
		return pan.FilePage{}, err
	}
	page := pageOf(entries, offset, limit)
	result := pan.FilePage{
		Files:   make([]pan.File, len(page)),
		Path:    pathDirectories(dirPath),
		Total:   len(entries),
		HasMore: offset+len(page) < len(entries),
	}
	for i, entry := range page {
		result.Files[i] = fileInfoOf(entry).File
	}
	return result, nil
}

// Info resolves one file ID to its metadata.
func (d *Drive) Info(ctx context.Context, _, fileID string) (pan.FileInfo, error) {
	name, err := DecodePath(fileID)
	if err != nil {
		return pan.FileInfo{}, err
	}
	entry, err := d.resolve(ctx, name)
	if err != nil {
		return pan.FileInfo{}, err
	}
	return fileInfoOf(entry), nil
}

// resolve finds one entry, preferring the parent directory listing that the
// last page or Info call already loaded.
func (d *Drive) resolve(ctx context.Context, name string) (*gen.CloudDriveFile, error) {
	if name == "/" {
		return &gen.CloudDriveFile{Name: "/", FullPathName: "/", IsDirectory: true}, nil
	}
	if entry, ok := d.cache.lookup(name); ok {
		return entry, nil
	}
	parent, base := path.Split(name)
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	entry, err := d.client.api.FindFileByPath(request, &gen.FindFileByPathRequest{
		ParentPath: CleanPath(parent), Path: base,
	})
	if err != nil {
		return nil, wrapCall("find "+name, err)
	}
	return entry, nil
}

// listing returns a complete directory listing, reusing a recently cached one.
func (d *Drive) listing(ctx context.Context, name string) ([]*gen.CloudDriveFile, error) {
	cleaned := CleanPath(name)
	if entries, ok := d.cache.get(cleaned); ok {
		return entries, nil
	}
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	stream, err := d.client.api.GetSubFiles(request, &gen.ListSubFileRequest{Path: cleaned})
	if err != nil {
		return nil, wrapCall("list "+cleaned, err)
	}
	var entries []*gen.CloudDriveFile
	for {
		reply, err := stream.Recv()
		if err != nil {
			if isEndOfStream(err) {
				break
			}
			return nil, wrapCall("list "+cleaned, err)
		}
		entries = append(entries, reply.GetSubFiles()...)
	}
	d.cache.put(cleaned, entries)
	return entries, nil
}

// ReadMetadata reads the first bytes of a small sidecar such as an NFO file.
// Callers pass pan.File.PickCode, and pick codes and IDs share the same
// encoding, so the value decodes to a path either way.
func (d *Drive) ReadMetadata(ctx context.Context, _, pickCode string, limit int64) ([]byte, error) {
	address, err := d.DownloadURL(ctx, "", pickCode, pan.MediaUserAgent)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > metadataLimit {
		limit = metadataLimit
	}
	readContext, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	response, err := d.OpenMedia(readContext, http.MethodGet, address, http.Header{"User-Agent": {pan.MediaUserAgent}})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("CloudDrive2 metadata returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("CloudDrive2 metadata exceeds %d bytes", limit)
	}
	return body, nil
}

// DownloadURL returns a direct media URL for a file ID. CloudDrive2 serves
// downloads through its own HTTP endpoint, so no cloud-side user agent is
// needed and the argument is ignored.
func (d *Drive) DownloadURL(ctx context.Context, _, pickCode, _ string) (string, error) {
	name, err := DecodePath(pickCode)
	if err != nil {
		return "", err
	}
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return "", err
	}
	defer cancel()
	info, err := d.client.api.GetDownloadUrlPath(request, &gen.GetDownloadUrlPathRequest{
		Path: name, GetDirectUrl: true,
	})
	if err != nil {
		return "", wrapCall("download URL for "+name, err)
	}
	if direct := info.GetDirectUrl(); direct != "" {
		return direct, nil
	}
	if info.GetDownloadUrlPath() == "" {
		return "", pan.ErrDownloadUnavailable
	}
	address, err := d.client.mediaURL(info.GetDownloadUrlPath(), false)
	if err != nil {
		return "", err
	}
	return address, nil
}

// OpenMedia streams a media URL returned by DownloadURL, forwarding the client
// headers a player relies on and ignoring the API authorization.
func (d *Drive) OpenMedia(ctx context.Context, method, address string, headers http.Header) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return nil, err
	}
	// An explicitly empty value suppresses net/http's default User-Agent.
	request.Header.Set("User-Agent", strings.TrimSpace(headers.Get("User-Agent")))
	request.Header.Set("Accept-Encoding", "identity")
	for _, name := range []string{"Range", "If-Range"} {
		if value := headers.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	response, err := d.client.media.Do(request)
	if err != nil {
		// net/http embeds the signed URL in url.Error; keep only its cause.
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, fmt.Errorf("request CloudDrive2 media: %w", err)
	}
	return response, nil
}

// BeginLogin reports that CloudDrive2 cloud accounts are signed in on the
// CloudDrive2 side, so miyabi has no QR login to offer.
// TODO(stage 3): surface CloudDrive2's own account flow in the settings page.
func (d *Drive) BeginLogin(context.Context) (*pan.Login, error) { return nil, ErrUnsupported }

// LoginStatus is unused while BeginLogin is unsupported.
// TODO(stage 3): map the CloudDrive2 login session state.
func (d *Drive) LoginStatus(context.Context, *pan.Login) (pan.LoginState, error) {
	return pan.LoginExpired, ErrUnsupported
}

// ExchangeToken is unused while BeginLogin is unsupported.
// TODO(stage 3): exchange the completed CloudDrive2 login for miyabi tokens.
func (d *Drive) ExchangeToken(context.Context, *pan.Login) (pan.Tokens, error) {
	return pan.Tokens{}, ErrUnsupported
}

// RefreshToken reports that CloudDrive2 credentials are managed from Config or
// a JWT that the client renews on its own.
func (d *Drive) RefreshToken(context.Context, string) (pan.Tokens, error) {
	return pan.Tokens{}, ErrUnsupported
}

// PlayURL reports that CloudDrive2 offers no transcoding. Playback uses the
// direct download URL instead, which strm.Relay already prefers.
func (d *Drive) PlayURL(context.Context, string, string, string) ([]pan.PlaySource, error) {
	return nil, pan.ErrTranscodeUnavailable
}

// fileInfoOf maps one CloudDrive2 entry onto miyabi's file metadata.
// CloudDrive2 has no pick code, so it repeats the file ID: both are path
// encodings and every consumer (ReadMetadata, DownloadURL, strm) accepts
// either one.
func fileInfoOf(entry *gen.CloudDriveFile) pan.FileInfo {
	name := filePath(entry)
	file := pan.File{
		ID:          EncodePath(name),
		ParentID:    EncodePath(path.Dir(name)),
		Name:        entry.GetName(),
		IsDirectory: entry.GetIsDirectory(),
		Size:        entry.GetSize(),
		PickCode:    EncodePath(name),
		SHA1:        strings.ToUpper(entry.GetFileHashes()[uint32(gen.CloudDriveFile_Sha1)]),
	}
	return pan.FileInfo{File: file, Path: pathDirectories(name)}
}

// wrapCall converts a gRPC status into a miyabi error that keeps the
// CloudDrive2 message readable.
func wrapCall(action string, err error) error {
	if isCode(err, codes.NotFound) {
		return fmt.Errorf("%s: %w", action, ErrNotFound)
	}
	return fmt.Errorf("CloudDrive2 %s: %w", action, err)
}

// isEndOfStream reports the graceful end of a server stream. A NotFound
// status also arrives through Recv, but it describes a missing directory and
// must not become an empty listing.
func isEndOfStream(err error) bool {
	return errors.Is(err, io.EOF)
}

func isCode(err error, code codes.Code) bool {
	statusError, ok := status.FromError(err)
	return ok && statusError.Code() == code
}

// formatBytes renders a byte count the way the settings page shows 115 space.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KB", "MB", "GB", "TB", "PB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.2f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.2f EB", value/unit)
}
