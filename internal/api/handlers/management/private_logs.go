package management

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/rpmode"
)

const privatePreviewSize = 64 * 1024

var privateLogStatusWorker = make(chan struct{}, 1)

var privateRequestIDPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

var privateMediaPrefix = regexp.MustCompile(`(?i)(?:data:(image/(?:png|jpeg|gif|webp)|audio/(?:mpeg|wav|ogg)|video/(?:mp4|webm));base64,|"mimeType"\s*:\s*"(image/(?:png|jpeg|gif|webp)|audio/(?:mpeg|wav|ogg)|video/(?:mp4|webm))"\s*,\s*"data"\s*:\s*")`)

type privateMediaRef struct {
	Offset int64  `json:"offset"`
	MIME   string `json:"mime"`
}

type privateLogFile struct {
	Name      string `json:"name"`
	RequestID string `json:"request_id"`
	Size      int64  `json:"size"`
	Modified  int64  `json:"modified"`
}

type privateLogCursor struct {
	Modified int64  `json:"modified"`
	Name     string `json:"name"`
}

func privateLogName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") && filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\\`) && strings.HasSuffix(name, ".log") &&
		name != defaultLogFileName && !isRotatedLogFile(name)
}

func privateLogPath(dir, name string) (string, error) {
	if !privateLogName(name) {
		return "", fmt.Errorf("invalid request log name")
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("invalid request log file")
	}
	return path, nil
}

func requirePrivateLogging(c *gin.Context) bool {
	if !rpmode.RequestLoggingEnabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "private request logging is unavailable"})
		return false
	}
	return true
}

// ListPrivateRequestLogs returns a bounded page of completed local request logs.
func (h *Handler) ListPrivateRequestLogs(c *gin.Context) {
	if !requirePrivateLogging(c) {
		return
	}
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
		limit = parsed
	}
	var cursor privateLogCursor
	if value := c.Query("cursor"); value != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || !privateLogName(cursor.Name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cursor"})
			return
		}
	}
	var after, before int64
	for _, item := range []struct {
		key string
		ptr *int64
	}{{"after", &after}, {"before", &before}} {
		if value := c.Query(item.key); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid time filter"})
				return
			}
			*item.ptr = parsed
		}
	}
	entries, err := os.ReadDir(h.logDirectory())
	if err != nil && !os.IsNotExist(err) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot read request logs"})
		return
	}
	query := strings.ToLower(strings.TrimSpace(c.Query("q")))
	files := make([]privateLogFile, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !privateLogName(name) || (query != "" && !strings.Contains(strings.ToLower(name), query)) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil || !info.Mode().IsRegular() {
			continue
		}
		modified := info.ModTime().UnixNano()
		if after > 0 && modified < after*1e9 || before > 0 && modified >= before*1e9 {
			continue
		}
		if cursor.Name != "" && (modified > cursor.Modified || (modified == cursor.Modified && name >= cursor.Name)) {
			continue
		}
		files = append(files, privateLogFile{Name: name, RequestID: privateRequestIDPattern.FindString(name), Size: info.Size(), Modified: modified})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Modified > files[j].Modified || (files[i].Modified == files[j].Modified && files[i].Name > files[j].Name)
	})
	next := ""
	if len(files) > limit {
		files = files[:limit]
		last := files[len(files)-1]
		raw, _ := json.Marshal(privateLogCursor{Modified: last.Modified, Name: last.Name})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	for i := range files {
		files[i].Modified /= 1e9
	}
	c.JSON(http.StatusOK, gin.H{"files": files, "next_cursor": next})
}

// PreviewPrivateRequestLog returns bounded raw bytes, encoded by JSON as base64.
func (h *Handler) PreviewPrivateRequestLog(c *gin.Context) {
	if !requirePrivateLogging(c) {
		return
	}
	path, err := privateLogPath(h.logDirectory(), c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request log not found"})
		return
	}
	offset := int64(0)
	if value := c.Query("offset"); value != "" {
		offset, err = strconv.ParseInt(value, 10, 64)
		if err != nil || offset < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
			return
		}
	}
	file, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request log not found"})
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || offset > info.Size() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid offset"})
		return
	}
	data := make([]byte, privatePreviewSize)
	n, readErr := file.ReadAt(data, offset)
	if readErr != nil && readErr != io.EOF {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot read request log"})
		return
	}
	if offset+int64(n) < info.Size() {
		for i := n - 1; i >= 0 && i >= n-utf8.UTFMax; i-- {
			if utf8.RuneStart(data[i]) {
				if !utf8.FullRune(data[i:n]) {
					n = i
				}
				break
			}
		}
	}
	start := offset - 256
	if start < 0 {
		start = 0
	}
	window := make([]byte, int(offset-start)+n)
	_, _ = file.ReadAt(window, start)
	allRefs := findPrivateMediaRefs(window, start)
	refs := make([]privateMediaRef, 0, len(allRefs))
	for _, ref := range allRefs {
		if ref.Offset >= offset && ref.Offset < offset+int64(n) {
			refs = append(refs, ref)
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": data[:n], "offset": offset, "next_offset": offset + int64(n), "size": info.Size(), "media": refs})
}

func findPrivateMediaRefs(data []byte, offset int64) []privateMediaRef {
	refs := make([]privateMediaRef, 0)
	for _, match := range privateMediaPrefix.FindAllSubmatchIndex(data, -1) {
		mimeStart, mimeEnd := match[2], match[3]
		if mimeStart < 0 {
			mimeStart, mimeEnd = match[4], match[5]
		}
		if mimeStart >= 0 {
			refs = append(refs, privateMediaRef{Offset: offset + int64(match[1]), MIME: strings.ToLower(string(data[mimeStart:mimeEnd]))})
		}
	}
	return refs
}

type base64StringReader struct{ source io.Reader }

func (r *base64StringReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	for i, b := range p[:n] {
		if b == '"' || b == '\'' || b == '\\' {
			return i, io.EOF
		}
	}
	return n, err
}

// DownloadPrivateLogMedia streams a base64 media value from a completed log.
func (h *Handler) DownloadPrivateLogMedia(c *gin.Context) {
	if !requirePrivateLogging(c) {
		return
	}
	path, err := privateLogPath(h.logDirectory(), c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request log not found"})
		return
	}
	offset, err := strconv.ParseInt(c.Query("offset"), 10, 64)
	if err != nil || offset <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media offset"})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request log not found"})
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || offset >= info.Size() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media offset"})
		return
	}
	start := offset - 256
	if start < 0 {
		start = 0
	}
	prefix := make([]byte, offset-start)
	if _, err := file.ReadAt(prefix, start); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media reference"})
		return
	}
	refs := findPrivateMediaRefs(prefix, start)
	var mime string
	for _, ref := range refs {
		if ref.Offset == offset {
			mime = ref.MIME
		}
	}
	if mime == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media reference"})
		return
	}
	c.Header("Content-Type", mime)
	c.Header("Content-Disposition", `attachment; filename="media"`)
	c.Header("X-Content-Type-Options", "nosniff")
	reader := &base64StringReader{source: io.NewSectionReader(file, offset, info.Size()-offset)}
	_, _ = io.Copy(c.Writer, base64.NewDecoder(base64.StdEncoding, reader))
}

// DownloadPrivateRequestLog streams one completed log without buffering it in the server.
func (h *Handler) DownloadPrivateRequestLog(c *gin.Context) {
	if !requirePrivateLogging(c) {
		return
	}
	path, err := privateLogPath(h.logDirectory(), c.Param("name"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request log not found"})
		return
	}
	c.FileAttachment(path, filepath.Base(path))
}

// GetPrivateLogStatus reports total on-disk usage and process-local capture failures.
func (h *Handler) GetPrivateLogStatus(c *gin.Context) {
	if !requirePrivateLogging(c) {
		return
	}
	type measurement struct {
		used             int64
		total, available uint64
		diskOK           bool
		err              error
	}
	results := make(chan measurement, 1)
	started := false
	select {
	case privateLogStatusWorker <- struct{}{}:
		started = true
		go func() {
			defer func() { <-privateLogStatusWorker }()
			var used int64
			err := filepath.WalkDir(h.logDirectory(), func(_ string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					if os.IsNotExist(walkErr) {
						return nil
					}
					return walkErr
				}
				if entry.Type().IsRegular() {
					info, errInfo := entry.Info()
					if errInfo != nil {
						return errInfo
					}
					used += info.Size()
				}
				return nil
			})
			total, available, diskOK := privateDiskUsage(h.logDirectory())
			results <- measurement{used, total, available, diskOK, err}
		}()
	default:
	}
	var measured measurement
	if started {
		select {
		case measured = <-results:
		case <-time.After(2 * time.Second):
			measured.err = fmt.Errorf("log storage status timed out")
		}
	} else {
		measured.err = fmt.Errorf("log storage status is unavailable")
	}
	if measured.err != nil {
		logging.ReportPrivateCaptureFailure()
	}
	var limit int64
	if h.cfg != nil {
		limit = int64(h.cfg.LogsMaxTotalSizeMB) * 1024 * 1024
	}
	failures, drops, lastFailure := logging.PrivateCaptureStatus()
	c.JSON(http.StatusOK, gin.H{"used_bytes": measured.used, "usage_available": measured.err == nil, "limit_bytes": limit, "disk_total_bytes": measured.total, "disk_available_bytes": measured.available, "disk_stats_available": measured.err == nil && measured.diskOK, "capture_failures": failures, "dropped_chunks": drops, "last_failure": lastFailure})
}

func clearPrivateRequestLogs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !privateLogName(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		if errRemove := os.Remove(filepath.Join(dir, entry.Name())); errRemove != nil && !os.IsNotExist(errRemove) {
			return errRemove
		}
	}
	return nil
}
