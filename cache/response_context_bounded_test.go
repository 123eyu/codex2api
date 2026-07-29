package cache

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisBoundedResponseContextReaderExactBoundaryAndOneByteOver(t *testing.T) {
	t.Run("exact boundary", func(t *testing.T) {
		record := redisResponseContextRecord{Items: []json.RawMessage{json.RawMessage(`{"type":"message"}`)}}
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		tc, commands := newBoundedRedisTestCache(t, payload)

		result, err := tc.GetResponseContextBounded(context.Background(), "owner|resp", int64(len(payload)))
		if err != nil {
			t.Fatalf("GetResponseContextBounded() error = %v", err)
		}
		if result.Status != ResponseContextReadFound || len(result.Items) != 1 {
			t.Fatalf("result = %+v, want found with one item", result)
		}
		assertBoundedRedisCommand(t, commands, int64(len(payload)))
	})

	t.Run("one byte over", func(t *testing.T) {
		payload := []byte(`{"items":[{"type":"message","content":"too large"}]}`)
		limit := int64(len(payload) - 1)
		tc, commands := newBoundedRedisTestCache(t, payload)

		result, err := tc.GetResponseContextBounded(context.Background(), "owner|resp", limit)
		if err != nil {
			t.Fatalf("GetResponseContextBounded() error = %v", err)
		}
		if result.Status != ResponseContextReadTooLarge || len(result.Items) != 0 {
			t.Fatalf("result = %+v, want too-large without decoded items", result)
		}
		assertBoundedRedisCommand(t, commands, limit)
	})
}

func TestRedisBoundedResponseContextReaderMissCorruptAndTransportError(t *testing.T) {
	t.Run("miss", func(t *testing.T) {
		tc, _ := newBoundedRedisTestCache(t, nil)
		result, err := tc.GetResponseContextBounded(context.Background(), "owner|missing", 1024)
		if err != nil || result.Status != ResponseContextReadMiss {
			t.Fatalf("result=%+v err=%v, want miss", result, err)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		tc, _ := newBoundedRedisTestCache(t, []byte(`{"items":[`))
		result, err := tc.GetResponseContextBounded(context.Background(), "owner|corrupt", 1024)
		if err != nil || result.Status != ResponseContextReadCorrupt {
			t.Fatalf("result=%+v err=%v, want corrupt", result, err)
		}
	})

	for _, payload := range []string{`{"items":[]}`, `{"items":null}`} {
		t.Run("empty value is miss "+payload, func(t *testing.T) {
			tc, _ := newBoundedRedisTestCache(t, []byte(payload))
			result, err := tc.GetResponseContextBounded(context.Background(), "owner|empty", 1024)
			if err != nil || result.Status != ResponseContextReadMiss {
				t.Fatalf("result=%+v err=%v, want miss", result, err)
			}
		})
	}

	t.Run("transport error", func(t *testing.T) {
		client := redis.NewClient(&redis.Options{
			Addr:        "127.0.0.1:1",
			DialTimeout: 10 * time.Millisecond,
			MaxRetries:  -1,
		})
		t.Cleanup(func() { _ = client.Close() })
		tc := &redisTokenCache{client: client}
		_, err := tc.GetResponseContextBounded(context.Background(), "owner|error", 1024)
		if err == nil {
			t.Fatal("GetResponseContextBounded() error = nil, want transport error")
		}
	})
}

func TestRedisResponseContextWireCompatibility(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"call_1"}`)}
	record := redisResponseContextRecord{Items: items}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), `{"items":[{"type":"function_call","call_id":"call_1"}]}`; got != want {
		t.Fatalf("wire payload = %s, want %s", got, want)
	}
	if got := responseContextKey("key:7|resp_1"); got != runtimeHashKey("codex:response:", "key:7|resp_1") {
		t.Fatalf("response context key changed: %q", got)
	}
}

func newBoundedRedisTestCache(t *testing.T, payload []byte) (*redisTokenCache, <-chan []string) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	commands := make(chan []string, 8)
	go func() {
		defer serverConn.Close()
		reader := bufio.NewReader(serverConn)
		for {
			command, err := readRESPCommand(reader)
			if err != nil {
				return
			}
			commands <- command
			if len(command) == 0 {
				return
			}
			switch strings.ToUpper(command[0]) {
			case "HELLO":
				_, _ = serverConn.Write([]byte("*0\r\n"))
			case "CLIENT":
				_, _ = serverConn.Write([]byte("+OK\r\n"))
			case "GETRANGE":
				if payload == nil {
					_, _ = serverConn.Write([]byte("$0\r\n\r\n"))
					continue
				}
				end, _ := strconv.Atoi(command[len(command)-1])
				value := payload
				if end+1 < len(value) {
					value = value[:end+1]
				}
				_, _ = fmt.Fprintf(serverConn, "$%d\r\n", len(value))
				_, _ = serverConn.Write(value)
				_, _ = serverConn.Write([]byte("\r\n"))
			default:
				_, _ = serverConn.Write([]byte("+OK\r\n"))
			}
		}
	}()

	var dialed bool
	client := redis.NewClient(&redis.Options{
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			if dialed {
				return nil, fmt.Errorf("unexpected second dial")
			}
			dialed = true
			return clientConn, nil
		},
		PoolSize:        1,
		Protocol:        2,
		DisableIdentity: true,
	})
	t.Cleanup(func() { _ = client.Close() })
	return &redisTokenCache{client: client}, commands
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	command := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if err != nil {
			return nil, err
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		command = append(command, string(value[:length]))
	}
	return command, nil
}

func assertBoundedRedisCommand(t *testing.T, commands <-chan []string, limit int64) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case command := <-commands:
			if len(command) == 0 || strings.ToUpper(command[0]) != "GETRANGE" {
				continue
			}
			if len(command) != 4 {
				t.Fatalf("GETRANGE command = %#v, want four args", command)
			}
			if command[2] != "0" || command[3] != strconv.FormatInt(limit, 10) {
				t.Fatalf("GETRANGE bounds = %q..%q, want 0..%d", command[2], command[3], limit)
			}
			return
		case <-deadline:
			t.Fatal("GETRANGE command was not observed")
		}
	}
}
