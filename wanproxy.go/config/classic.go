package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy.go/ssh"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

// classicObject is one created-but-not-yet-activated config object.
type classicObject struct {
	name    string
	class   string
	fields  map[string]string // raw field strings, for $refs and export
	order   []string
	kind    string // set on activation: "cache", "codec", "address", ...
	cache   xcodec.Cache
	codec   *pipeline.CodecSpec
	address *proxy.Endpoint
}

// ClassicParser parses the C++ wanproxy.conf directive format:
//
//	create <class> <name>
//	set <name>.<member> <value>
//	activate <name>
//
// with # comments, quoted strings and $object.member references.
type ClassicParser struct {
	objects map[string]*classicObject
	// fieldStrings mirrors the C++ field_strings_map_ used to resolve
	// $object.member references.
	fieldStrings map[string]string // key: object name + "." + member
	cfg          *Config
	disks        []*xcodec.Disk
	log          *slog.Logger
}

// NewClassicParser returns an empty parser.
func NewClassicParser() *ClassicParser {
	return &ClassicParser{
		objects:      make(map[string]*classicObject),
		fieldStrings: make(map[string]string),
		cfg:          &Config{LogLevel: slog.LevelInfo},
		log:          slog.Default(),
	}
}

// Parse parses a whole wanproxy.conf document.
func (p *ClassicParser) Parse(text string) (*Config, error) {
	for i, line := range strings.Split(text, "\n") {
		tokens := tokenizeLine(line)
		if len(tokens) == 0 {
			continue
		}
		if err := p.parseDirective(tokens); err != nil {
			return nil, fmt.Errorf("line %d: %q: %w", i+1, line, err)
		}
	}
	if !p.cfg.HasProxies() {
		return nil, fmt.Errorf("configuration defines no proxies")
	}
	return p.cfg, nil
}

// tokenizeLine splits a config line into whitespace-separated words,
// stopping at # comments, mirroring the C++ istringstream tokenizer.
func tokenizeLine(line string) []string {
	var tokens []string
	for _, word := range strings.Fields(line) {
		if strings.HasPrefix(word, "#") {
			break
		}
		tokens = append(tokens, word)
	}
	return tokens
}

func (p *ClassicParser) parseDirective(tokens []string) error {
	switch tokens[0] {
	case "create":
		return p.parseCreate(tokens[1:])
	case "set":
		return p.parseSet(tokens[1:])
	case "activate":
		return p.parseActivate(tokens[1:])
	}
	return fmt.Errorf("unrecognized configuration command %q", tokens[0])
}

var classicClasses = map[string]bool{
	"log-mask":    true,
	"cache":       true,
	"codec":       true,
	"interface":   true,
	"peer":        true,
	"proxy":       true,
	"proxy-socks": true,
	"monitor":     true,
}

func (p *ClassicParser) parseCreate(tokens []string) error {
	if len(tokens) != 2 {
		return fmt.Errorf("wrong number of words in create (%d)", len(tokens))
	}
	class, name := tokens[0], tokens[1]
	if !classicClasses[class] {
		return fmt.Errorf("class (%s) not present", class)
	}
	if _, ok := p.objects[name]; ok {
		return fmt.Errorf("refusing to create object (%s) twice", name)
	}
	p.objects[name] = &classicObject{name: name, class: class, fields: make(map[string]string)}
	return nil
}

func (p *ClassicParser) parseSet(tokens []string) error {
	if len(tokens) != 2 {
		return fmt.Errorf("wrong number of words in set (%d)", len(tokens))
	}
	dot := strings.IndexByte(tokens[0], '.')
	if dot < 0 {
		return fmt.Errorf("identifier (%s) is not an object member identifier", tokens[0])
	}
	name, member := tokens[0][:dot], tokens[0][dot+1:]
	if name == "" || member == "" {
		return fmt.Errorf("object (%s) or member name (%s) is empty", name, member)
	}

	value := tokens[1]
	if strings.HasPrefix(value, "$") {
		ref := value[1:]
		refdot := strings.IndexByte(ref, '.')
		if refdot <= 0 || refdot == len(ref)-1 {
			return fmt.Errorf("reference to invalid object or member by name (%s)", value)
		}
		refname, refmember := ref[:refdot], ref[refdot+1:]
		if _, ok := p.objects[refname]; !ok {
			return fmt.Errorf("reference to non-existant object (%s)", refname)
		}
		resolved, ok := p.fieldStrings[refname+"."+refmember]
		if !ok {
			return fmt.Errorf("reference to unset member (%s) in object (%s)", refmember, refname)
		}
		value = resolved
	}

	obj, ok := p.objects[name]
	if !ok {
		return fmt.Errorf("can not set member value on object (%s) that does not exist", name)
	}
	if _, seen := obj.fields[member]; !seen {
		obj.order = append(obj.order, member)
	}
	obj.fields[member] = value
	p.fieldStrings[name+"."+member] = value
	return nil
}

func (p *ClassicParser) parseActivate(tokens []string) error {
	if len(tokens) != 1 {
		return fmt.Errorf("wrong number of words in activate (%d)", len(tokens))
	}
	name := tokens[0]
	obj, ok := p.objects[name]
	if !ok {
		return fmt.Errorf("object (%s) can not be activated as it does not exist", name)
	}
	if err := p.activate(obj); err != nil {
		return fmt.Errorf("object (%s) activation failed: %w", name, err)
	}
	p.cfg.Objects = append(p.cfg.Objects, Object{
		Name:   obj.name,
		Class:  obj.class,
		Fields: objectFields(obj),
	})
	return nil
}

func objectFields(obj *classicObject) []ObjectField {
	fields := make([]ObjectField, 0, len(obj.order))
	for _, member := range obj.order {
		fields = append(fields, ObjectField{Name: member, Type: "string", Value: obj.fields[member]})
	}
	return fields
}

func (o *classicObject) str(member string) string { return o.fields[member] }

func (o *classicObject) pointer(p *ClassicParser, member string) (*classicObject, error) {
	value, ok := o.fields[member]
	if !ok || value == "None" {
		return nil, nil
	}
	target, ok := p.objects[value]
	if !ok {
		return nil, fmt.Errorf("member %s references unknown object %q", member, value)
	}
	return target, nil
}

func (p *ClassicParser) activate(obj *classicObject) error {
	switch obj.class {
	case "log-mask":
		return p.activateLogMask(obj)
	case "cache":
		return p.activateCache(obj)
	case "codec":
		return p.activateCodec(obj)
	case "interface", "peer":
		return p.activateAddress(obj)
	case "proxy":
		return p.activateProxy(obj)
	case "proxy-socks":
		return p.activateProxySocks(obj)
	case "monitor":
		return p.activateMonitor(obj)
	}
	return fmt.Errorf("unknown class %q", obj.class)
}

// activateLogMask mirrors ConfigClassLogMask::Instance::activate: the
// regex is compiled and installed on the shared logger (the C++ version
// calls Log::mask, failing activation when the regex does not compile)
// and the mask is recorded for the daemon and monitor export.
func (p *ClassicParser) activateLogMask(obj *classicObject) error {
	var level slog.Level
	if mask := obj.str("mask"); mask == "" {
		level = slog.LevelInfo
	} else {
		parsed, err := common.ParseLogLevel(mask)
		if err != nil {
			return fmt.Errorf("unknown log mask %q", mask)
		}
		level = parsed
	}

	// Like the C++ activation, a regex that does not compile fails
	// the activation ("Could not set log mask.").
	regex := unquote(obj.str("regex"))
	if err := common.Mask(regex, level); err != nil {
		return fmt.Errorf("could not set log mask: %w", err)
	}

	p.cfg.LogMasks = append(p.cfg.LogMasks, LogMask{
		Name:  obj.name,
		Regex: regex,
		Level: level,
	})
	// The most detailed mask wins for the single-level view.
	if level < p.cfg.LogLevel {
		p.cfg.LogLevel = level
	}
	obj.kind = "log-mask"
	return nil
}

func (p *ClassicParser) activateCache(obj *classicObject) error {
	if obj.kind != "" {
		return fmt.Errorf("already activated")
	}
	cacheType := obj.str("type")
	if cacheType == "" {
		cacheType = "Memory"
	}

	id, err := parseCacheUUID(unquote(obj.str("uuid")))
	if err != nil {
		return err
	}

	switch cacheType {
	case "Memory":
		if obj.str("path") != "" {
			return fmt.Errorf("no path parameter for memory caches")
		}
		if primary, _ := obj.pointer(p, "primary"); primary != nil {
			return fmt.Errorf("specified cache hierarchy for memory cache")
		}
		if secondary, _ := obj.pointer(p, "secondary"); secondary != nil {
			return fmt.Errorf("specified cache hierarchy for memory cache")
		}
		size, err := ParseSize(obj.str("size"))
		if err != nil && obj.str("size") != "" {
			return err
		}
		obj.cache = xcodec.NewMemoryCacheWithBytes(id, size)

	case "Disk":
		if unquote(obj.str("uuid")) != "" {
			return fmt.Errorf("cannot specify UUID for disk cache; a persistent UUID will be allocated")
		}
		path := unquote(obj.str("path"))
		if path == "" {
			return fmt.Errorf("disk cache requires a path")
		}
		size, err := ParseSize(obj.str("size"))
		if err != nil && obj.str("size") != "" {
			return err
		}
		if size == 0 {
			p.log.Info("no disk cache size specified; will attempt to detect from file size", "path", path)
		}
		// OpenDisk tags its logger with the C++ "/xcodec/disk"
		// component itself; hand it the masked base logger.
		disk, err := xcodec.OpenDisk(path, uint64(size), common.Logger())
		if err != nil {
			return fmt.Errorf("could not open disk cache: %w", err)
		}
		p.disks = append(p.disks, disk)
		obj.cache = disk.Local()

	case "Pair":
		if unquote(obj.str("uuid")) != "" {
			return fmt.Errorf("cannot specify UUID for cache pair; secondary UUID will be used")
		}
		if obj.str("size") != "" {
			return fmt.Errorf("no size parameter for cache pair")
		}
		primary, err := obj.pointer(p, "primary")
		if err != nil {
			return err
		}
		secondary, err := obj.pointer(p, "secondary")
		if err != nil {
			return err
		}
		if primary == nil || secondary == nil {
			return fmt.Errorf("cache pair requires both primary and secondary cache")
		}
		if primary.cache == nil {
			return fmt.Errorf("cache (%s) must be activated prior to use as a primary cache", primary.name)
		}
		if secondary.cache == nil {
			return fmt.Errorf("cache (%s) must be activated prior to use as a secondary cache", secondary.name)
		}
		obj.cache = xcodec.NewPairCache(primary.cache, secondary.cache)

	case "None":
		obj.cache = nil

	default:
		return fmt.Errorf("unknown cache type %q", cacheType)
	}

	obj.kind = "cache"
	if obj.cache != nil {
		p.cfg.Caches = append(p.cfg.Caches, obj.cache)
	}
	return nil
}

// parseCacheUUID maps the C++ uuid config member (a 16-character string)
// onto a CacheID.
func parseCacheUUID(s string) (xcodec.CacheID, error) {
	var id xcodec.CacheID
	if s == "" {
		return id, nil
	}
	if len(s) != 16 {
		return id, fmt.Errorf("specified UUID is invalid")
	}
	copy(id[:], s)
	return id, nil
}

func (p *ClassicParser) activateCodec(obj *classicObject) error {
	if obj.kind != "" {
		return fmt.Errorf("already activated")
	}
	spec := &pipeline.CodecSpec{CompressorLevel: -1}

	switch obj.str("codec") {
	case "", "None":
		spec.Mode = pipeline.ModeNone
	case "XCodec":
		spec.Mode = pipeline.ModeXCodec
	default:
		return fmt.Errorf("unknown codec %q", obj.str("codec"))
	}

	compressor := obj.str("compressor")
	switch compressor {
	case "", "None":
		// No compression.
	case "zlib":
		if spec.Mode == pipeline.ModeXCodec {
			spec.Mode = pipeline.ModeZlibXCodec
		} else {
			spec.Mode = pipeline.ModeZlib
		}
	default:
		return fmt.Errorf("unknown compressor %q", compressor)
	}

	if level := obj.str("compressor_level"); level != "" {
		n, err := strconv.Atoi(level)
		if err != nil {
			return fmt.Errorf("invalid compressor_level %q", level)
		}
		spec.CompressorLevel = n
	}

	if spec.Mode == pipeline.ModeXCodec || spec.Mode == pipeline.ModeZlibXCodec {
		cache, err := obj.pointer(p, "cache")
		if err != nil {
			return err
		}
		if cache == nil || cache.cache == nil {
			return fmt.Errorf("codec requires an activated cache")
		}
		spec.Cache = cache.cache
	}

	switch strings.ToLower(obj.str("track_statistics")) {
	case "true", "yes":
		spec.TrackStatistics = true
		spec.Stats = &pipeline.CodecStats{}
	case "", "false", "no":
	default:
		return fmt.Errorf("invalid track_statistics %q", obj.str("track_statistics"))
	}

	obj.codec = spec
	obj.kind = "codec"
	return nil
}

func (p *ClassicParser) activateAddress(obj *classicObject) error {
	if obj.kind != "" {
		return fmt.Errorf("already activated")
	}
	family, err := proxy.ParseFamily(obj.str("family"))
	if err != nil {
		return err
	}
	endpoint := proxy.Endpoint{
		Family: family,
		Host:   unquote(obj.str("host")),
		Port:   unquote(obj.str("port")),
		Path:   unquote(obj.str("path")),
	}
	switch family {
	case proxy.FamilyIP, proxy.FamilyIPv4, proxy.FamilyIPv6:
		if endpoint.Path != "" {
			return fmt.Errorf("IP socket has path field set, which is only valid for Unix domain sockets")
		}
	case proxy.FamilyUnix:
		if endpoint.Host != "" || endpoint.Port != "" {
			return fmt.Errorf("Unix domain socket has host and/or port field set, which is only valid for IP sockets")
		}
	}
	obj.address = &endpoint
	obj.kind = "address"
	return nil
}

func (p *ClassicParser) activateProxy(obj *classicObject) error {
	if obj.kind != "" {
		return fmt.Errorf("already activated")
	}
	proxyType := obj.str("type")
	if proxyType == "" {
		proxyType = "TCP-TCP"
	}
	var sshSettings *SSHProxySettings
	switch proxyType {
	case "TCP", "TCP-TCP":
		if unquote(obj.str("server_host_key")) != "" {
			return fmt.Errorf("server_host_key is only valid for SSH proxies")
		}
	case "SSH", "SSH-SSH":
		keyPath := unquote(obj.str("server_host_key"))
		if keyPath == "" {
			// For historical reasons the C++ version looks for the
			// server host key at this path by default.
			keyPath = "ssh-server1.pem"
		}
		pemData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("could not load server host key %q: %w", keyPath, err)
		}
		hostKey, err := wanproxyssh.LoadRSAPrivateKeyPEM(pemData)
		if err != nil {
			return fmt.Errorf("invalid server host key %q: %w", keyPath, err)
		}
		sshSettings = &SSHProxySettings{HostKey: hostKey, HostKeyPath: keyPath}
	default:
		return fmt.Errorf("unknown proxy type %q", proxyType)
	}

	iface, err := obj.pointer(p, "interface")
	if err != nil {
		return err
	}
	if iface == nil || iface.address == nil {
		return fmt.Errorf("proxy requires an activated interface")
	}
	peer, err := obj.pointer(p, "peer")
	if err != nil {
		return err
	}
	if peer == nil || peer.address == nil {
		return fmt.Errorf("proxy requires an activated peer")
	}
	if iface.address.Host == "" || iface.address.Port == "" {
		return fmt.Errorf("proxy interface endpoint is incomplete")
	}
	if peer.address.Host == "" || peer.address.Port == "" {
		return fmt.Errorf("proxy peer endpoint is incomplete")
	}

	cfg := ProxyConfig{
		Name:      obj.name,
		Interface: *iface.address,
		Peer:      *peer.address,
		SSH:       sshSettings,
	}

	ifaceCodec, err := obj.pointer(p, "interface_codec")
	if err != nil {
		return err
	}
	if ifaceCodec != nil {
		if ifaceCodec.codec == nil {
			return fmt.Errorf("interface_codec must be an activated codec")
		}
		cfg.InterfaceCodec = ifaceCodec.codec
	}
	peerCodec, err := obj.pointer(p, "peer_codec")
	if err != nil {
		return err
	}
	if peerCodec != nil {
		if peerCodec.codec == nil {
			return fmt.Errorf("peer_codec must be an activated codec")
		}
		cfg.PeerCodec = peerCodec.codec
	}

	p.cfg.Proxies = append(p.cfg.Proxies, cfg)
	obj.kind = "proxy"
	return nil
}

func (p *ClassicParser) activateProxySocks(obj *classicObject) error {
	if obj.kind != "" {
		return fmt.Errorf("already activated")
	}
	iface, err := obj.pointer(p, "interface")
	if err != nil {
		return err
	}
	if iface == nil || iface.address == nil {
		return fmt.Errorf("proxy-socks requires an activated interface")
	}
	p.cfg.SocksProxies = append(p.cfg.SocksProxies, SocksConfig{
		Name:      obj.name,
		Interface: *iface.address,
	})
	obj.kind = "proxy-socks"
	return nil
}

func (p *ClassicParser) activateMonitor(obj *classicObject) error {
	if obj.kind != "" {
		return fmt.Errorf("already activated")
	}
	iface, err := obj.pointer(p, "interface")
	if err != nil {
		return err
	}
	if iface == nil || iface.address == nil {
		return fmt.Errorf("monitor requires an activated interface")
	}
	p.cfg.Monitor = &MonitorConfig{
		Name:      obj.name,
		Interface: *iface.address,
	}
	obj.kind = "monitor"
	return nil
}

// unquote strips the surrounding quotes required by the C++ string
// type; values arrive as whole tokens so interior spaces cannot occur.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
