package monitor

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/config"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
)

type fakeSource struct{}

func (f *fakeSource) Objects() []config.Object {
	return []config.Object{
		{
			Name:  "codec0",
			Class: "codec",
			Fields: []config.ObjectField{
				{Name: "codec", Type: "codec", Value: "XCodec"},
				{Name: "cache", Type: "pointer", Value: "cache0"},
			},
		},
		{
			Name:  "cache0",
			Class: "cache",
			Fields: []config.ObjectField{
				{Name: "type", Type: "cache", Value: "Pair"},
			},
		},
	}
}

func (f *fakeSource) Status() proxy.FleetStatus {
	return proxy.FleetStatus{
		GeneratedAt: time.Now(),
		Proxies: []interface{}{
			proxy.TcpProxyStatus{
				Kind: "tcp",
				Name: "proxy0",
				PeerCodec: &proxy.CodecStatus{
					Mode:            "zlib+xcodec",
					TrackStatistics: true,
					Stats:           &pipeline.Snapshot{IncomingToCodec: 100, CodecToOutgoing: 10},
				},
				Listening:           true,
				AcceptedConnections: 3,
			},
			proxy.SocksProxyStatus{
				Kind:      "socks",
				Name:      "socks0",
				Listening: true,
			},
		},
	}
}

func TestMonitorEndpoints(t *testing.T) {
	srv := NewServer(proxy.Endpoint{Family: proxy.FamilyIP, Host: "127.0.0.1", Port: "0"}, &fakeSource{})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()
	addr := srv.Address()
	base := "http://127.0.0.1:" + portString(addr.Port)

	t.Run("status json", func(t *testing.T) {
		resp, err := http.Get(base + "/status")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("content-type = %s", ct)
		}
		var status proxy.FleetStatus
		body, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(body, &status); err != nil {
			t.Fatalf("json: %v (%s)", err, body)
		}
	})

	t.Run("html root", func(t *testing.T) {
		resp, err := http.Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "WANProxy Monitor") {
			t.Fatalf("html root: %d %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "codec0") || !strings.Contains(string(body), "<a href=\"/object/cache0\">cache0</a>") {
			t.Fatalf("html root missing object links: %s", body)
		}
	})

	t.Run("object select", func(t *testing.T) {
		resp, err := http.Get(base + "/object/codec0")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "codec0") {
			t.Fatalf("object: %d %s", resp.StatusCode, body)
		}
		if strings.Contains(string(body), "<a href=\"/object/cache0\">cache0</a></td><td>cache</td>") {
			t.Fatalf("object select should hide other objects: %s", body)
		}
		if !strings.Contains(string(body), "<a href=\"/object/cache0\">cache0</a>") {
			t.Fatalf("pointer field should link to cache0: %s", body)
		}
	})

	t.Run("text export", func(t *testing.T) {
		resp, err := http.Get(base + "/:text")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.Contains(string(body), " object: codec0; class: codec;") {
			t.Fatalf("text: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("not found", func(t *testing.T) {
		resp, err := http.Get(base + "/bogus/path/here")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("bogus path = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		resp, err := http.Post(base+"/", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("post = %d, want 400", resp.StatusCode)
		}
	})
}

func portString(port int) string {
	if port == 0 {
		return "0"
	}
	digits := ""
	for port > 0 {
		digits = string(rune('0'+port%10)) + digits
		port /= 10
	}
	return digits
}
