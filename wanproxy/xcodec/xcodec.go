package xcodec

const Magic byte = 0xf1

const (
	OpEscape byte = 0x00

	OpExtract byte = 0x01

	OpRef byte = 0x02

	OpBackref byte = 0x03
)

const SegmentLength = 2048

const (
	WindowMax   = 0xff
	WindowCount = WindowMax + 1
)

type Codec struct {
	cache Cache
}

func NewCodec(cache Cache) *Codec {
	return &Codec{cache: cache}
}

func (c *Codec) Cache() Cache {
	return c.cache
}
