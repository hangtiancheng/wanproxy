package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hangtiancheng/wanproxy/wanproxy.go/common"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/pipeline"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/proxy"
	wanproxyssh "github.com/hangtiancheng/wanproxy/wanproxy.go/ssh"
	"github.com/hangtiancheng/wanproxy/wanproxy.go/xcodec"
)

type endpoint struct {
	Family *string `json:"family,omitempty"`
	Host   *string `json:"host,omitempty"`
	Port   *int    `json:"port,omitempty"`
	Path   *string `json:"path,omitempty"`
}

type cacheConfig struct {
	Type      string  `json:"type"`
	Size      *string `json:"size,omitempty"`
	Path      *string `json:"path,omitempty"`
	Primary   *string `json:"primary,omitempty"`
	Secondary *string `json:"secondary,omitempty"`
}

type codecConfig struct {
	Mode            string  `json:"mode"`
	Cache           *string `json:"cache,omitempty"`
	CompressorLevel *int    `json:"compressorLevel,omitempty"`
	TrackStatistics bool    `json:"trackStatistics,omitempty"`
}

type proxyConfig struct {
	Name                    string    `json:"name"`
	Type                    string    `json:"type"`
	Listen                  *endpoint `json:"listen"`
	Upstream                *endpoint `json:"upstream,omitempty"`
	InterfaceCodec          *string   `json:"interfaceCodec,omitempty"`
	PeerCodec               *string   `json:"peerCodec,omitempty"`
	HostKey                 *string   `json:"hostKey,omitempty"`
	HandshakeTimeoutSeconds *int      `json:"handshakeTimeoutSeconds,omitempty"`
	IdleTimeoutSeconds      *int      `json:"idleTimeoutSeconds,omitempty"`
	MaxSessions             *int      `json:"maxSessions,omitempty"`
}

type logMaskConfig struct {
	Regex *string `json:"regex"`
	Level string  `json:"level"`
}

type fileConfig struct {
	Schema *string `json:"$schema,omitempty"`
	Log    *struct {
		Level *string         `json:"level,omitempty"`
		Masks []logMaskConfig `json:"masks,omitempty"`
	} `json:"log,omitempty"`
	Caches  map[string]cacheConfig `json:"caches,omitempty"`
	Codecs  map[string]codecConfig `json:"codecs,omitempty"`
	Proxies []proxyConfig          `json:"proxies"`
	Monitor *struct {
		Listen *endpoint `json:"listen"`
	} `json:"monitor,omitempty"`
}

var configName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Parse(data []byte) (cfg *Config, err error) {
	if err := validateJSON(data); err != nil {
		return nil, fmt.Errorf("invalid JSON configuration: %w", err)
	}
	var file fileConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("invalid JSON configuration: %w", err)
	}
	if len(file.Proxies) == 0 {
		return nil, fmt.Errorf("proxies: at least one proxy is required")
	}
	cfg = &Config{LogLevel: slog.LevelInfo}
	owned := cfg
	defer func() {
		if err != nil {
			_ = owned.Close()
		}
	}()
	if file.Log != nil {
		if file.Log.Level != nil {
			cfg.LogLevel, err = parseLogLevel(*file.Log.Level)
			if err != nil {
				return nil, err
			}
		}
		for _, mask := range file.Log.Masks {
			level, err := parseLogLevel(mask.Level)
			if err != nil {
				return nil, err
			}
			if mask.Regex == nil {
				return nil, fmt.Errorf("log mask: regex is required")
			}
			if _, err := regexp.Compile(*mask.Regex); err != nil {
				return nil, fmt.Errorf("log mask: %w", err)
			}
			cfg.LogMasks = append(cfg.LogMasks, LogMask{Regex: *mask.Regex, Level: level})
		}
	}
	caches, err := cfg.loadCaches(file.Caches)
	if err != nil {
		return nil, err
	}
	codecs := make(map[string]*pipeline.CodecSpec)
	for _, name := range sortedKeys(file.Codecs) {
		c := file.Codecs[name]
		if !configName.MatchString(name) {
			return nil, fmt.Errorf("invalid codec name %q", name)
		}
		if c.Mode != "none" && c.Mode != "zlib" && c.Mode != "xcodec" && c.Mode != "zlib+xcodec" {
			return nil, fmt.Errorf("codec %q: invalid mode %q", name, c.Mode)
		}
		mode, _ := pipeline.ParseMode(c.Mode)
		spec := &pipeline.CodecSpec{Mode: mode, CompressorLevel: -1, TrackStatistics: c.TrackStatistics}
		if spec.NeedsSession() {
			if c.Cache == nil || caches[*c.Cache] == nil {
				return nil, fmt.Errorf("codec %q: unknown or missing cache", name)
			}
			spec.Cache = caches[*c.Cache]
		} else if c.Cache != nil {
			return nil, fmt.Errorf("codec %q: cache requires an XCodec mode", name)
		}
		if c.CompressorLevel != nil {
			if !spec.NeedsCompressor() {
				return nil, fmt.Errorf("codec %q: compressorLevel requires a zlib mode", name)
			}
			spec.CompressorLevel = *c.CompressorLevel
		}
		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("codec %q: %w", name, err)
		}
		if spec.TrackStatistics {
			spec.Stats = &pipeline.CodecStats{}
		}
		codecs[name] = spec
		cfg.addObject("codec", name, c)
	}
	names := make(map[string]bool)
	for i, p := range file.Proxies {
		if !configName.MatchString(p.Name) || names[p.Name] {
			return nil, fmt.Errorf("proxies[%d]: invalid or duplicate name %q", i, p.Name)
		}
		names[p.Name] = true
		pc, err := loadProxy(p, codecs)
		if err != nil {
			return nil, fmt.Errorf("proxy %q: %w", p.Name, err)
		}
		cfg.Proxies = append(cfg.Proxies, pc)
		cfg.addObject("proxy", p.Name, p)
	}
	if file.Monitor != nil {
		ep, err := loadEndpoint(file.Monitor.Listen, false)
		if err != nil {
			return nil, fmt.Errorf("monitor.listen: %w", err)
		}
		cfg.Monitor = &ep
	}
	return cfg, nil
}

func loadProxy(p proxyConfig, codecs map[string]*pipeline.CodecSpec) (ProxyConfig, error) {
	pc := ProxyConfig{Name: p.Name, Type: p.Type}
	if p.Type != "tcp" && p.Type != "udp" && p.Type != "ssh" && p.Type != "socks" {
		return pc, fmt.Errorf("type must be tcp, udp, ssh or socks")
	}
	var err error
	pc.Listen, err = loadEndpoint(p.Listen, false)
	if err != nil {
		return pc, fmt.Errorf("listen: %w", err)
	}
	if p.Type == "socks" {
		if p.Upstream != nil {
			return pc, fmt.Errorf("SOCKS does not take an upstream")
		}
	} else {
		pc.Upstream, err = loadEndpoint(p.Upstream, true)
		if err != nil {
			return pc, fmt.Errorf("upstream: %w", err)
		}
	}
	if p.Type == "udp" || p.Type == "socks" {
		if p.InterfaceCodec != nil || p.PeerCodec != nil {
			return pc, fmt.Errorf("%s does not support stream codecs", p.Type)
		}
	} else {
		for _, slot := range []struct {
			ref  *string
			dest **pipeline.CodecSpec
		}{{p.InterfaceCodec, &pc.InterfaceCodec}, {p.PeerCodec, &pc.PeerCodec}} {
			if slot.ref != nil {
				if codecs[*slot.ref] == nil {
					return pc, fmt.Errorf("unknown codec %q", *slot.ref)
				}
				*slot.dest = codecs[*slot.ref]
			}
		}
	}
	if p.Type == "ssh" {
		if p.HostKey == nil || *p.HostKey == "" {
			return pc, fmt.Errorf("hostKey is required for SSH")
		}
		data, err := os.ReadFile(*p.HostKey)
		if err != nil {
			return pc, fmt.Errorf("hostKey: %w", err)
		}
		pc.HostKey, err = wanproxyssh.LoadRSAPrivateKeyPEM(data)
		if err != nil {
			return pc, fmt.Errorf("hostKey: %w", err)
		}
	} else if p.HostKey != nil {
		return pc, fmt.Errorf("hostKey is only valid for SSH")
	}
	if p.HandshakeTimeoutSeconds != nil {
		if p.Type != "ssh" && p.Type != "socks" {
			return pc, fmt.Errorf("handshakeTimeoutSeconds is only valid for SSH/SOCKS")
		}
		pc.HandshakeTimeout, err = seconds(*p.HandshakeTimeoutSeconds)
		if err != nil {
			return pc, fmt.Errorf("handshakeTimeoutSeconds: %w", err)
		}
	}
	if p.Type == "udp" {
		if _, err := pc.Listen.UDPNetwork(); err != nil {
			return pc, err
		}
		if _, err := pc.Upstream.UDPNetwork(); err != nil {
			return pc, err
		}
		if p.IdleTimeoutSeconds != nil {
			pc.IdleTimeout, err = seconds(*p.IdleTimeoutSeconds)
			if err != nil {
				return pc, fmt.Errorf("idleTimeoutSeconds: %w", err)
			}
		}
		if p.MaxSessions != nil {
			if *p.MaxSessions < 1 || *p.MaxSessions > 65536 {
				return pc, fmt.Errorf("maxSessions must be in 1..65536")
			}
			pc.MaxSessions = *p.MaxSessions
		}
	} else if p.IdleTimeoutSeconds != nil || p.MaxSessions != nil {
		return pc, fmt.Errorf("idleTimeoutSeconds/maxSessions are only valid for UDP")
	}
	return pc, nil
}

func loadEndpoint(e *endpoint, upstream bool) (proxy.Endpoint, error) {
	if e == nil {
		return proxy.Endpoint{}, fmt.Errorf("endpoint is required")
	}
	familyName := "ip"
	if e.Family != nil {
		familyName = *e.Family
	}
	if familyName == "unix" {
		if e.Path == nil || *e.Path == "" || e.Host != nil || e.Port != nil {
			return proxy.Endpoint{}, fmt.Errorf("unix endpoint requires only path")
		}
		return proxy.Endpoint{Family: proxy.FamilyUnix, Path: *e.Path}, nil
	}
	families := map[string]proxy.Family{"ip": proxy.FamilyIP, "ipv4": proxy.FamilyIPv4, "ipv6": proxy.FamilyIPv6}
	family, ok := families[familyName]
	if !ok || e.Host == nil || strings.TrimSpace(*e.Host) == "" || e.Path != nil || e.Port == nil {
		return proxy.Endpoint{}, fmt.Errorf("IP endpoint requires family ip/ipv4/ipv6, host and port")
	}
	minPort := 0
	if upstream {
		minPort = 1
	}
	if *e.Port < minPort || *e.Port > 65535 {
		return proxy.Endpoint{}, fmt.Errorf("port must be in %d..65535", minPort)
	}
	return proxy.Endpoint{Family: family, Host: *e.Host, Port: strconv.Itoa(*e.Port)}, nil
}

func seconds(n int) (time.Duration, error) {
	if n < 1 || n > 86400 {
		return 0, fmt.Errorf("must be in 1..86400")
	}
	return time.Duration(n) * time.Second, nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("invalid log level %q", s)
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (cfg *Config) loadCaches(defs map[string]cacheConfig) (map[string]xcodec.Cache, error) {
	loaded := make(map[string]xcodec.Cache)
	visiting := make(map[string]bool)
	var resolve func(string) (xcodec.Cache, error)
	resolve = func(name string) (xcodec.Cache, error) {
		if cache := loaded[name]; cache != nil {
			return cache, nil
		}
		c, ok := defs[name]
		if !ok || !configName.MatchString(name) {
			return nil, fmt.Errorf("unknown or invalid cache %q", name)
		}
		if visiting[name] {
			return nil, fmt.Errorf("cache %q: cyclic cache hierarchy", name)
		}
		visiting[name] = true
		var cache xcodec.Cache
		var size int64
		if c.Size != nil {
			var err error
			size, err = ParseSize(*c.Size)
			if err != nil {
				return nil, fmt.Errorf("cache %q: %w", name, err)
			}
		}
		switch c.Type {
		case "memory", "disk":
			if c.Primary != nil || c.Secondary != nil {
				return nil, fmt.Errorf("cache %q: primary/secondary require type pair", name)
			}
			if c.Type == "memory" {
				if c.Path != nil {
					return nil, fmt.Errorf("cache %q: path requires type disk", name)
				}
				cache = xcodec.NewMemoryCacheWithBytes(xcodec.CacheID{}, size)
			} else {
				if c.Path == nil || *c.Path == "" {
					return nil, fmt.Errorf("cache %q: disk path is required", name)
				}
				disk, err := xcodec.OpenDisk(*c.Path, uint64(size), common.Logger())
				if err != nil {
					return nil, fmt.Errorf("cache %q: %w", name, err)
				}
				cfg.resources = append(cfg.resources, disk)
				cache = disk.Local()
			}
		case "pair":
			if c.Size != nil || c.Path != nil {
				return nil, fmt.Errorf("cache %q: pair cannot have size/path", name)
			}
			if c.Primary == nil || c.Secondary == nil {
				return nil, fmt.Errorf("cache %q: pair requires primary and secondary", name)
			}
			primary, err := resolve(*c.Primary)
			if err != nil {
				return nil, err
			}
			secondary, err := resolve(*c.Secondary)
			if err != nil {
				return nil, err
			}
			cache = xcodec.NewPairCache(primary, secondary)
		default:
			return nil, fmt.Errorf("cache %q: type must be memory, disk or pair", name)
		}
		loaded[name] = cache
		visiting[name] = false
		cfg.addObject("cache", name, c)
		return cache, nil
	}
	for _, name := range sortedKeys(defs) {
		if _, err := resolve(name); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

func (cfg *Config) addObject(class, name string, value any) {
	data, _ := json.Marshal(value)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	obj := Object{Name: class + "." + name, Class: class}
	for _, key := range sortedKeys(fields) {
		text := string(fields[key])
		kind := "json"
		var str string
		if json.Unmarshal(fields[key], &str) == nil {
			text, kind = str, "string"
		}
		switch key {
		case "cache", "primary", "secondary":
			text, kind = "cache."+text, "pointer"
		case "interfaceCodec", "peerCodec":
			text, kind = "codec."+text, "pointer"
		}
		obj.Fields = append(obj.Fields, ObjectField{Name: key, Type: kind, Value: text})
	}
	cfg.Objects = append(cfg.Objects, obj)
}

func validateJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(reflect.Type) error
	value = func(expected reflect.Type) error {
		for expected.Kind() == reflect.Pointer {
			expected = expected.Elem()
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("null values are not allowed")
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch delim {
		case '{':
			fields := make(map[string]reflect.Type)
			switch expected.Kind() {
			case reflect.Struct:
				for i := 0; i < expected.NumField(); i++ {
					field := expected.Field(i)
					name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
					fields[name] = field.Type
				}
			case reflect.Map:
			default:
				return fmt.Errorf("unexpected object")
			}
			seen := make(map[string]bool)
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key := keyToken.(string)
				if seen[key] {
					return fmt.Errorf("duplicate property %q", key)
				}
				seen[key] = true
				child := fields[key]
				if expected.Kind() == reflect.Map {
					child = expected.Elem()
				}
				if child == nil {
					return fmt.Errorf("unknown property %q", key)
				}
				if err := value(child); err != nil {
					return err
				}
			}
		case '[':
			if expected.Kind() != reflect.Slice {
				return fmt.Errorf("unexpected array")
			}
			for d.More() {
				if err := value(expected.Elem()); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected delimiter %q", delim)
		}
		_, err = d.Token()
		return err
	}
	if err := value(reflect.TypeFor[fileConfig]()); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing content")
	}
	return nil
}
