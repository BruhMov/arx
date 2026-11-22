// Package studio: клиент Directum Development Studio по JSON-RPC 2.0 поверх WebSocket.
// Studio — локальное приложение разработчика (ws://127.0.0.1:8362/rpc), поэтому
// WebSocket-клиент реализован вручную на stdlib (RFC 6455): внешних зависимостей не тянем.
package studio

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// opcode кадров WebSocket (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsGUID константа хэндшейка WebSocket (RFC 6455 §1.3).
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxFrame защита от неадекватно больших кадров (64 МБ).
const maxFrame = 64 << 20

// wsConn одно WebSocket-соединение: чтение и запись кадров по RFC 6455.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex // запись под мьютексом: кадры не должны перемежаться
}

// dialWS открывает WebSocket-соединение с указанным адресом.
func dialWS(ctx context.Context, rawURL string, timeout time.Duration) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("адрес Studio %q не разобран: %w", rawURL, err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("адрес Studio должен начинаться с ws:// или wss://, получен %q", rawURL)
	}
	addr := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("Studio недоступна по %s: %w", rawURL, err)
	}
	if u.Scheme == "wss" {
		tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS-рукопожатие со Studio: %w", err)
		}
		conn = tc
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if err := writeAll(conn, []byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("запрос к Studio не отправлен: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ответ Studio не разобран: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("Studio не переключилась на WebSocket: %s", resp.Status)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != acceptKey(key) {
		conn.Close()
		return nil, fmt.Errorf("Studio прислала неверный Sec-WebSocket-Accept: %q", got)
	}
	return &wsConn{conn: conn, br: br}, nil
}

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (w *wsConn) close() error { return w.conn.Close() }

func writeAll(c net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := c.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// writeFrame пишет один кадр. Клиент обязан маскировать payload (RFC 6455 §5.3).
func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	hdr := make([]byte, 0, 14)
	hdr = append(hdr, 0x80|opcode) // FIN + opcode
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n)) // MASK + длина
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, b[:]...)
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	return writeAll(w.conn, append(hdr, masked...))
}

// readFrame читает один кадр: fin, opcode, payload (снята маска).
func (w *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(w.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(w.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	if n > maxFrame {
		return false, 0, nil, errors.New("кадр Studio слишком велик")
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(w.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// readMessage собирает сообщение из кадров, отвечая на ping и close.
// Суммарный размер сообщения ограничен тем же maxFrame, что и размер кадра.
func (w *wsConn) readMessage() ([]byte, error) {
	var msg []byte
	for {
		fin, op, data, err := w.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opText, opBinary:
			if len(msg)+len(data) > maxFrame {
				return nil, errors.New("сообщение Studio слишком велико")
			}
			msg = append(msg, data...)
			if fin {
				return msg, nil
			}
		case opContinuation:
			if len(msg)+len(data) > maxFrame {
				return nil, errors.New("сообщение Studio слишком велико")
			}
			msg = append(msg, data...)
			if fin {
				return msg, nil
			}
		case opPing:
			if err := w.writeFrame(opPong, data); err != nil {
				return nil, err
			}
		case opPong:
			// собственные pong игнорируем
		case opClose:
			_ = w.writeFrame(opClose, data)
			return nil, io.EOF
		default:
			return nil, fmt.Errorf("Studio прислала неожиданный opcode %#x", op)
		}
	}
}
