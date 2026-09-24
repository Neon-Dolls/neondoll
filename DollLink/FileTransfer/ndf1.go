package filetransfer

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ── NDF1 Frame ──────────────────────────────────────────────────────────

// NDF1Frame is a complete NDF1 v1 binary data frame consisting of a
// fixed 32-byte header followed by a payload of exactly the advertised
// length.
type NDF1Frame struct {
	Header  NDF1Header
	Payload []byte
}

// Validate checks that the frame is internally consistent:
//   - payload length matches the header length field
//   - header has valid magic (checked by header decode)
//   - payload does not exceed MaxNDF1Payload
//   - offset + length does not overflow (uint64 arithmetic)
func (f *NDF1Frame) Validate() error {
	if f.Header.TransferID.IsZero() {
		return fmt.Errorf("filetransfer: NDF1 frame: zero transfer_id")
	}
	if int(f.Header.Length) != len(f.Payload) {
		return fmt.Errorf("filetransfer: NDF1 frame: header length %d != payload length %d",
			f.Header.Length, len(f.Payload))
	}
	if f.Header.Length > MaxNDF1Payload {
		return fmt.Errorf("filetransfer: NDF1 frame: payload %d exceeds max %d",
			f.Header.Length, MaxNDF1Payload)
	}
	// Reject non-sequential offset (must be an absolute offset, but
	// sequential checking is a reader concern; here we just ensure
	// the offset + length doesn't overflow).
	if f.Header.Offset+uint64(f.Header.Length) < f.Header.Offset {
		return fmt.Errorf("filetransfer: NDF1 frame: offset+length overflow")
	}
	return nil
}

// Encode writes the complete NDF1 frame (header + payload) to w.
// It returns the number of bytes written.
func (f *NDF1Frame) Encode(w io.Writer) (int, error) {
	if err := f.Validate(); err != nil {
		return 0, err
	}
	header := f.Header.Encode()
	n, err := w.Write(header)
	if err != nil {
		return n, fmt.Errorf("filetransfer: encode header: %w", err)
	}
	n2, err := w.Write(f.Payload)
	n += n2
	if err != nil {
		return n, fmt.Errorf("filetransfer: encode payload: %w", err)
	}
	return n, nil
}

// DecodeNDF1Frame reads and parses one complete NDF1 frame from r.
// It does not enforce sequential offsets — that is the caller's responsibility.
// The returned frame owns the payload slice, which may reference the
// underlying read buffer.
func DecodeNDF1Frame(r io.Reader) (*NDF1Frame, error) {
	headerBuf := make([]byte, NDF1HeaderLen)
	if _, err := io.ReadFull(r, headerBuf); err != nil {
		return nil, fmt.Errorf("filetransfer: read NDF1 header: %w", err)
	}
	header, err := DecodeNDF1Header(headerBuf)
	if err != nil {
		return nil, err
	}
	if header.Length > MaxNDF1Payload {
		return nil, fmt.Errorf("filetransfer: NDF1 frame: payload %d exceeds max %d",
			header.Length, MaxNDF1Payload)
	}
	payload := make([]byte, header.Length)
	if header.Length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("filetransfer: read NDF1 payload: %w", err)
		}
	}
	return &NDF1Frame{
		Header:  *header,
		Payload: payload,
	}, nil
}

// ── Sequential Offset Checker ───────────────────────────────────────────

// SequentialChecker enforces that frames arrive at consecutive expected
// offsets for a single transfer attempt.
type SequentialChecker struct {
	nextOffset uint64
}

// NewSequentialChecker creates a checker starting at offset 0.
func NewSequentialChecker() *SequentialChecker {
	return &SequentialChecker{}
}

// NewSequentialCheckerAt creates a checker starting at the given offset
// (useful for resume).
func NewSequentialCheckerAt(offset uint64) *SequentialChecker {
	return &SequentialChecker{nextOffset: offset}
}

// Check verifies that `frame` starts at exactly the expected offset and
// advances the expected offset past the frame.
func (sc *SequentialChecker) Check(frame *NDF1Frame) error {
	if frame.Header.Offset != sc.nextOffset {
		return fmt.Errorf("filetransfer: expected offset %d, got %d",
			sc.nextOffset, frame.Header.Offset)
	}
	sc.nextOffset += uint64(frame.Header.Length)
	return nil
}

// NextOffset returns the next expected start offset.
func (sc *SequentialChecker) NextOffset() uint64 {
	return sc.nextOffset
}

// ── Wire format helpers ─────────────────────────────────────────────────

// MarshalBinary encodes a full NDF1 frame as a single []byte.
func (f *NDF1Frame) MarshalBinary() ([]byte, error) {
	header := f.Header.Encode()
	out := make([]byte, NDF1HeaderLen+len(f.Payload))
	copy(out[0:NDF1HeaderLen], header)
	copy(out[NDF1HeaderLen:], f.Payload)
	return out, nil
}

// UnmarshalBinary decodes a single []byte into an NDF1Frame.
func (f *NDF1Frame) UnmarshalBinary(data []byte) error {
	if len(data) < NDF1HeaderLen {
		return fmt.Errorf("filetransfer: NDF1 frame too short: %d < %d", len(data), NDF1HeaderLen)
	}
	header, err := DecodeNDF1Header(data[:NDF1HeaderLen])
	if err != nil {
		return err
	}
	if uint32(len(data)-NDF1HeaderLen) != header.Length {
		return fmt.Errorf("filetransfer: NDF1 frame: payload length mismatch: header says %d, data has %d",
			header.Length, len(data)-NDF1HeaderLen)
	}
	payload := make([]byte, header.Length)
	copy(payload, data[NDF1HeaderLen:])
	f.Header = *header
	f.Payload = payload
	return nil
}

// Size returns the total wire size of the frame in bytes.
func (f *NDF1Frame) Size() int {
	return NDF1HeaderLen + len(f.Payload)
}

// OffsetEnd returns the byte just past the end of this frame's payload.
func (f *NDF1Frame) OffsetEnd() uint64 {
	return f.Header.Offset + uint64(f.Header.Length)
}

// ── Build a frame ───────────────────────────────────────────────────────

// NewNDF1Frame creates a frame with the given transfer_id at the given
// absolute offset from the given payload slice. It clamps large payloads:
// if payload is longer than MaxNDF1Payload, it truncates.
func NewNDF1Frame(tid TransferID, offset uint64, payload []byte) *NDF1Frame {
	if len(payload) > MaxNDF1Payload {
		payload = payload[:MaxNDF1Payload]
	}
	return &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     offset,
			Length:     uint32(len(payload)),
		},
		Payload: payload,
	}
}

// Ensure binary 32-byte header layout is correct.
var _ = NDF1HeaderLen // use in type-check
func init() {
	h := NDF1Header{
		TransferID: [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		Offset:     0x0102030405060708,
		Length:     0x090A0B0C,
	}
	encoded := h.Encode()
	if len(encoded) != 32 {
		panic("NDF1Header.Encode produced wrong length")
	}
	if string(encoded[0:4]) != "NDF1" {
		panic("NDF1 magic wrong")
	}
	for i, b := range encoded[4:20] {
		if b != byte(i) {
			panic(fmt.Sprintf("NDF1 transfer_id byte %d wrong", i))
		}
	}
	if binary.BigEndian.Uint64(encoded[20:28]) != 0x0102030405060708 {
		panic("NDF1 offset wrong")
	}
	if binary.BigEndian.Uint32(encoded[28:32]) != 0x090A0B0C {
		panic("NDF1 length wrong")
	}
}
