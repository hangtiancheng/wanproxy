module github.com/hangtiancheng/wanproxy/srv

go 1.27.4

replace github.com/hangtiancheng/wanproxy/wanproxy => ../wanproxy

require github.com/hangtiancheng/wanproxy/wanproxy v0.0.0-00010101000000-000000000000

require github.com/hangtiancheng/yukino.go/libs/yukino_http v0.0.1

require (
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)
