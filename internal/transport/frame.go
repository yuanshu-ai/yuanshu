// Package transport defines the transport-neutral, full-duplex Protocol v1
// connection boundary shared by relay and standalone deployments.
package transport

// Frame is an immutable opaque wire frame. Transport implementations must not
// parse, normalize, or re-encode its contents.
type Frame struct {
	data []byte
}

// NewFrame copies raw so later caller mutations cannot change the frame.
func NewFrame(raw []byte) Frame {
	return Frame{data: append([]byte(nil), raw...)}
}

// Bytes returns a detached copy of the original wire bytes.
func (f Frame) Bytes() []byte {
	return append([]byte(nil), f.data...)
}

// Len returns the wire size in bytes.
func (f Frame) Len() int {
	return len(f.data)
}
