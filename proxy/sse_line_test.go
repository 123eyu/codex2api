package proxy

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadBoundedSSELine(t *testing.T) {
	long := strings.Repeat("a", 10000) + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(long), 16)
	line, err := readBoundedSSELine(reader, maxSSELineBytes)
	if err != nil || len(line) != len(long) {
		t.Fatalf("len=%d err=%v, want %d bytes", len(line), err, len(long))
	}

	reader = bufio.NewReaderSize(strings.NewReader("123456789\n"), 4)
	if _, err := readBoundedSSELine(reader, 8); err != errSSELineTooLong {
		t.Fatalf("err = %v, want errSSELineTooLong", err)
	}

	reader = bufio.NewReaderSize(strings.NewReader("12345678\n"), 4)
	if line, err := readBoundedSSELine(reader, 9); err != nil || string(line) != "12345678\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
}
