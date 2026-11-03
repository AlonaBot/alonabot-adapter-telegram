package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
	"github.com/AlonaBot/AlonaBot/sdk"
)

// Bot API 公共服务的 getFile 下载上限，见 CONTRACTS §1.4。
const maxDownloadSize = 20 * 1024 * 1024

type telegramFile struct {
	ID       string  `json:"file_id"`
	Size     *uint64 `json:"file_size"`
	MIME     *string `json:"mime_type"`
	Filename *string `json:"file_name"`
	Width    uint64  `json:"width"`
	Height   uint64  `json:"height"`
}

func (m *telegramMessage) mediaSegment() *pb.Segment {
	var file *telegramFile
	switch {
	case len(m.Photo) > 0:
		file = &m.Photo[0]
		for n := range m.Photo {
			candidate := &m.Photo[n]
			if candidate.Width*candidate.Height > file.Width*file.Height {
				file = candidate
			}
		}
	case m.Video != nil:
		file = m.Video
	case m.Animation != nil:
		file = m.Animation
	case m.Document != nil:
		file = m.Document
	case m.Audio != nil:
		file = m.Audio
	case m.Voice != nil:
		file = m.Voice
	default:
		return nil
	}
	incoming := &pb.IncomingMedia{Token: file.ID, Size: file.Size, Mime: file.MIME}
	source := &pb.MediaSource{Source: &pb.MediaSource_IncomingRef{IncomingRef: incoming}}
	switch {
	case len(m.Photo) > 0:
		return &pb.Segment{Data: &pb.Segment_Image{Image: &pb.ImageSegment{Source: source, Caption: m.Caption}}}
	case m.Video != nil || m.Animation != nil:
		return &pb.Segment{Data: &pb.Segment_Video{Video: &pb.VideoSegment{Source: source, Caption: m.Caption}}}
	default:
		return &pb.Segment{Data: &pb.Segment_File{File: &pb.FileSegment{Source: source, Filename: file.Filename, Mime: file.MIME, Caption: m.Caption}}}
	}
}

type platformFileLocation struct {
	Path string  `json:"file_path"`
	Size *uint64 `json:"file_size"`
}

func (i *instance) fileLocation(ctx context.Context, fileID string, offset uint64) (*platformFileLocation, error) {
	if offset > 0 {
		i.fileMu.Lock()
		file := i.files[fileID]
		i.fileMu.Unlock()
		if file != nil {
			return file, nil
		}
	}
	// 新一轮取件刷新路径；网络请求留在锁外，其他文件可同时取件。
	file := &platformFileLocation{}
	if err := i.client.call(ctx, "getFile", struct {
		ID string `json:"file_id"`
	}{fileID}, file); err != nil {
		var api *apiError
		if errors.As(err, &api) && api.status == http.StatusBadRequest && strings.Contains(api.description, "file is too big") {
			return nil, &sdk.Error{Code: pb.ErrorCode_ERROR_CODE_UNSUPPORTED, Message: api.Error()}
		}
		return nil, err
	}
	if file.Size != nil && *file.Size > maxDownloadSize {
		return nil, downloadUnsupported()
	}
	if file.Path == "" {
		return nil, fmt.Errorf("telegram getFile returned no file_path")
	}
	i.fileMu.Lock()
	if i.files == nil {
		i.files = make(map[string]*platformFileLocation)
	}
	i.files[fileID] = file
	i.fileMu.Unlock()
	return file, nil
}

// ReadPlatformFile 经实例的 HTTP 客户端按需读取平台文件；内容缓存由 Core 持有。
// offset 为 0 时刷新文件路径，后续分块复用，读取完成或出错时移除。
func (i *instance) ReadPlatformFile(ctx context.Context, fileID string, offset uint64, length uint32) (data []byte, next uint64, eof bool, err error) {
	// 1. 新一轮取件刷新可信平台路径，避免沿用上次中途放弃的过期路径。
	file, err := i.fileLocation(ctx, fileID, offset)
	if err != nil {
		return nil, offset, false, err
	}
	defer func() {
		if err != nil || eof {
			i.fileMu.Lock()
			if i.files[fileID] == file {
				delete(i.files, fileID)
			}
			i.fileMu.Unlock()
		}
	}()
	if offset > maxDownloadSize {
		return nil, offset, false, &sdk.Error{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "media offset exceeds download limit"}
	}
	if file.Size != nil {
		if offset > *file.Size {
			return nil, offset, false, &sdk.Error{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "media offset exceeds file size"}
		}
		if offset == *file.Size {
			return nil, offset, true, nil
		}
	}
	requested := min(uint64(length), maxDownloadSize-offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.client.fileBase+file.Path, nil)
	if err != nil {
		return nil, offset, false, i.client.safeError(err)
	}
	// 2. 请求精确范围；206 的总大小用于判断 EOF，避免相邻分块重复传输。
	if requested > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+requested-1))
	}
	response, err := i.client.http.Do(req)
	if err != nil {
		return nil, offset, false, i.client.safeError(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		var total uint64
		header := response.Header.Get("Content-Range")
		if n, parseErr := fmt.Sscanf(header, "bytes */%d", &total); parseErr == nil && n == 1 && header == fmt.Sprintf("bytes */%d", total) {
			if total > maxDownloadSize {
				return nil, offset, false, downloadUnsupported()
			}
			if offset == total {
				return nil, offset, true, nil
			}
			if offset > total {
				return nil, offset, false, &sdk.Error{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "media offset exceeds file size"}
			}
		}
	}
	if response.StatusCode == http.StatusPartialContent {
		var start, end, total uint64
		header := response.Header.Get("Content-Range")
		n, parseErr := fmt.Sscanf(header, "bytes %d-%d/%d", &start, &end, &total)
		if parseErr != nil || n != 3 || header != fmt.Sprintf("bytes %d-%d/%d", start, end, total) || start != offset || end < start || end >= total || end-start >= requested {
			return nil, offset, false, i.client.safeError(fmt.Errorf("telegram invalid Content-Range: %q", header))
		}
		if total > maxDownloadSize {
			return nil, offset, false, downloadUnsupported()
		}
		data, err = io.ReadAll(io.LimitReader(response.Body, int64(end-start)+2))
		if err != nil {
			return nil, offset, false, i.client.safeError(err)
		}
		if uint64(len(data)) != end-start+1 {
			return nil, offset, false, fmt.Errorf("telegram range body size mismatch")
		}
		return data, end + 1, end+1 == total, nil
	}
	if response.StatusCode != http.StatusOK {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, offset, false, i.client.safeError(err)
		}
		return nil, offset, false, i.client.safeError(fmt.Errorf("telegram download status %d: %s", response.StatusCode, body))
	}
	if response.ContentLength > maxDownloadSize {
		return nil, offset, false, downloadUnsupported()
	}
	// 3. 平台忽略 Range 返回 200 时，沿用流式跳过前缀和一个字节的 EOF 探测。
	if _, err := io.CopyN(io.Discard, response.Body, int64(offset)); err != nil {
		if err == io.EOF {
			return nil, offset, false, &sdk.Error{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "media offset exceeds file size"}
		}
		return nil, offset, false, i.client.safeError(err)
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, int64(requested)+1))
	if err != nil {
		return nil, offset, false, i.client.safeError(err)
	}
	if offset+uint64(len(data)) > maxDownloadSize {
		return nil, offset, false, downloadUnsupported()
	}
	eof = uint64(len(data)) <= requested
	if !eof {
		data = data[:requested]
	}
	return data, offset + uint64(len(data)), eof, nil
}

func downloadUnsupported() error {
	return &sdk.Error{Code: pb.ErrorCode_ERROR_CODE_UNSUPPORTED, Message: "telegram file exceeds 20 MiB download limit"}
}
