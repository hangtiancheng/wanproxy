// Package server wires the yukino_http application: global CORS, the
// root health endpoint, and the /api routes backed by the files,
// monitor, bench and tools services.  It is the Go port of the NestJS
// controllers under src/**.
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	yukino "github.com/hangtiancheng/yukino.go/libs/yukino_http"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/benchsvc"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/files"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/monitor"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/payload"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/tools"
)

const maxTransferSize = 4 * 1024 * 1024 * 1024

// DaemonController restarts the demo fleet; implemented by
// internal/daemon.Daemon.
type DaemonController interface {
	Restart(clearCache bool) error
}

// Server holds the service dependencies used by the HTTP handlers.
type Server struct {
	files   *files.Service
	monitor *monitor.Service
	bench   *benchsvc.Service
	tools   *tools.Service
	daemon  DaemonController
}

// New builds the yukino application with all routes and middleware
// registered.  Registration must complete before serving.
func New(f *files.Service, m *monitor.Service, b *benchsvc.Service, t *tools.Service, d DaemonController) *yukino.Application {
	s := &Server{files: f, monitor: m, bench: b, tools: t, daemon: d}
	app := yukino.New()
	app.Use(httpLog, cors, yukino.Recovery())

	app.Get("/", s.root)

	api := app.Router("/api")
	api.Get("/files", s.listFiles)
	api.Post("/files/generate", s.generate)
	api.Post("/files/upload", s.upload)
	api.Get("/files/:id/content", s.content)
	api.Delete("/files/:id", s.removeFile)

	api.Get("/monitor/status", s.monitorStatus)
	api.Get("/monitor/scenarios", s.scenarios)

	api.Get("/bench/scenarios", s.scenarios)
	api.Post("/bench/run", s.benchRun)
	api.Post("/bench/experiment", s.experiment)
	api.Post("/bench/sink", s.sink)
	api.Get("/bench/source", s.source)

	api.Post("/daemon/restart", s.restartDaemon)

	api.Post("/tools/analyze", s.analyze)
	return app
}

// ---------------------------------------------------------------------
// middleware
// ---------------------------------------------------------------------

func httpLog(ctx *yukino.Context, next func()) {
	start := time.Now()
	next()
	log.Printf("[http] %d %s %s in %s", ctx.Status, ctx.Method, ctx.Request.RequestURI,
		time.Since(start).Round(time.Microsecond))
}

// cors reflects the request Origin and answers preflight OPTIONS with
// 204.  Headers are applied both before and after the downstream chain
// so they survive yukino.Recovery clearing the buffered header map on a
// panic.  After a takeover the re-apply is a harmless no-op.
func cors(ctx *yukino.Context, next func()) {
	apply := func() {
		if origin := ctx.Get("Origin"); origin != "" {
			ctx.Set("Access-Control-Allow-Origin", origin)
		}
		ctx.Set("Vary", "Origin")
		ctx.Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		ctx.Set("Access-Control-Allow-Headers", "Content-Type,Range")
		ctx.Set("Access-Control-Expose-Headers", "Content-Range,Content-Length,Accept-Ranges")
	}
	apply()
	if ctx.Method == http.MethodOptions {
		ctx.SetStatus(http.StatusNoContent)
		return
	}
	next()
	apply()
}

func errJSON(ctx *yukino.Context, status int, msg string) {
	ctx.SetStatus(status)
	ctx.JSON(yukino.H{"error": msg})
}

// ---------------------------------------------------------------------
// root
// ---------------------------------------------------------------------

func (s *Server) root(ctx *yukino.Context, next func()) {
	ctx.JSON(yukino.H{"service": "wanproxy-server", "status": "ok"})
}

// ---------------------------------------------------------------------
// files
// ---------------------------------------------------------------------

func (s *Server) listFiles(ctx *yukino.Context, next func()) {
	ctx.JSON(s.files.List())
}

func (s *Server) generate(ctx *yukino.Context, next func()) {
	var body struct {
		Name    *string `json:"name"`
		Size    *int64  `json:"size"`
		Pattern *string `json:"pattern"`
		Seed    *int64  `json:"seed"`
	}
	if err := ctx.BindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		errJSON(ctx, http.StatusBadRequest, "invalid JSON")
		return
	}
	patternStr := "mixed"
	if body.Pattern != nil {
		patternStr = *body.Pattern
	}
	pattern, err := payload.ParsePattern(patternStr)
	if err != nil {
		errJSON(ctx, http.StatusBadRequest, err.Error())
		return
	}
	size := int64(16 * 1024 * 1024)
	if body.Size != nil {
		size = *body.Size
	}
	if size < 1 || size > maxTransferSize {
		errJSON(ctx, http.StatusBadRequest, "invalid size")
		return
	}
	seed := int64(42)
	if body.Seed != nil {
		seed = *body.Seed
	}
	name := fmt.Sprintf("test-%s.bin", patternStr)
	if body.Name != nil {
		name = *body.Name
	}
	meta, err := s.files.Generate(name, size, pattern, seed)
	if err != nil {
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.SetStatus(http.StatusCreated)
	ctx.JSON(meta)
}

func (s *Server) upload(ctx *yukino.Context, next func()) {
	name := ctx.Query("name")
	if name == "" {
		name = "upload.bin"
	}
	meta, err := s.files.Store(name, ctx.Request.Body)
	if err != nil {
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.SetStatus(http.StatusCreated)
	ctx.JSON(meta)
}

func (s *Server) removeFile(ctx *yukino.Context, next func()) {
	id := ctx.Param("id")
	if err := s.files.Remove(id); err != nil {
		if errors.Is(err, files.ErrNotFound) {
			errJSON(ctx, http.StatusNotFound, err.Error())
			return
		}
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.SetStatus(http.StatusNoContent)
}

var rangeRe = regexp.MustCompile(`bytes=(\d*)-(\d*)`)

func (s *Server) content(ctx *yukino.Context, next func()) {
	id := ctx.Param("id")
	meta, err := s.files.Get(id)
	if err != nil {
		errJSON(ctx, http.StatusNotFound, err.Error())
		return
	}
	filePath := s.files.FilePath(id)
	st, err := os.Stat(filePath)
	if err != nil {
		errJSON(ctx, http.StatusNotFound, fmt.Sprintf("file %s not found", id))
		return
	}
	total := st.Size()

	f, err := os.Open(filePath)
	if err != nil {
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	ctx.Set("Accept-Ranges", "bytes")
	ctx.Set("Content-Type", s.files.ContentType(meta.Name))
	ctx.Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", encodeURIComponent(meta.Name)))

	start, end := int64(0), total-1
	status := http.StatusOK
	if rangeHdr := ctx.Get("Range"); rangeHdr != "" {
		if m := rangeRe.FindStringSubmatch(rangeHdr); m != nil {
			if m[1] != "" {
				start, _ = strconv.ParseInt(m[1], 10, 64)
			} else {
				start = 0
			}
			if m[2] != "" {
				end, _ = strconv.ParseInt(m[2], 10, 64)
			} else {
				end = total - 1
			}
		}
		if start >= total || end >= total || start > end {
			ctx.TakeOver()
			ctx.SetStatus(http.StatusRequestedRangeNotSatisfiable)
			ctx.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
			ctx.Writer.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		status = http.StatusPartialContent
	}

	ctx.TakeOver()
	ctx.SetStatus(status)
	w := ctx.Writer
	if status == http.StatusPartialContent {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(status)
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return
		}
		_, _ = io.CopyN(w, f, end-start+1)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
	w.WriteHeader(status)
	_, _ = io.Copy(w, f)
}

// ---------------------------------------------------------------------
// monitor
// ---------------------------------------------------------------------

func (s *Server) monitorStatus(ctx *yukino.Context, next func()) {
	raw, err := s.monitor.StatusRaw()
	if err != nil {
		errJSON(ctx, http.StatusBadGateway, err.Error())
		return
	}
	ctx.Set("Content-Type", "application/json")
	ctx.SetStatus(http.StatusOK)
	ctx.Data(raw)
}

func (s *Server) scenarios(ctx *yukino.Context, next func()) {
	ctx.JSON(monitor.Scenarios())
}

// ---------------------------------------------------------------------
// bench
// ---------------------------------------------------------------------

func (s *Server) benchRun(ctx *yukino.Context, next func()) {
	var req benchsvc.Request
	if err := ctx.BindJSON(&req); err != nil {
		errJSON(ctx, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := s.bench.Run(req)
	if err != nil {
		if errors.Is(err, benchsvc.ErrAlreadyRunning) {
			errJSON(ctx, http.StatusConflict, err.Error())
			return
		}
		errJSON(ctx, http.StatusBadRequest, err.Error())
		return
	}
	ctx.JSON(result)
}

// experiment runs a full A/B experiment: every arm transfers the same
// payload the requested number of times and the aggregates are compared
// against the baseline arm.
func (s *Server) experiment(ctx *yukino.Context, next func()) {
	var req benchsvc.ExperimentRequest
	if err := ctx.BindJSON(&req); err != nil {
		errJSON(ctx, http.StatusBadRequest, "invalid JSON")
		return
	}
	report, err := s.bench.RunExperiment(req)
	if err != nil {
		switch {
		case errors.Is(err, benchsvc.ErrAlreadyRunning):
			errJSON(ctx, http.StatusConflict, err.Error())
		case errors.As(err, new(*benchsvc.ErrUnknownScenario)):
			errJSON(ctx, http.StatusNotFound, err.Error())
		default:
			errJSON(ctx, http.StatusBadRequest, err.Error())
		}
		return
	}
	ctx.JSON(report)
}

// restartDaemon restarts the wanproxy fleet, optionally deleting the
// XCDFS volume so the next experiment starts with a cold dedup cache.
func (s *Server) restartDaemon(ctx *yukino.Context, next func()) {
	var body struct {
		ClearCache *bool `json:"clearCache"`
	}
	if err := ctx.BindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		errJSON(ctx, http.StatusBadRequest, "invalid JSON")
		return
	}
	if s.bench.Busy() {
		errJSON(ctx, http.StatusConflict, "a benchmark or experiment is running")
		return
	}
	clearCache := body.ClearCache != nil && *body.ClearCache
	if s.daemon == nil {
		errJSON(ctx, http.StatusNotImplemented, "daemon control is not available")
		return
	}
	if err := s.daemon.Restart(clearCache); err != nil {
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.JSON(yukino.H{"ok": true, "clearedCache": clearCache})
}

func (s *Server) sink(ctx *yukino.Context, next func()) {
	h := sha256.New()
	n, err := io.Copy(h, ctx.Request.Body)
	if err != nil {
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.JSON(yukino.H{"bytes": n, "sha256": hex.EncodeToString(h.Sum(nil))})
}

func (s *Server) source(ctx *yukino.Context, next func()) {
	sizeStr := ctx.Query("size")
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || size < 0 || size > maxTransferSize {
		errJSON(ctx, http.StatusBadRequest, "invalid size")
		return
	}
	patternStr := ctx.Query("pattern")
	if patternStr == "" {
		patternStr = "mixed"
	}
	pattern, err := payload.ParsePattern(patternStr)
	if err != nil {
		errJSON(ctx, http.StatusBadRequest, err.Error())
		return
	}
	seed := int64(42)
	if seedStr := ctx.Query("seed"); seedStr != "" {
		if v, e := strconv.ParseInt(seedStr, 10, 64); e == nil {
			seed = v
		}
	}
	ctx.Set("Content-Type", "application/octet-stream")
	ctx.Set("Content-Length", strconv.FormatInt(size, 10))
	ctx.TakeOver()
	ctx.SetStatus(http.StatusOK)
	ctx.Writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(ctx.Writer, payload.NewReader(pattern, size, seed))
}

// ---------------------------------------------------------------------
// tools
// ---------------------------------------------------------------------

func (s *Server) analyze(ctx *yukino.Context, next func()) {
	var body struct {
		FileID string `json:"fileId"`
	}
	if err := ctx.BindJSON(&body); err != nil {
		errJSON(ctx, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := s.tools.Analyze(body.FileID)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			errJSON(ctx, http.StatusNotFound, err.Error())
			return
		}
		errJSON(ctx, http.StatusInternalServerError, err.Error())
		return
	}
	ctx.JSON(result)
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// encodeURIComponent mirrors the JavaScript function of the same name:
// every byte except A-Z a-z 0-9 - _ . ! ~ * ' ( ) is percent-encoded.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isURIUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", c))
	}
	return b.String()
}

func isURIUnreserved(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.' || c == '!' || c == '~' || c == '*' || c == '\'' || c == '(' || c == ')':
		return true
	}
	return false
}
