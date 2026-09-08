package lspproxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const MaxFrameBytes = 16 << 20 // 16 MiB per LSP message body

const maxHeaderBytes = 8 << 10

var ErrFrameTooLarge = errors.New("lsp frame too large")

// ReadFrame reads one LSP stdio frame (Content-Length header + body).
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := readHeaderLine(r)
		if err != nil {
			return nil, err
		}
		if line == "" {
			break
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "content-length:") {
			n, err := strconv.ParseInt(strings.TrimSpace(line[len("Content-Length:"):]), 10, 64)
			if err != nil || n < 0 || n > int64(maxInt()) {
				return nil, fmt.Errorf("bad Content-Length")
			}
			if n > int64(MaxFrameBytes) {
				return nil, ErrFrameTooLarge
			}
			contentLength = int(n)
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readHeaderLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxHeaderBytes {
			return "", ErrFrameTooLarge
		}
		line = append(line, part...)
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
		if err == nil && len(part) > 0 && part[len(part)-1] == '\n' {
			return strings.TrimRight(string(line), "\r\n"), nil
		}
	}
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

// WriteFrame writes one LSP stdio frame.
func WriteFrame(w io.Writer, body []byte) error {
	if len(body) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}
