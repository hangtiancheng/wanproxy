package tools

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hangtiancheng/wanproxy/srv/internal/files"
	"github.com/hangtiancheng/wanproxy/srv/internal/paths"
)

type HashCount struct {
	Hash  string `json:"hash"`
	Count int    `json:"count"`
}

type Frame struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

type FrameSummary struct {
	Extract     int         `json:"extract"`
	Ref         int         `json:"ref"`
	Escape      int         `json:"escape"`
	Data        int         `json:"data"`
	DataBytes   int         `json:"dataBytes"`
	TopHashes   []HashCount `json:"topHashes"`
	FirstFrames []Frame     `json:"firstFrames"`
}

type AnalyzeResult struct {
	File       FileRef      `json:"file"`
	Inband     Inband       `json:"inband"`
	Dictionary Dictionary   `json:"dictionary"`
	Frames     FrameSummary `json:"frames"`
}

type FileRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Inband struct {
	CompressedSize int64   `json:"compressedSize"`
	Ratio          float64 `json:"ratio"`
	Stats          string  `json:"stats"`
}

type Dictionary struct {
	FirstPassSize  int64   `json:"firstPassSize"`
	SecondPassSize int64   `json:"secondPassSize"`
	Ratio          float64 `json:"ratio"`
}

type Service struct {
	files *files.Service
}

func NewService(f *files.Service) *Service { return &Service{files: f} }

var (
	hashRe = regexp.MustCompile(`hash="0x([0-9a-f]+)"`)
	dataRe = regexp.MustCompile(`data="([0-9a-f]*)"`)
	kindRe = regexp.MustCompile(`^<([a-z-]+)`)
)

func (s *Service) Analyze(fileID string) (*AnalyzeResult, error) {
	meta, err := s.files.Get(fileID)
	if err != nil {
		return nil, err
	}
	src := s.files.FilePath(fileID)
	workDir := filepath.Join(paths.ToolsDir, fileID)
	_ = os.RemoveAll(workDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)

	tack := paths.TackBin
	xcdump := paths.XcdumpBin
	inbandPath := filepath.Join(workDir, "inband.tack")
	dictPath := filepath.Join(workDir, "dict.cache")
	dict1Path := filepath.Join(workDir, "dict1.tack")
	dict2Path := filepath.Join(workDir, "dict2.tack")

	inbandStats, err := runTack(tack, []string{"-c", "-s", src}, inbandPath)
	if err != nil {
		return nil, err
	}
	inbandSize, err := fileSize(inbandPath)
	if err != nil {
		return nil, err
	}

	if _, err := runTack(tack, []string{"-c", "-Q", "-p", dictPath, src}, ""); err != nil {
		return nil, err
	}
	if _, err := runTack(tack, []string{"-c", "-p", dictPath, src}, dict1Path); err != nil {
		return nil, err
	}
	if _, err := runTack(tack, []string{"-c", "-p", dictPath, src}, dict2Path); err != nil {
		return nil, err
	}
	dict1Size, err := fileSize(dict1Path)
	if err != nil {
		return nil, err
	}
	dict2Size, err := fileSize(dict2Path)
	if err != nil {
		return nil, err
	}

	dump, err := runXcdump(xcdump, inbandPath)
	if err != nil {
		return nil, err
	}
	frames := parseDump(dump)

	var inbandRatio, dictRatio float64
	if meta.Size > 0 {
		inbandRatio = float64(inbandSize) / float64(meta.Size)
		dictRatio = float64(dict2Size) / float64(meta.Size)
	}
	return &AnalyzeResult{
		File: FileRef{ID: meta.ID, Name: meta.Name, Size: meta.Size},
		Inband: Inband{
			CompressedSize: inbandSize,
			Ratio:          inbandRatio,
			Stats:          inbandStats,
		},
		Dictionary: Dictionary{
			FirstPassSize:  dict1Size,
			SecondPassSize: dict2Size,
			Ratio:          dictRatio,
		},
		Frames: frames,
	}, nil
}

func fileSize(p string) (int64, error) {
	st, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func runTack(tack string, args []string, outFile string) (string, error) {
	cmd := exec.Command(tack, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if outFile == "" {
		cmd.Stdout = io.Discard
		if err := cmd.Run(); err != nil {
			return stderr.String(), fmt.Errorf("tack %s failed: %v: %s", strings.Join(args, " "), err, stderr.String())
		}
		return stderr.String(), nil
	}
	f, err := os.Create(outFile)
	if err != nil {
		return "", err
	}
	cmd.Stdout = f
	runErr := cmd.Run()
	closeErr := f.Close()
	if runErr != nil {
		return stderr.String(), fmt.Errorf("tack %s failed: %v: %s", strings.Join(args, " "), runErr, stderr.String())
	}
	if closeErr != nil {
		return stderr.String(), closeErr
	}
	return stderr.String(), nil
}

func runXcdump(xcdump, inbandPath string) (string, error) {
	cmd := exec.Command(xcdump, "-v", inbandPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("xcdump failed: %v: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func parseDump(dump string) FrameSummary {
	summary := FrameSummary{
		TopHashes:   []HashCount{},
		FirstFrames: []Frame{},
	}
	counts := make(map[string]int)
	var order []string
	bump := func(h string) {
		if _, ok := counts[h]; !ok {
			order = append(order, h)
		}
		counts[h]++
	}

	for _, line := range strings.Split(dump, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "<escape"):
			summary.Escape++
		case strings.HasPrefix(trimmed, "<hash-declare"):
			summary.Extract++
			if m := hashRe.FindStringSubmatch(trimmed); m != nil {
				bump(m[1])
			}
		case strings.HasPrefix(trimmed, "<hash-reference"):
			summary.Ref++
			if m := hashRe.FindStringSubmatch(trimmed); m != nil {
				bump(m[1])
			}
		case strings.HasPrefix(trimmed, "<back-reference"):
			summary.Ref++
		case strings.HasPrefix(trimmed, "<data"):
			summary.Data++
			if m := dataRe.FindStringSubmatch(trimmed); m != nil {
				summary.DataBytes += len(m[1]) / 2
			}
		}
		if len(summary.FirstFrames) < 24 {
			kind := "?"
			if m := kindRe.FindStringSubmatch(trimmed); m != nil {
				kind = m[1]
			}
			f := Frame{Kind: kind}
			if m := hashRe.FindStringSubmatch(trimmed); m != nil {
				h := m[1]
				if len(h) > 12 {
					h = h[:12]
				}
				f.Detail = h + "\u2026"
			}
			summary.FirstFrames = append(summary.FirstFrames, f)
		}
	}

	list := make([]HashCount, 0, len(order))
	for _, h := range order {
		list = append(list, HashCount{Hash: h, Count: counts[h]})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Count > list[j].Count })
	if len(list) > 8 {
		list = list[:8]
	}
	summary.TopHashes = list
	return summary
}
