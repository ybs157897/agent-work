package lspproxy

import (
	"bufio"
	"bytes"
	"errors"
	"testing"
)

func TestReadFrameRejectsOversizedBodyBeforeAllocation(t *testing.T) {
	input := []byte("Content-Length: 16777217\r\n\r\n")
	_, err := ReadFrame(bufio.NewReader(bytes.NewReader(input)))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}

func TestWriteFrameRejectsOversizedBody(t *testing.T) {
	_, err := func() ([]byte, error) {
		var out bytes.Buffer
		err := WriteFrame(&out, make([]byte, MaxFrameBytes+1))
		return out.Bytes(), err
	}()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}

func TestReadFrameAcceptsBodyAtLimit(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, MaxFrameBytes)
	var input bytes.Buffer
	if err := WriteFrame(&input, body); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(bufio.NewReader(bytes.NewReader(input.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(body) {
		t.Fatalf("body length=%d want %d", len(got), len(body))
	}
}

func TestReadFrameRejectsOversizedHeader(t *testing.T) {
	input := bytes.Repeat([]byte{'x'}, maxHeaderBytes+1)
	input = append(input, '\n')
	_, err := ReadFrame(bufio.NewReader(bytes.NewReader(input)))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}
