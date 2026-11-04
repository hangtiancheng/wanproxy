module github.com/hangtiancheng/wanproxy/srv.go

go 1.26.0

replace github.com/hangtiancheng/wanproxy/wanproxy.go => ../wanproxy.go

require github.com/hangtiancheng/wanproxy/wanproxy.go v0.0.0-00010101000000-000000000000

require github.com/hangtiancheng/yukino.go/libs/yukino_http v0.0.1
