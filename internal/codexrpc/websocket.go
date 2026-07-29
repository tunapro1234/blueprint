package codexrpc

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	opText  = 0x1
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xa
)

type webSocketConn struct {
	conn      net.Conn
	reader    *bufio.Reader
	mu        sync.Mutex
	closeSent bool
	closed    bool
}

func dialWebSocketUnix(ctx context.Context, path string) (*webSocketConn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("connect to codex socket %s: %w", path, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	ws, err := upgradeWebSocket(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("upgrade codex socket %s: %w", path, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ws, nil
}

func upgradeWebSocket(conn net.Conn) (*webSocketConn, error) {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request, err := http.NewRequest(http.MethodGet, "http://localhost/", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", key)
	if err := request.Write(conn); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	if !headerHasToken(response.Header, "Connection", "upgrade") || !headerHasToken(response.Header, "Upgrade", "websocket") {
		return nil, fmt.Errorf("invalid WebSocket upgrade headers")
	}
	if response.Header.Get("Sec-WebSocket-Extensions") != "" {
		return nil, fmt.Errorf("WebSocket extensions are not supported")
	}
	want := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(want[:]) {
		return nil, fmt.Errorf("invalid Sec-WebSocket-Accept header")
	}
	return &webSocketConn{conn: conn, reader: reader}, nil
}

func headerHasToken(header http.Header, name, want string) bool {
	for _, value := range header.Values(name) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}

func (c *webSocketConn) ReadMessage() ([]byte, error) {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opText:
			if !utf8.Valid(payload) {
				return nil, fmt.Errorf("WebSocket text frame is not UTF-8")
			}
			return payload, nil
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
			continue
		case opClose:
			if len(payload) == 1 {
				return nil, fmt.Errorf("invalid WebSocket close frame")
			}
			_ = c.writeFrame(opClose, payload)
			return nil, io.EOF
		default:
			return nil, fmt.Errorf("unsupported WebSocket opcode %d", opcode)
		}
	}
}

func (c *webSocketConn) readFrame() (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return 0, nil, err
	}
	if header[0]&0x70 != 0 {
		return 0, nil, fmt.Errorf("WebSocket extensions are not supported")
	}
	if header[0]&0x80 == 0 {
		return 0, nil, fmt.Errorf("fragmented WebSocket frames are not supported")
	}
	opcode := header[0] & 0x0f
	if header[1]&0x80 != 0 {
		return 0, nil, fmt.Errorf("server WebSocket frames must not be masked")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
		if length < 126 {
			return 0, nil, fmt.Errorf("non-minimal WebSocket frame length")
		}
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
		if length < 65536 || length>>63 != 0 {
			return 0, nil, fmt.Errorf("invalid WebSocket frame length")
		}
	}
	if opcode >= opClose && length > 125 {
		return 0, nil, fmt.Errorf("WebSocket control frame is too large")
	}
	if length > maxMessageSize {
		return 0, nil, fmt.Errorf("WebSocket frame exceeds %d bytes", maxMessageSize)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

func (c *webSocketConn) WriteMessage(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("WebSocket text frame is not UTF-8")
	}
	return c.writeFrame(opText, data)
}

func (c *webSocketConn) writeFrame(opcode byte, payload []byte) error {
	if len(payload) > maxMessageSize {
		return fmt.Errorf("WebSocket frame exceeds %d bytes", maxMessageSize)
	}
	if opcode >= opClose && len(payload) > 125 {
		return fmt.Errorf("WebSocket control frame is too large")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	err := c.writeFrameLocked(opcode, payload)
	if err == nil && opcode == opClose {
		c.closeSent = true
	}
	return err
}

func (c *webSocketConn) writeFrameLocked(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch length := len(payload); {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 65535:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if err := writeAll(c.conn, header); err != nil {
		return err
	}
	return writeAll(c.conn, masked)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (c *webSocketConn) Close() error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	if !c.closeSent {
		_ = c.writeFrameLocked(opClose, nil)
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}
