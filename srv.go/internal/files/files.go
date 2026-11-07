package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hangtiancheng/wanproxy/srv.go/internal/paths"
	"github.com/hangtiancheng/wanproxy/srv.go/internal/payload"
)

type FileMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Pattern   string `json:"pattern,omitempty"`
	Seed      int64  `json:"seed,omitempty"`
	CreatedAt string `json:"createdAt"`
}

var ErrNotFound = errors.New("not found")

var contentTypes = map[string]string{
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".txt":  "text/plain",
	".log":  "text/plain",
	".json": "application/json",
	".bin":  "application/octet-stream",
}

type Service struct {
	mu    sync.Mutex
	metas []FileMeta
}

func NewService() *Service {
	paths.EnsureDirs()
	s := &Service{metas: []FileMeta{}}
	if data, err := os.ReadFile(paths.MetaPath); err == nil {
		var metas []FileMeta
		if err := json.Unmarshal(data, &metas); err == nil && metas != nil {
			s.metas = metas
		}
	}
	return s
}

func (s *Service) saveMetas() {
	data, err := json.MarshalIndent(s.metas, "", "  ")
	if err != nil {
		log.Printf("[files] marshal metas: %v", err)
		return
	}
	if err := os.WriteFile(paths.MetaPath, data, 0o644); err != nil {
		log.Printf("[files] write metas: %v", err)
	}
}

func (s *Service) List() []FileMeta {
	s.mu.Lock()
	out := make([]FileMeta, len(s.metas))
	copy(out, s.metas)
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

func (s *Service) Get(id string) (FileMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.metas {
		if m.ID == id {
			return m, nil
		}
	}
	return FileMeta{}, fmt.Errorf("file %s %w", id, ErrNotFound)
}

func (s *Service) FilePath(id string) string {
	return filepath.Join(paths.FilesDir, id)
}

func (s *Service) ContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t, ok := contentTypes[ext]; ok {
		return t
	}
	return "application/octet-stream"
}

func uuidv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (s *Service) Store(name string, body io.Reader) (FileMeta, error) {
	return s.write(name, body, "", 0, false)
}

func (s *Service) Generate(name string, size int64, pattern payload.Pattern, seed int64) (FileMeta, error) {
	return s.write(name, payload.NewReader(pattern, size, seed), string(pattern), seed, true)
}

func (s *Service) write(name string, src io.Reader, pattern string, seed int64, hasPattern bool) (FileMeta, error) {
	id, err := uuidv4()
	if err != nil {
		return FileMeta{}, fmt.Errorf("generate id: %w", err)
	}
	paths.EnsureDirs()
	dest := s.FilePath(id)
	f, err := os.Create(dest)
	if err != nil {
		return FileMeta{}, err
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		return FileMeta{}, err
	}
	meta := FileMeta{
		ID:        id,
		Name:      name,
		Size:      size,
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		CreatedAt: nowISO(),
	}
	if hasPattern {
		meta.Pattern = pattern
		meta.Seed = seed
	}

	s.mu.Lock()
	s.metas = append(s.metas, meta)
	s.saveMetas()
	s.mu.Unlock()

	if hasPattern {
		log.Printf("[files] generated %s (%d bytes, %s) as %s", name, size, pattern, id)
	} else {
		log.Printf("[files] stored %s (%d bytes) as %s", name, size, id)
	}
	return meta, nil
}

func (s *Service) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, m := range s.metas {
		if m.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("file %s %w", id, ErrNotFound)
	}
	meta := s.metas[idx]
	dest := s.FilePath(id)
	if _, err := os.Stat(dest); err == nil {
		_ = os.Remove(dest)
	}
	s.metas = append(s.metas[:idx], s.metas[idx+1:]...)
	s.saveMetas()
	log.Printf("[files] removed %s (%s)", meta.Name, id)
	return nil
}
