package proxy

import (
	"bufio"
	"errors"
)

// 上游 SSE 单行上限。正常事件远小于此值；超过说明上游发送了异常数据，
// 继续按 ReadBytes 累积会直接放大到整个行长的内存占用。
const maxSSELineBytes = 8 << 20

var errSSELineTooLong = errors.New("upstream SSE line exceeds limit")

// readBoundedSSELine 读取一行，内存占用不超过 limit（不含 bufio 自身缓冲）。
func readBoundedSSELine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errSSELineTooLong
		}
		line = append(line, fragment...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}
