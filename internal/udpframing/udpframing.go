package udpframing

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// UDP 报文在 yamux/TCP 字节流上以长度前缀分帧传输（避免粘包/解析错误）。
// 每条数据报：uint16 大端长度 + payload。
const (
	MaxDatagram = 65507  // UDP 最大 payload
	headerLen   = 2
)

var ErrTooLarge = errors.New("udp datagram too large")

// WriteFrame 写一帧：2B 大端长度 + payload。
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxDatagram {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(payload))
	}
	var hdr [headerLen]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// ReadFrame 读一帧；buf 容量足够时复用，否则分配新缓冲。
func ReadFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > MaxDatagram {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, n)
	}
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	payload := buf[:n]
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}