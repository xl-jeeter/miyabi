package cd2

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/ppxb/miyabi/internal/drive/cd2/gen"
	"github.com/ppxb/miyabi/internal/pan"
)

// Offline status values the rest of miyabi already understands from 115.
const (
	offlineWaiting     = 0
	offlineDownloading = 1
	offlineDone        = 2
	offlineFailed      = -1
)

// destCache remembers where this process sent an info hash. CloudDrive2 reports
// offline tasks with cloud-internal parent IDs that cannot be turned back into
// paths, but duplicate detection compares destinations, so the folder is kept
// from the submission that started the task.
type destCache struct {
	mu       sync.Mutex
	folders  map[string]string
	sequence []string
}

// destCacheLimit bounds the remembered hashes; it only accelerates duplicate
// detection, so evicting old entries is harmless.
const destCacheLimit = 256

func (c *destCache) put(hash, folder string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.folders == nil {
		c.folders = make(map[string]string)
	}
	if _, seen := c.folders[hash]; !seen {
		c.sequence = append(c.sequence, hash)
	}
	c.folders[hash] = folder
	if len(c.sequence) > destCacheLimit {
		delete(c.folders, c.sequence[0])
		c.sequence = c.sequence[1:]
	}
}

func (c *destCache) get(hash string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.folders[hash]
}

// AddOffline submits a magnet to CloudDrive2 and returns its info hash.
// CloudDrive2 routes the task to a cloud that supports offline downloads, so
// the destination folder decides the account rather than an account argument.
func (d *Drive) AddOffline(ctx context.Context, _ string, magnet, dirID string) (string, error) {
	hash, err := infoHashOf(magnet)
	if err != nil {
		return "", err
	}
	folder, err := DecodePath(dirID)
	if err != nil {
		return "", err
	}
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return "", err
	}
	defer cancel()
	result, err := d.client.api.AddOfflineFiles(request, &gen.AddOfflineFileRequest{
		Urls: magnet, ToFolder: folder,
	})
	if err != nil {
		return "", wrapCall("add offline download", err)
	}
	if !result.GetSuccess() {
		return "", fmt.Errorf("CloudDrive2 离线下载提交失败：%s", result.GetErrorMessage())
	}
	d.destinations.put(hash, dirID)
	return hash, nil
}

// RemoveOffline cancels an offline task and keeps its downloaded files, which
// is what miyabi expects when it retries a stalled download with another magnet.
func (d *Drive) RemoveOffline(ctx context.Context, _ string, hash string) error {
	cloud, userName, err := d.accountParts(ctx)
	if err != nil {
		return err
	}
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	result, err := d.client.api.RemoveOfflineFiles(request, &gen.RemoveOfflineFilesRequest{
		CloudName: cloud, CloudAccountId: userName,
		InfoHashes: []string{strings.ToLower(hash)}, DeleteFiles: false,
	})
	if err != nil {
		return wrapCall("remove offline download", err)
	}
	if !result.GetSuccess() {
		return fmt.Errorf("CloudDrive2 离线任务取消失败：%s", result.GetErrorMessage())
	}
	return nil
}

// OfflineTasks returns one page of offline tasks of the account cloud.
// CloudDrive2 reports tasks per cloud account, so no path scoping is applied.
// Completed tasks have no resolvable location, which leaves their FileID empty
// and lets the offline service fall back to a full library scan.
func (d *Drive) OfflineTasks(ctx context.Context, _ string, page int) (pan.OfflinePage, error) {
	cloud, userName, err := d.accountParts(ctx)
	if err != nil {
		return pan.OfflinePage{}, err
	}
	if page < 1 {
		page = 1
	}
	request, cancel, err := d.client.authorize(ctx)
	if err != nil {
		return pan.OfflinePage{}, err
	}
	defer cancel()
	result, err := d.client.api.ListAllOfflineFiles(request, &gen.OfflineFileListAllRequest{
		CloudName: cloud, CloudAccountId: userName, Page: uint32(page),
	})
	if err != nil {
		return pan.OfflinePage{}, wrapCall("list offline downloads", err)
	}
	tasks := result.GetOfflineFiles()
	pageResult := pan.OfflinePage{PageCount: int(result.GetPageCount()), Tasks: make([]pan.OfflineTask, 0, len(tasks))}
	for _, task := range tasks {
		hash := strings.ToLower(task.GetInfoHash())
		pageResult.Tasks = append(pageResult.Tasks, pan.OfflineTask{
			Hash: hash, Status: offlineStatus(task.GetStatus()),
			Progress:    int(max(0, min(100, task.GetPercendDone()))),
			RawProgress: max(0, min(100, task.GetPercendDone())),
			// CloudDrive2 reports finished tasks with cloud-internal file IDs
			// that miyabi cannot address, so the location stays unknown and the
			// offline service asks for a library scan instead of a targeted one.
			DirectoryID: d.destinations.get(hash),
		})
	}
	return pageResult, nil
}

func offlineStatus(value gen.OfflineFileStatus) int {
	switch value {
	case gen.OfflineFileStatus_OFFLINE_INIT:
		return offlineWaiting
	case gen.OfflineFileStatus_OFFLINE_DOWNLOADING:
		return offlineDownloading
	case gen.OfflineFileStatus_OFFLINE_FINISHED:
		return offlineDone
	case gen.OfflineFileStatus_OFFLINE_ERROR:
		return offlineFailed
	default:
		// Unknown records keep polling instead of being reported as failures.
		return offlineWaiting
	}
}

// accountParts splits the served account into the cloud name and user name the
// offline RPCs expect.
func (d *Drive) accountParts(ctx context.Context) (string, string, error) {
	account, err := d.resolveAccount(ctx)
	if err != nil {
		return "", "", err
	}
	return CloudName(account.ID), UserName(account.ID), nil
}

// infoHashOf extracts the lowercase hexadecimal info hash from a magnet URI.
func infoHashOf(magnet string) (string, error) {
	parsed, err := url.Parse(magnet)
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return "", ErrInvalidInfoHash
	}
	for _, exactTopic := range parsed.Query()["xt"] {
		value, ok := strings.CutPrefix(strings.ToLower(exactTopic), "urn:btih:")
		if !ok {
			continue
		}
		switch len(value) {
		case 40:
			if _, err := hex.DecodeString(value); err == nil {
				return value, nil
			}
		case 32:
			decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(value))
			if err == nil {
				return hex.EncodeToString(decoded), nil
			}
		}
	}
	return "", ErrInvalidInfoHash
}
