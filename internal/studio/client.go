package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Client JSON-RPC 2.0 поверх одного WebSocket-соединения со Studio.
// Вызовы сериализуются: Studio правит метаданные разработки, и параллельное
// редактирование ей вредит (см. нюансы публикации).
type Client struct {
	ws         *wsConn
	mu         sync.Mutex
	id         int64
	defTimeout time.Duration
}

// Dial открывает соединение со Studio.
func Dial(ctx context.Context, rawURL string, timeout time.Duration) (*Client, error) {
	ws, err := dialWS(ctx, rawURL, timeout)
	if err != nil {
		return nil, err
	}
	return &Client{ws: ws, defTimeout: timeout}, nil
}

// Close закрывает соединение.
func (c *Client) Close() error {
	if c.ws == nil {
		return nil
	}
	return c.ws.close()
}

// rpcError ошибка JSON-RPC, которую вернула сама Studio (не транспорт).
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Message != "" {
		if len(e.Data) > 0 {
			return fmt.Sprintf("Studio: %s (%s)", e.Message, strings.TrimSpace(string(e.Data)))
		}
		return "Studio: " + e.Message
	}
	return fmt.Sprintf("Studio: ошибка RPC %d", e.Code)
}

// IsRPCError сообщает, что это ошибка, присланная Studio в ответе (а не обрыв связи).
func IsRPCError(err error) bool {
	var re *rpcError
	return errors.As(err, &re)
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// CallParams отправляет метод с params (объект — именованные, массив — позиционные)
// и возвращает поле result ответа. params == nil означает метод без параметров.
func (c *Client) CallParams(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	deadline, has := ctx.Deadline()
	if !has {
		deadline = time.Now().Add(c.defTimeout)
	}
	defer func() {
		// Снять дедлайны, чтобы просроченный один вызов не ронял следующие.
		_ = c.ws.conn.SetWriteDeadline(time.Time{})
		_ = c.ws.conn.SetReadDeadline(time.Time{})
	}()
	if err := c.ws.conn.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	if err := c.ws.writeFrame(opText, payload); err != nil {
		return nil, fmt.Errorf("запрос к Studio не отправлен: %w", err)
	}
	if err := c.ws.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	for {
		data, err := c.ws.readMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("Studio закрыла соединение")
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, fmt.Errorf("Studio не ответила вовремя на %s", method)
			}
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		var resp rpcResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("ответ Studio не разобран: %w", err)
		}
		if resp.ID != c.id {
			// Ответ на другой вызов: при последовательном обращении быть не должно,
			// но пропускаем на всякий случай, а не путаем результаты.
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Call отправляет метод, параметры которого Studio ждёт в поле options.
func (c *Client) Call(ctx context.Context, method string, options map[string]any) (json.RawMessage, error) {
	return c.CallParams(ctx, method, map[string]any{"options": options})
}
