package studio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// httpBase корень HTTP Studio, выведенный из WebSocket-адреса: ws:// → http://, wss:// → https://.
func (s *Service) httpBase() (string, error) {
	u, err := url.Parse(s.url)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("адрес Studio %q не WebSocket", s.url)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// ExportPackage получает пакет разработки (GET /exportPackage). Возвращает тело и Content-Type.
func (s *Service) ExportPackage(ctx context.Context) ([]byte, string, error) {
	base, err := s.httpBase()
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/exportPackage", nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.httpDo(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// SaveNoCode сохраняет NoCode-настройку (PUT /saveNoCode/{kind}/{id}).
// kind: FormView, ListView, ModuleView или ExplorerModule.
func (s *Service) SaveNoCode(ctx context.Context, kind, id string, body []byte) error {
	switch kind {
	case "FormView", "ListView", "ModuleView", "ExplorerModule":
	default:
		return fmt.Errorf("kind %q: допустимо FormView, ListView, ModuleView или ExplorerModule", kind)
	}
	base, err := s.httpBase()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/saveNoCode/"+url.PathEscape(kind)+"/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpDo(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (s *Service) httpDo(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: s.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Studio (HTTP) недоступна: %w", err)
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("Studio ответила %s на %s %s: %s", resp.Status, req.Method, req.URL.Path, strings.TrimSpace(string(data)))
	}
	return resp, nil
}
