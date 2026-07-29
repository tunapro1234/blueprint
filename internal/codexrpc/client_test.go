package codexrpc

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStdioMatchesResponsesByIDAndForwardsNotifications(t *testing.T) {
	serverOutput, clientOutput := io.Pipe()
	serverInput, clientInput := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer serverOutput.Close()
		scanner := bufio.NewScanner(serverInput)
		write := func(value any) error {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(clientOutput, string(data))
			return err
		}
		read := func() (testRequest, error) {
			if !scanner.Scan() {
				return testRequest{}, io.EOF
			}
			var request testRequest
			return request, json.Unmarshal(scanner.Bytes(), &request)
		}

		initialize, err := read()
		if err != nil || initialize.Method != "initialize" {
			serverDone <- fmt.Errorf("initialize request: %+v, %v", initialize, err)
			return
		}
		// The real app-server omits the jsonrpc field, so the fake does too.
		if err := write(map[string]any{"id": initialize.ID, "result": map[string]any{}}); err != nil {
			serverDone <- err
			return
		}
		first, err := read()
		if err != nil {
			serverDone <- err
			return
		}
		second, err := read()
		if err != nil {
			serverDone <- err
			return
		}
		// A server-to-client request with a string id must be ignored, not fatal.
		if err := write(map[string]any{"id": "srv-1", "method": "item/reviewApproval", "params": map[string]any{}}); err != nil {
			return
		}
		if err := write(map[string]any{"jsonrpc": "2.0", "method": "thread/status/changed", "params": map[string]any{"threadId": "thread-1"}}); err != nil {
			serverDone <- err
			return
		}
		for _, request := range []testRequest{second, first} {
			var params struct {
				Tag string `json:"tag"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				serverDone <- err
				return
			}
			if err := write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]string{"tag": params.Tag}}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := ConnectStdio(ctx, serverOutput, clientInput)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	type result struct {
		Tag string `json:"tag"`
	}
	results := make(chan result, 2)
	errs := make(chan error, 2)
	for _, tag := range []string{"alpha", "beta"} {
		tag := tag
		go func() {
			var got result
			err := client.call(ctx, "thread/list", map[string]string{"tag": tag}, &got)
			results <- got
			errs <- err
		}()
	}
	got := []string{(<-results).Tag, (<-results).Tag}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(stringSet(got), stringSet([]string{"alpha", "beta"})) {
		t.Fatalf("responses=%v", got)
	}
	select {
	case notification := <-client.Notifications():
		if notification.Method != "thread/status/changed" || !strings.Contains(string(notification.Params), "thread-1") {
			t.Fatalf("notification=%+v", notification)
		}
	case <-ctx.Done():
		t.Fatal("notification was not forwarded")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestThreadListFollowsPagination(t *testing.T) {
	serverOutput, clientOutput := io.Pipe()
	serverInput, clientInput := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer serverOutput.Close()
		scanner := bufio.NewScanner(serverInput)
		for page := 0; page < 3; page++ {
			if !scanner.Scan() {
				serverDone <- scanner.Err()
				return
			}
			var request testRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				serverDone <- err
				return
			}
			result := any(map[string]any{})
			if page == 1 {
				result = map[string]any{"data": []map[string]any{{"id": "one", "name": "First", "cwd": "/one", "status": map[string]string{"type": "idle"}}}, "nextCursor": "next"}
			}
			if page == 2 {
				var params map[string]any
				_ = json.Unmarshal(request.Params, &params)
				if params["cursor"] != "next" {
					serverDone <- fmt.Errorf("cursor=%v", params["cursor"])
					return
				}
				update, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "thread/tokenUsage/updated",
					"params": map[string]any{
						"threadId": "one",
						"tokenUsage": map[string]any{
							"last":               map[string]any{"totalTokens": 12_000},
							"total":              map[string]any{"totalTokens": 30_000},
							"modelContextWindow": 200_000,
						},
					},
				})
				if _, err := fmt.Fprintln(clientOutput, string(update)); err != nil {
					serverDone <- err
					return
				}
				result = map[string]any{"data": []map[string]any{{"id": "two", "name": "Second", "cwd": "/two", "status": map[string]string{"type": "active"}}}, "nextCursor": nil}
			}
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			if _, err := fmt.Fprintln(clientOutput, string(data)); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := ConnectStdio(ctx, serverOutput, clientInput)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	threads, err := client.ThreadList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{threads[0].ID, threads[1].ID}; !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("thread ids=%v", got)
	}
	if threads[0].TokenUsage == nil || threads[0].TokenUsage.Last.TotalTokens != 12_000 || threads[0].TokenUsage.ModelContextWindow == nil || *threads[0].TokenUsage.ModelContextWindow != 200_000 {
		t.Fatalf("notification token usage=%+v", threads[0].TokenUsage)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestDialUnixWebSocketHandshakeMaskingAndPing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-server.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			serverDone <- err
			return
		}
		if request.URL.Path != "/" || !headerHasToken(request.Header, "Connection", "upgrade") || request.Header.Get("Sec-WebSocket-Version") != "13" {
			serverDone <- fmt.Errorf("bad upgrade request: %+v", request)
			return
		}
		accept := sha1.Sum([]byte(request.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		response := "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(accept[:]) + "\r\n\r\n"
		if _, err := io.WriteString(conn, response); err != nil {
			serverDone <- err
			return
		}

		initialize, opcode, err := readClientFrame(reader)
		if err != nil || opcode != opText || !strings.Contains(string(initialize), `"method":"initialize"`) {
			serverDone <- fmt.Errorf("initialize frame: opcode=%d data=%q err=%v", opcode, initialize, err)
			return
		}
		if err := writeServerFrame(conn, opPing, []byte("hello")); err != nil {
			serverDone <- err
			return
		}
		pong, opcode, err := readClientFrame(reader)
		if err != nil || opcode != opPong || string(pong) != "hello" {
			serverDone <- fmt.Errorf("pong frame: opcode=%d data=%q err=%v", opcode, pong, err)
			return
		}
		var initRequest testRequest
		_ = json.Unmarshal(initialize, &initRequest)
		if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "id": initRequest.ID, "result": map[string]any{}}); err != nil {
			serverDone <- err
			return
		}

		list, opcode, err := readClientFrame(reader)
		if err != nil || opcode != opText {
			serverDone <- fmt.Errorf("list frame: opcode=%d err=%v", opcode, err)
			return
		}
		var listRequest testRequest
		_ = json.Unmarshal(list, &listRequest)
		result := map[string]any{"data": []map[string]any{{"id": "ws-thread", "name": "Socket agent", "cwd": "/srv/socket", "status": map[string]string{"type": "active"}}}, "nextCursor": nil}
		if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "id": listRequest.ID, "result": result}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := DialUnix(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	threads, err := client.ThreadList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != "ws-thread" || threads[0].Status.Type != "active" {
		t.Fatalf("threads=%+v", threads)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func TestWebSocketExtendedLengthAndFragmentRejection(t *testing.T) {
	t.Run("masked extended length", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer serverConn.Close()
		ws := &webSocketConn{conn: clientConn, reader: bufio.NewReader(clientConn)}
		payload := []byte(strings.Repeat("x", 300))
		done := make(chan error, 1)
		go func() { done <- ws.WriteMessage(payload) }()
		got, opcode, err := readClientFrame(bufio.NewReader(serverConn))
		if err != nil {
			t.Fatal(err)
		}
		if opcode != opText || !reflect.DeepEqual(got, payload) {
			t.Fatalf("opcode=%d len=%d", opcode, len(got))
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		_ = ws.Close()
	})

	t.Run("fragmented server frame", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		ws := &webSocketConn{conn: clientConn, reader: bufio.NewReader(clientConn)}
		go func() {
			_, _ = serverConn.Write([]byte{opText, 2, 'o', 'k'})
			_ = serverConn.Close()
		}()
		_, err := ws.ReadMessage()
		if err == nil || !strings.Contains(err.Error(), "fragmented") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("close", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		ws := &webSocketConn{conn: clientConn, reader: bufio.NewReader(clientConn)}
		serverDone := make(chan error, 1)
		go func() {
			if err := writeServerFrame(serverConn, opClose, nil); err != nil {
				serverDone <- err
				return
			}
			_, opcode, err := readClientFrame(bufio.NewReader(serverConn))
			if err == nil && opcode != opClose {
				err = fmt.Errorf("opcode=%d", opcode)
			}
			serverDone <- err
		}()
		_, err := ws.ReadMessage()
		if !errors.Is(err, io.EOF) {
			t.Fatalf("error=%v", err)
		}
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
		_ = serverConn.Close()
		_ = ws.Close()
	})
}

type testRequest struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func readClientFrame(reader *bufio.Reader) ([]byte, byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, 0, err
	}
	if header[0]&0x80 == 0 || header[1]&0x80 == 0 {
		return nil, 0, fmt.Errorf("client frame is not final and masked")
	}
	length := uint64(header[1] & 0x7f)
	if length == 126 {
		var encoded [2]byte
		if _, err := io.ReadFull(reader, encoded[:]); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(encoded[:]))
	} else if length == 127 {
		var encoded [8]byte
		if _, err := io.ReadFull(reader, encoded[:]); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(encoded[:])
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return nil, 0, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, 0, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return payload, header[0] & 0xf, nil
}

func writeServerJSON(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeServerFrame(writer, opText, data)
}

func writeServerFrame(writer io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	if len(payload) < 126 {
		header = append(header, byte(len(payload)))
	} else {
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	}
	if err := writeAll(writer, header); err != nil {
		return err
	}
	return writeAll(writer, payload)
}
