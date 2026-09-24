package filetransfer

import (
	"bytes"
	"testing"
)

func TestNDF1FrameEncodeDecode(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	payload := []byte("Hello, NDF1 World!")
	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     0,
			Length:     uint32(len(payload)),
		},
		Payload: payload,
	}

	// Encode to bytes
	data, err := frame.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Check header size
	if len(data) != NDF1HeaderLen+len(payload) {
		t.Fatalf("encoded size: got %d, want %d", len(data), NDF1HeaderLen+len(payload))
	}

	// Decode from bytes
	var decoded NDF1Frame
	if err := decoded.UnmarshalBinary(data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Header.TransferID != tid {
		t.Fatalf("transfer_id mismatch")
	}
	if decoded.Header.Offset != 0 {
		t.Fatalf("offset: got %d, want 0", decoded.Header.Offset)
	}
	if int(decoded.Header.Length) != len(payload) {
		t.Fatalf("length: got %d, want %d", decoded.Header.Length, len(payload))
	}
	if string(decoded.Payload) != string(payload) {
		t.Fatalf("payload: got %q, want %q", string(decoded.Payload), string(payload))
	}
}

func TestNDF1FrameStreamEncodeDecode(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	frame := NewNDF1Frame(tid, 0, payload)
	var buf bytes.Buffer
	n, err := frame.Encode(&buf)
	if err != nil {
		t.Fatalf("encode stream: %v", err)
	}
	if n != NDF1HeaderLen+len(payload) {
		t.Fatalf("encoded bytes: got %d, want %d", n, NDF1HeaderLen+len(payload))
	}

	decoded, err := DecodeNDF1Frame(&buf)
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if decoded.Header.Offset != 0 {
		t.Fatalf("offset: got %d, want 0", decoded.Header.Offset)
	}
	if int(decoded.Header.Length) != len(payload) {
		t.Fatalf("length: got %d, want %d", decoded.Header.Length, len(payload))
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestNDF1FrameValidate(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	t.Run("valid frame", func(t *testing.T) {
		frame := NewNDF1Frame(tid, 0, []byte("data"))
		if err := frame.Validate(); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})

	t.Run("empty payload", func(t *testing.T) {
		frame := NewNDF1Frame(tid, 100, nil)
		if err := frame.Validate(); err != nil {
			t.Fatalf("expected valid empty payload, got: %v", err)
		}
	})

	t.Run("length mismatch", func(t *testing.T) {
		frame := NewNDF1Frame(tid, 0, []byte("data"))
		frame.Header.Length = 999 // wrong
		if err := frame.Validate(); err == nil {
			t.Fatal("expected error for length mismatch")
		}
	})

	t.Run("payload exceeds max", func(t *testing.T) {
		bigPayload := make([]byte, MaxNDF1Payload+1)
		frame := NewNDF1Frame(tid, 0, bigPayload[:MaxNDF1Payload])
		// NewNDF1Frame truncates, so set it manually to exceed
		frame.Header.Length = MaxNDF1Payload + 1
		frame.Payload = bigPayload[:MaxNDF1Payload+1]
		if err := frame.Validate(); err == nil {
			t.Fatal("expected error for oversized payload")
		}
	})

	t.Run("zero transfer id", func(t *testing.T) {
		var zeroTid TransferID
		frame := NewNDF1Frame(zeroTid, 0, []byte("bad"))
		if err := frame.Validate(); err == nil {
			t.Fatal("expected error for zero transfer_id")
		}
	})
}

func TestNDF1FrameMaxPayload(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	payload := make([]byte, MaxNDF1Payload)
	for i := range payload {
		payload[i] = byte(i & 0xff)
	}

	frame := NewNDF1Frame(tid, 0, payload)
	if len(frame.Payload) != MaxNDF1Payload {
		t.Fatalf("payload length: got %d, want %d", len(frame.Payload), MaxNDF1Payload)
	}
	if err := frame.Validate(); err != nil {
		t.Fatalf("expected valid max payload, got: %v", err)
	}

	data, err := frame.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded NDF1Frame
	if err := decoded.UnmarshalBinary(data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Fatal("payload mismatch after round-trip")
	}
}

func TestNDF1FrameOversizedPayload(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	big := make([]byte, MaxNDF1Payload+1)
	frame := NewNDF1Frame(tid, 0, big)
	if len(frame.Payload) != MaxNDF1Payload {
		t.Fatalf("NewNDF1Frame should truncate to max, got %d", len(frame.Payload))
	}
}

func TestNDF1FrameDecodeTruncated(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte("NDF1"))
	_, err := DecodeNDF1Frame(&buf)
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestNDF1FrameDecodeBadBuffered(t *testing.T) {
	// Test UnmarshalBinary with a short buffer
	var f NDF1Frame
	err := f.UnmarshalBinary([]byte("short"))
	if err == nil {
		t.Fatal("expected error for short data")
	}
}

func TestSequentialChecker(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	sc := NewSequentialChecker()

	t.Run("first frame at 0", func(t *testing.T) {
		f1 := NewNDF1Frame(tid, 0, []byte("hello"))
		if err := sc.Check(f1); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
		if sc.NextOffset() != 5 {
			t.Fatalf("next offset: got %d, want 5", sc.NextOffset())
		}
	})

	t.Run("second frame at expected offset", func(t *testing.T) {
		f2 := NewNDF1Frame(tid, 5, []byte("world"))
		if err := sc.Check(f2); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
		if sc.NextOffset() != 10 {
			t.Fatalf("next offset: got %d, want 10", sc.NextOffset())
		}
	})

	t.Run("wrong offset rejected", func(t *testing.T) {
		f3 := NewNDF1Frame(tid, 0, []byte("rewind"))
		if err := sc.Check(f3); err == nil {
			t.Fatal("expected error for wrong offset")
		}
	})
}

func TestSequentialCheckerResume(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	t.Run("resume at non-zero", func(t *testing.T) {
		sc := NewSequentialCheckerAt(262144)
		f := NewNDF1Frame(tid, 262144, make([]byte, 1024))
		if err := sc.Check(f); err != nil {
			t.Fatalf("expected valid resume, got: %v", err)
		}
		if sc.NextOffset() != 263168 {
			t.Fatalf("next offset: got %d, want 263168", sc.NextOffset())
		}
	})

	t.Run("resume at zero same as fresh", func(t *testing.T) {
		sc := NewSequentialCheckerAt(0)
		f := NewNDF1Frame(tid, 0, []byte("data"))
		if err := sc.Check(f); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})
}

func TestNDF1FrameOffsetEnd(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	f := NewNDF1Frame(tid, 1000, make([]byte, 256))
	if f.OffsetEnd() != 1256 {
		t.Fatalf("OffsetEnd: got %d, want 1256", f.OffsetEnd())
	}

	empty := NewNDF1Frame(tid, 500, nil)
	if empty.OffsetEnd() != 500 {
		t.Fatalf("OffsetEnd (empty): got %d, want 500", empty.OffsetEnd())
	}
}

func TestNDF1FrameSize(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	f := NewNDF1Frame(tid, 0, make([]byte, 256))
	if f.Size() != NDF1HeaderLen+256 {
		t.Fatalf("Size: got %d, want %d", f.Size(), NDF1HeaderLen+256)
	}
}

func TestNDF1FrameNewConstrutorTruncation(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	big := make([]byte, MaxNDF1Payload+500)
	frame := NewNDF1Frame(tid, 0, big)
	if len(frame.Payload) != MaxNDF1Payload {
		t.Fatalf("NewNDF1Frame should truncate to MaxNDF1Payload, got %d", len(frame.Payload))
	}
	if frame.Header.Length != MaxNDF1Payload {
		t.Fatalf("header length: got %d, want %d", frame.Header.Length, MaxNDF1Payload)
	}
}

func TestNDF1FrameRecommendedChunkSize(t *testing.T) {
	if RecommendedChunkSize >= MaxNDF1Payload {
		t.Fatalf("RecommendedChunkSize %d must be <= MaxNDF1Payload %d", RecommendedChunkSize, MaxNDF1Payload)
	}
	if RecommendedChunkSize <= 0 {
		t.Fatalf("RecommendedChunkSize must be positive")
	}
}
