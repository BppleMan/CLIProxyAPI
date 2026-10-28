package management

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPrivateLogsPaginationPreviewAndClear(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	dir := t.TempDir()
	files := []string{
		"request-20261029T010000-019a1234-1234-7123-8123-123456789abc.log",
		"request-20261029T020000-019a1234-1234-7123-8123-123456789def.log",
	}
	content := []byte(strings.Repeat("中a", 800000))
	for index, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(1000+index), 0)
		if err := os.Chtimes(filepath.Join(dir, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.log"), []byte("main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".request-active-test.tmp"), []byte("active"), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{LoggingToFile: true, LogsMaxTotalSizeMB: 18432}, logDir: dir}
	r := gin.New()
	r.GET("/requests", h.ListPrivateRequestLogs)
	r.GET("/requests/:name/preview", h.PreviewPrivateRequestLog)
	r.GET("/requests/:name/download", h.DownloadPrivateRequestLog)
	r.GET("/status", h.GetPrivateLogStatus)
	r.DELETE("/logs", func(c *gin.Context) { c.Set(ConfigV8ContextKey, true); h.DeleteLogs(c) })
	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	w := call("GET", "/status")
	var status struct {
		UsedBytes          int64  `json:"used_bytes"`
		DiskAvailableBytes uint64 `json:"disk_available_bytes"`
		DiskStatsAvailable bool   `json:"disk_stats_available"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.UsedBytes < int64(len(content)*2) {
		t.Fatalf("invalid log status: %d %s", w.Code, w.Body.String())
	}
	if (runtime.GOOS == "linux" || runtime.GOOS == "darwin") && (!status.DiskStatsAvailable || status.DiskAvailableBytes == 0) {
		t.Fatal("disk capacity was not reported")
	}
	w = call("GET", "/requests?limit=1")
	var listing struct {
		Files      []privateLogFile `json:"files"`
		NextCursor string           `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listing) != nil || len(listing.Files) != 1 || listing.Files[0].Name != files[1] || listing.NextCursor == "" {
		t.Fatalf("unexpected first page: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/requests?limit=1&cursor="+listing.NextCursor)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listing) != nil || len(listing.Files) != 1 || listing.Files[0].Name != files[0] {
		t.Fatalf("unexpected next page: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/requests?q=789def&after=1001")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listing) != nil || len(listing.Files) != 1 || listing.Files[0].Name != files[1] {
		t.Fatalf("unexpected filtered page: %d %s", w.Code, w.Body.String())
	}
	var restored []byte
	var offset int64
	for offset < int64(len(content)) {
		w = call("GET", "/requests/"+files[0]+"/preview?offset="+strconv.FormatInt(offset, 10))
		var page struct {
			Data       []byte `json:"data"`
			NextOffset int64  `json:"next_offset"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.NextOffset <= offset {
			t.Fatalf("invalid preview: %d", w.Code)
		}
		restored = append(restored, page.Data...)
		offset = page.NextOffset
	}
	if string(restored) != string(content) {
		t.Fatal("paginated preview did not reconstruct the original UTF-8 content")
	}
	w = call("GET", "/requests/"+files[0]+"/download")
	if w.Code != http.StatusOK || w.Body.Len() != len(content) {
		t.Fatalf("invalid download: %d %d", w.Code, w.Body.Len())
	}
	w = call("DELETE", "/logs")
	if w.Code != http.StatusOK {
		t.Fatalf("clear failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, files[0])); !os.IsNotExist(err) {
		t.Fatal("completed request log survived clear")
	}
	if _, err := os.Stat(filepath.Join(dir, ".request-active-test.tmp")); err != nil {
		t.Fatal("clear removed an active request")
	}
}

func TestPrivateLogStatusReportsBlockedStorage(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	privateLogStatusWorker <- struct{}{}
	defer func() { <-privateLogStatusWorker }()
	h := &Handler{cfg: &config.Config{LogsMaxTotalSizeMB: 18432}, logDir: t.TempDir()}
	r := gin.New()
	r.GET("/status", h.GetPrivateLogStatus)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))
	var status struct {
		UsageAvailable bool   `json:"usage_available"`
		DiskAvailable  bool   `json:"disk_stats_available"`
		Failures       uint64 `json:"capture_failures"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.UsageAvailable || status.DiskAvailable || status.Failures == 0 {
		t.Fatalf("blocked storage was not reported: %s", w.Body.String())
	}
}

func TestPrivateLogMediaCanBeDownloaded(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	dir := t.TempDir()
	name := "request-019a1234-1234-7123-8123-123456789abc.log"
	media := []byte("synthetic image bytes")
	encoded := base64.StdEncoding.EncodeToString(media)
	content := `{"mimeType":"image/png","data":"` + encoded + `"}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{}, logDir: dir}
	r := gin.New()
	r.GET("/requests/:name/preview", h.PreviewPrivateRequestLog)
	r.GET("/requests/:name/media", h.DownloadPrivateLogMedia)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/requests/"+name+"/preview", nil))
	var page struct {
		Media []privateMediaRef `json:"media"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Media) != 1 {
		t.Fatalf("media not detected: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/requests/"+name+"/media?offset="+strconv.FormatInt(page.Media[0].Offset, 10), nil))
	if w.Code != 200 || w.Body.String() != string(media) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("media download failed: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/requests/"+name+"/media?offset=1", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid media offset returned %d", w.Code)
	}
}
