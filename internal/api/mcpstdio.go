package api

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// ServeStdio runs an MCP session over newline-delimited JSON-RPC: one message
// per line on r, one response per line on w. It returns when r ends or ctx
// is canceled. Logs never go to w; stdout belongs to the protocol.
func ServeStdio(ctx context.Context, session *MCPSession, r io.Reader, w io.Writer) error {
	reader := bufio.NewReaderSize(r, 64<<10)
	out := bufio.NewWriter(w)
	lines := make(chan string)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		for {
			line, err := readLine(reader, maxLineBytes)
			if line != "" {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errs <- err
				}
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			return err
		case line, ok := <-lines:
			if !ok {
				select {
				case err := <-errs:
					return err
				default:
					return nil
				}
			}
			reply := session.Handle(ctx, []byte(line))
			if reply == nil {
				continue
			}
			if _, err := out.Write(append(reply, '\n')); err != nil {
				return err
			}
			if err := out.Flush(); err != nil {
				return err
			}
		}
	}
}

// readLine reads one line, trimmed. A line longer than limit is consumed and
// replaced by an invalid message, so the session answers with a parse error
// instead of buffering without bound.
func readLine(r *bufio.Reader, limit int) (string, error) {
	var b strings.Builder
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if b.Len()+len(chunk) > limit {
				tooLong = true
				b.Reset()
			} else {
				b.Write(chunk)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		line := strings.TrimSpace(b.String())
		if tooLong {
			line = "{"
		}
		return line, err
	}
}
