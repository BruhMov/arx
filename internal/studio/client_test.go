package studio_test

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drxinfra/rxmcp/internal/studio"
)

// fakeStudio отвечает на JSON-RPC по WebSocket: отдаёт результат по имени метода.
type fakeStudio struct {
	srv     *httptest.Server
	respond func(method string) map[string]any
}

func newFakeStudio(t *testing.T, respond func(string) map[string]any) *fakeStudio {
	t.Helper()
	f := &fakeStudio{respond: respond}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("нет Hijacker")
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		accept := acceptKey(r.Header.Get("Sec-WebSocket-Key"))
		_, _ = io.WriteString(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+accept+"\r\n\r\n")
		_ = brw.Flush()
		serveFrames(brw.Reader, conn, respond)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeStudio) wsURL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/rpc"
}

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// serveFrames читает кадры клиента (маскированные) и отвечает JSON-RPC.
func serveFrames(br *bufio.Reader, conn net.Conn, respond func(string) map[string]any) {
	for {
		op, payload, err := readFrame(br)
		if err != nil {
			return
		}
		switch op {
		case 0x8: // close
			return
		case 0x9: // ping -> pong
			_ = writeFrame(conn, 0xA, payload)
			continue
		}
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(payload, &req) != nil {
			return
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if respond != nil {
			if body := respond(req.Method); body != nil {
				for k, v := range body {
					resp[k] = v
				}
			}
		}
		if _, ok := resp["result"]; !ok {
			if _, ok := resp["error"]; !ok {
				resp["result"] = map[string]any{}
			}
		}
		out, _ := json.Marshal(resp)
		if writeFrame(conn, 0x1, out) != nil {
			return
		}
	}
}

func readFrame(br *bufio.Reader) (opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(br, h[:]); err != nil {
		return 0, nil, err
	}
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeFrame(conn net.Conn, opcode byte, payload []byte) error {
	hdr := []byte{0x80 | opcode}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 1<<16:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func TestCallResult(t *testing.T) {
	f := newFakeStudio(t, func(method string) map[string]any {
		if method == "DevelopmentExplorer/GetDevelopmentTree" {
			return map[string]any{"result": []map[string]any{{"id": "sol-1", "name": "Решение"}}}
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := studio.Dial(ctx, f.wsURL(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	raw, err := c.Call(ctx, "DevelopmentExplorer/GetDevelopmentTree", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("result не массив: %v", err)
	}
	if len(arr) != 1 || arr[0]["name"] != "Решение" {
		t.Fatalf("неожиданный результат: %v", arr)
	}
}

func TestCallError(t *testing.T) {
	f := newFakeStudio(t, func(method string) map[string]any {
		return map[string]any{"error": map[string]any{"code": -32000, "message": "имя занято"}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := studio.Dial(ctx, f.wsURL(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_, err = c.Call(ctx, "DevelopmentBuilders/CreateDevelopmentItem", map[string]any{"name": "x"})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !studio.IsRPCError(err) {
		t.Fatalf("ожидалась RPC-ошибка Studio, получена %v", err)
	}
}
