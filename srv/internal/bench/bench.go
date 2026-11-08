package bench

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/hangtiancheng/wanproxy/srv/internal/payload"
)

type Result struct {
	DurationMs   float64 `json:"durationMs"`
	ThroughputMB float64 `json:"throughputMBps"`
	SHA256       string  `json:"sha256,omitempty"`
	Expected     string  `json:"expected,omitempty"`
	OK           bool    `json:"ok"`
	Error        string  `json:"error,omitempty"`
}

func Run(kind, base, proxy, target, direction string, pattern payload.Pattern, size, seed int64) Result {
	var res Result
	start := time.Now()
	var sha string
	var err error
	switch kind {
	case "http":
		sha, err = benchHTTP(base, direction, pattern, size, seed)
	case "socks":
		sha, err = benchSOCKS(proxy, target, direction, pattern, size, seed)
	case "ssh":
		sha, err = benchSSH(proxy, pattern, size, seed)
	default:
		err = fmt.Errorf("unknown scenario %q", kind)
	}
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	if res.DurationMs > 0 {
		res.ThroughputMB = float64(size) / (1 << 20) / (res.DurationMs / 1000.0)
	}
	res.SHA256 = sha
	if err != nil {
		res.OK = false
		res.Error = err.Error()
		return res
	}
	expected, xerr := payload.SHA256(pattern, size, seed)
	if xerr != nil {
		res.OK = false
		res.Error = xerr.Error()
		return res
	}
	res.Expected = expected
	if sha != expected {
		res.OK = false
		if direction == "upload" {
			res.Error = "sink reported a different SHA-256"
		} else {
			res.Error = "payload SHA-256 mismatch"
		}
		return res
	}
	res.OK = true
	return res
}

func benchHTTP(base, direction string, pattern payload.Pattern, size, seed int64) (string, error) {
	if base == "" {
		return "", fmt.Errorf("http scenario requires a base URL")
	}
	client := &http.Client{Timeout: 0}
	return benchHTTPWith(client, base, direction, pattern, size, seed)
}

func benchHTTPWith(client *http.Client, base, direction string, pattern payload.Pattern, size, seed int64) (string, error) {
	var lastSHA string
	if direction == "upload" || direction == "roundtrip" {
		sha, err := httpUpload(client, base, pattern, size, seed)
		if err != nil {
			return "", err
		}
		lastSHA = sha
	}
	if direction == "download" || direction == "roundtrip" {
		sha, err := httpDownload(client, base, pattern, size, seed)
		if err != nil {
			return "", err
		}
		lastSHA = sha
	}
	return lastSHA, nil
}

func httpUpload(client *http.Client, base string, pattern payload.Pattern, size, seed int64) (string, error) {
	pr := payload.NewReader(pattern, size, seed)
	h := sha256.New()
	req, err := http.NewRequest("POST", base+"/api/bench/sink", io.NopCloser(payload.NewHashingReader(pr, h)))
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("sink status %d: %s", resp.StatusCode, body)
	}
	var sink struct {
		Bytes int64  `json:"bytes"`
		SHA   string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &sink); err != nil {
		return "", fmt.Errorf("sink response: %w", err)
	}
	if sink.Bytes != size {
		return "", fmt.Errorf("sink received %d bytes, want %d", sink.Bytes, size)
	}
	return sink.SHA, nil
}

func httpDownload(client *http.Client, base string, pattern payload.Pattern, size, seed int64) (string, error) {
	url := fmt.Sprintf("%s/api/bench/source?size=%d&seed=%d&pattern=%s", base, size, seed, pattern)
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("source status %d", resp.StatusCode)
	}
	h := sha256.New()
	n, err := io.Copy(h, resp.Body)
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("downloaded %d bytes, want %d", n, size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func benchSOCKS(proxyAddr, target, direction string, pattern payload.Pattern, size, seed int64) (string, error) {
	if proxyAddr == "" || target == "" {
		return "", fmt.Errorf("socks scenario requires a proxy and target")
	}
	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_ = addr
				return dialSOCKS5(proxyAddr, target)
			},
			DisableKeepAlives: false,
		},
	}
	base := "http://" + target
	return benchHTTPWith(client, base, direction, pattern, size, seed)
}

func dialSOCKS5(proxyAddr, target string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		conn.Close()
		return nil, err
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks greeting % x", greeting)
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		conn.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		conn.Close()
		return nil, err
	}

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 0x01)
			req = append(req, v4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(port))
	req = append(req, pb[:]...)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	resp := make([]byte, 4)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks connect response % x", resp)
	}
	switch resp[3] {
	case 0x01:
		_, _ = io.ReadFull(conn, make([]byte, 4+2))
	case 0x03:
		nb := make([]byte, 1)
		_, _ = io.ReadFull(conn, nb)
		_, _ = io.ReadFull(conn, make([]byte, int(nb[0])+2))
	case 0x04:
		_, _ = io.ReadFull(conn, make([]byte, 16+2))
	default:
		conn.Close()
		return nil, fmt.Errorf("socks address type %d", resp[3])
	}

	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
