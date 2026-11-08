// Package protocol implements the DSUI tunnel wire format (version 1).
//
// Every binary WebSocket message is exactly one frame:
//
//	byte 0      frame type
//	bytes 1-4   stream id, big endian (the server allocates ids, starting at 1)
//	bytes 5-    payload
//
// Text WebSocket messages carry JSON control messages (see control.go).
// The full specification lives in docs/protocol.md.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Version is the tunnel protocol version implemented by this package.
const Version = 1

// Frame types.
const (
	// TypeOpen asks the agent to open a TCP connection. Payload: "host:port".
	TypeOpen byte = 1
	// TypeOpenOK is the agent's reply once the TCP connection is established.
	TypeOpenOK byte = 2
	// TypeOpenErr is the agent's reply when opening failed. Payload: JSON ErrorBody.
	TypeOpenErr byte = 3
	// TypeData carries stream bytes in either direction.
	TypeData byte = 4
	// TypeWindow grants the peer more send credit. Payload: uint32 big endian.
	TypeWindow byte = 5
	// TypeClose signals the sender will write no more data (half close).
	TypeClose byte = 6
	// TypeReset aborts the stream in both directions.
	TypeReset byte = 7
)

const (
	// HeaderSize is the number of bytes before the payload.
	HeaderSize = 5
	// InitialWindow is the send credit each side starts with, per stream.
	InitialWindow = 256 * 1024
	// MaxPayload bounds a single DATA frame payload.
	MaxPayload = 32 * 1024
)

// Frame is one decoded tunnel frame.
type Frame struct {
	Type    byte
	Stream  uint32
	Payload []byte
}

// ErrShortFrame is returned for messages smaller than the header.
var ErrShortFrame = errors.New("frame shorter than header")

// Encode serializes the frame.
func (f Frame) Encode() []byte {
	buf := make([]byte, HeaderSize+len(f.Payload))
	buf[0] = f.Type
	binary.BigEndian.PutUint32(buf[1:5], f.Stream)
	copy(buf[HeaderSize:], f.Payload)
	return buf
}

// Decode parses one binary message. The payload aliases data.
func Decode(data []byte) (Frame, error) {
	if len(data) < HeaderSize {
		return Frame{}, ErrShortFrame
	}
	f := Frame{
		Type:    data[0],
		Stream:  binary.BigEndian.Uint32(data[1:5]),
		Payload: data[HeaderSize:],
	}
	switch f.Type {
	case TypeOpen, TypeOpenOK, TypeOpenErr, TypeData, TypeWindow, TypeClose, TypeReset:
	default:
		return Frame{}, fmt.Errorf("unknown frame type %d", f.Type)
	}
	if f.Stream == 0 {
		return Frame{}, errors.New("stream id 0 is reserved")
	}
	if f.Type == TypeWindow && len(f.Payload) != 4 {
		return Frame{}, errors.New("window frame payload must be 4 bytes")
	}
	return f, nil
}

// WindowPayload encodes a credit grant.
func WindowPayload(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}

// ParseWindow decodes a credit grant payload.
func ParseWindow(payload []byte) uint32 {
	return binary.BigEndian.Uint32(payload)
}
