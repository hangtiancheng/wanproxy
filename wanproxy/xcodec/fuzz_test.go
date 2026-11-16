package xcodec

import "testing"

func FuzzSessionReceive(f *testing.F) {
	f.Add(AppendPipeFrame(AppendHello(nil, CacheID{1}), []byte("data"), []byte("data")), uint8(1))
	f.Add(AppendAdvance(nil, ^uint32(0)), uint8(7))
	f.Add(AppendEOS(AppendHello(nil, CacheID{1})), uint8(64))
	f.Fuzz(func(t *testing.T, wire []byte, size uint8) {
		if len(wire) > 65536 {
			t.Skip()
		}
		s := NewSession(NewMemoryCache(WithSegmentLimit(4)))
		for len(wire) > 0 {
			n := min(len(wire), int(size)+1)
			res, err := s.Receive(wire[:n])
			if len(res.Output) > maxReceiveBytes || len(res.Responses) > maxReceiveBytes {
				t.Fatal("unbounded receive result")
			}
			if err != nil {
				return
			}
			wire = wire[n:]
		}
	})
}

func FuzzReadPipeFrame(f *testing.F) {
	f.Add(AppendPipeFrame(nil, []byte("hello"), []byte("hello")))
	f.Add([]byte{PipeOpFrame, 255, 255, 255, 255})
	f.Add(AppendAsk(nil, []uint64{0, 1}))
	f.Fuzz(func(t *testing.T, wire []byte) {
		_, n, status, err := ReadPipeFrame(wire)
		if status == PipeFrameReadOK && (err != nil || n <= 0 || n > len(wire)) {
			t.Fatal("invalid successful parse")
		}
		if status == PipeFrameReadNeedMore && (err != nil || n != 0) {
			t.Fatal("incomplete parse consumed bytes")
		}
		if status == PipeFrameReadInvalid && err == nil {
			t.Fatal("invalid parse omitted error")
		}
	})
}
