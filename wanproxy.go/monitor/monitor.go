// Package monitor implements the WANProxy monitoring HTTP interface,
// mirroring the C++ MonitorClient: an HTML configuration and statistics
// export at /, per-object views at /object/<name>, plain-text exports
// under the /:text prefix, plus the JSON status endpoint at /status.
package monitor

import (
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/config"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
)

// Source provides the monitor with configuration objects and live fleet
// status.
type Source interface {
	Objects() []config.Object
	Status() proxy.FleetStatus
}

// Server is the monitor HTTP server.
type Server struct {
	iface  proxy.Endpoint
	source Source
	server *http.Server
	ln     net.Listener
}

// NewServer creates a monitor bound to the given interface endpoint.
func NewServer(iface proxy.Endpoint, source Source) *Server {
	s := &Server{iface: iface, source: source}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return s
}

// Start begins serving; it returns once the listen socket is bound.
func (s *Server) Start() error {
	if !s.iface.Valid() {
		return fmt.Errorf("monitor: interface endpoint is incomplete")
	}
	ln, err := net.Listen(s.iface.Network(), s.iface.Addr())
	if err != nil {
		return fmt.Errorf("monitor: %w", err)
	}
	s.ln = ln
	go s.server.Serve(ln)
	return nil
}

// Stop shuts the monitor down.
func (s *Server) Stop() error {
	if s.server == nil {
		return nil
	}
	return s.server.Close()
}

// Address returns the bound address.
func (s *Server) Address() *proxy.BoundAddress {
	if s.ln == nil {
		return nil
	}
	addr, ok := s.ln.Addr().(*net.TCPAddr)
	if !ok {
		return nil
	}
	return &proxy.BoundAddress{Address: addr.IP.String(), Port: addr.Port}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Unsupported method.", http.StatusBadRequest)
		return
	}

	path := r.URL.Path
	if !strings.HasPrefix(path, "/") {
		http.Error(w, "Invalid URI.", http.StatusNotFound)
		return
	}

	if path == "/status" {
		s.handleStatusJSON(w)
		return
	}

	// The C++ monitor serves a text rendering under the /:text prefix.
	text := false
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == ":text" || strings.HasPrefix(trimmed, ":text/") {
		text = true
		trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, ":text"), "/")
	}

	selection := ""
	if trimmed != "" {
		components := strings.Split(trimmed, "/")
		if len(components) == 2 && components[0] == "object" {
			selection = components[1]
		} else {
			http.Error(w, "Unsupported URI.", http.StatusNotFound)
			return
		}
	}

	if text {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, s.exportText(selection))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, "<html><head><title>WANProxy Monitor</title>"+
		"<style type=\"text/css\">body { font-family: sans-serif; } "+
		"td, th { vertical-align: text-top; text-align: left; }</style></head><body>"+
		s.exportHTML(selection)+
		"</body></html>")
}

func (s *Server) handleStatusJSON(w http.ResponseWriter) {
	status := s.source.Status()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(status)
}

func (s *Server) exportHTML(selection string) string {
	var b strings.Builder
	objects := s.source.Objects()
	status := s.source.Status()

	if selection == "" {
		b.WriteString("<h1>Configuration</h1>")
	} else {
		b.WriteString("<h1>Configuration - " + html.EscapeString(selection) + " [<a href=\"/\">up</a>]</h1>")
	}
	b.WriteString("<table>")
	b.WriteString("<tr><th>object</th><th>class</th><th>field</th><th>type</th><th>value</th></tr>")
	for _, obj := range objects {
		if selection != "" && obj.Name != selection {
			continue
		}
		b.WriteString("<tr><td><a href=\"/object/" + html.EscapeString(obj.Name) + "\">" +
			html.EscapeString(obj.Name) + "</a></td><td>" + html.EscapeString(obj.Class) +
			"</td><td colspan=\"3\" /></tr>")
		for _, f := range obj.Fields {
			b.WriteString("<tr><td colspan=\"2\" /><td>" + html.EscapeString(f.Name) +
				"</td><td>" + html.EscapeString(f.Type) + "</td><td>")
			writeHTMLValue(&b, f.Value, isPointerField(objects, obj.Name, f.Name))
			b.WriteString("</td></tr>")
		}
	}
	b.WriteString("</table>")

	if selection == "" {
		b.WriteString("<h1>Status</h1>")
		b.WriteString(statusTableHTML(status))
	}
	return b.String()
}

func isPointerField(objects []config.Object, objName, field string) bool {
	for _, obj := range objects {
		if obj.Name == objName {
			for _, f := range obj.Fields {
				if f.Name == field {
					return f.Type == "pointer"
				}
			}
		}
	}
	return false
}

func writeHTMLValue(b *strings.Builder, value string, pointer bool) {
	if pointer && value != "" && value != "None" {
		b.WriteString("<a href=\"/object/" + html.EscapeString(value) + "\">" + html.EscapeString(value) + "</a>")
		return
	}
	b.WriteString("<tt>" + html.EscapeString(value) + "</tt>")
}

func statusTableHTML(status proxy.FleetStatus) string {
	data, err := json.Marshal(status)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("<table><tr><th>proxy</th><th>kind</th><th>listening</th><th>accepted</th><th>active</th><th>stats</th></tr>")
	for _, entry := range statusEntries(status) {
		b.WriteString("<tr>")
		for _, cell := range entry {
			b.WriteString("<td>" + cell + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	_ = data
	return b.String()
}

func statusEntries(status proxy.FleetStatus) [][]string {
	var rows [][]string
	entries, ok := status.Proxies.([]interface{})
	if !ok {
		return rows
	}
	for _, e := range entries {
		switch p := e.(type) {
		case proxy.TcpProxyStatus:
			rows = append(rows, []string{
				html.EscapeString(p.Name), p.Kind, fmt.Sprintf("%v", p.Listening),
				fmt.Sprintf("%d", p.AcceptedConnections), fmt.Sprintf("%d", p.ActiveSockets),
				html.EscapeString(codecStatsString(p.InterfaceCodec, p.PeerCodec)),
			})
		case proxy.UDPProxyStatus:
			rows = append(rows, []string{
				html.EscapeString(p.Name), p.Kind, fmt.Sprintf("%v", p.Listening),
				fmt.Sprintf("%d", p.CreatedSessions), fmt.Sprintf("%d", p.ActiveSessions),
				fmt.Sprintf("client→peer: %d datagrams / %d bytes; peer→client: %d datagrams / %d bytes; dropped: %d",
					p.ClientToPeerDatagrams, p.ClientToPeerBytes, p.PeerToClientDatagrams, p.PeerToClientBytes, p.DroppedDatagrams),
			})
		case proxy.SocksProxyStatus:
			rows = append(rows, []string{
				html.EscapeString(p.Name), p.Kind, fmt.Sprintf("%v", p.Listening),
				fmt.Sprintf("%d", p.AcceptedConnections), fmt.Sprintf("%d", p.ActiveSockets), "",
			})
		}
	}
	return rows
}

func codecStatsString(iface, peer *proxy.CodecStatus) string {
	var parts []string
	if iface != nil && iface.Stats != nil {
		parts = append(parts, fmt.Sprintf("interface(%s): %+v", iface.Mode, *iface.Stats))
	}
	if peer != nil && peer.Stats != nil {
		parts = append(parts, fmt.Sprintf("peer(%s): %+v", peer.Mode, *peer.Stats))
	}
	return strings.Join(parts, " ")
}

func (s *Server) exportText(selection string) string {
	var b strings.Builder
	objects := s.source.Objects()
	if selection == "" {
		b.WriteString("config:\r\n")
	} else {
		b.WriteString("config " + selection + ":\r\n")
	}
	for _, obj := range objects {
		if selection != "" && obj.Name != selection {
			continue
		}
		b.WriteString(" object: " + obj.Name + "; class: " + obj.Class + ";\r\n")
		for _, f := range obj.Fields {
			b.WriteString("  field: " + f.Name + "; type: " + f.Type + ";\r\n")
			b.WriteString("   value: " + f.Value + ";\r\n")
		}
	}
	if selection == "" {
		status := s.source.Status()
		b.WriteString("status:\r\n")
		for _, row := range statusEntries(status) {
			b.WriteString(" " + strings.Join(row, "; ") + "\r\n")
		}
	}
	return b.String()
}
