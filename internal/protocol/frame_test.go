package protocol

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Frame{Type: TypeData, Stream: 0xA1B2C3D4, Payload: []byte("hello")}
	out, err := Decode(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.Stream != in.Stream || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestDecodeRejectsInvalid(t *testing.T) {
	cases := map[string][]byte{
		"short":        {1, 0, 0},
		"unknown type": {99, 0, 0, 0, 1},
		"stream zero":  {TypeData, 0, 0, 0, 0},
		"bad window":   {TypeWindow, 0, 0, 0, 1, 1, 2},
	}
	for name, data := range cases {
		if _, err := Decode(data); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestWindowPayload(t *testing.T) {
	if got := ParseWindow(WindowPayload(123456)); got != 123456 {
		t.Fatalf("got %d", got)
	}
}

func TestEmptyPayloadAllowed(t *testing.T) {
	f, err := Decode(Frame{Type: TypeClose, Stream: 7}.Encode())
	if err != nil || len(f.Payload) != 0 {
		t.Fatalf("close frame: %+v %v", f, err)
	}
}
