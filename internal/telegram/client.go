package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const defaultAPIBaseURL = "https://api.telegram.org"

const (
	proxyModeNone   = "none"
	proxyModeCore   = "core"
	proxyModeCustom = "custom"
)

type botClient struct {
	http     *http.Client
	base     string
	fileBase string
	redact   *strings.Replacer
	proxy    string
}

type apiError struct {
	status      int
	description string
	retryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram API status %d: %s", e.status, e.description)
}

func newBotClient(token, base, proxyMode, proxy string) (*botClient, error) {
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("invalid api_base_url")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	replacements := []string{token, "<redacted>", url.PathEscape(token), "<redacted>"}
	var proxyURL *url.URL
	var customProxy string
	switch proxyMode {
	case proxyModeNone:
		transport.Proxy = nil
	case proxyModeCore:
		transport.Proxy = http.ProxyFromEnvironment
		proxyURL, _ = http.ProxyFromEnvironment(&http.Request{URL: baseURL})
	case proxyModeCustom:
		if proxy == "" {
			return nil, errors.New("invalid proxy")
		}
		proxyURL, err = url.Parse(proxy)
		if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5") || proxyURL.RawQuery != "" || proxyURL.Fragment != "" {
			return nil, errors.New("invalid proxy URL")
		}
		customProxy = proxy
		replacements = append(replacements, proxy, "<redacted>")
		transport.Proxy = http.ProxyURL(proxyURL)
	default:
		return nil, errors.New("invalid proxy_mode")
	}
	if proxyURL != nil {
		replacements = append(replacements, proxyURL.String(), "<redacted>", proxyURL.Host, "<redacted>", proxyURL.Hostname(), "<redacted>")
		if proxyURL.User != nil {
			user := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			for _, secret := range []string{proxyURL.User.String(), user, password} {
				if secret != "" {
					replacements = append(replacements, secret, "<redacted>")
				}
			}
		}
	}
	// 禁止跟随外部重定向，避免 Bot 凭证被传给新的 origin。
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base = strings.TrimRight(base, "/")
	botPath := "bot" + url.PathEscape(token) + "/"
	return &botClient{
		http: client, base: base + "/" + botPath, fileBase: base + "/file/" + botPath,
		redact: strings.NewReplacer(replacements...), proxy: customProxy,
	}, nil
}

type botIdentity struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

func (c *botClient) getMe(ctx context.Context) (botIdentity, error) {
	var me botIdentity
	if err := c.call(ctx, "getMe", struct{}{}, &me); err != nil {
		return botIdentity{}, err
	}
	if me.ID == 0 {
		return botIdentity{}, errors.New("telegram getMe returned no account ID")
	}
	return me, nil
}

func (c *botClient) call(ctx context.Context, method string, request any, result any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return c.request(ctx, method, "application/json", bytes.NewReader(body), result)
}

func (c *botClient) setFileBase(base, token string) error {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid file_base_url")
	}
	c.fileBase = strings.TrimRight(base, "/") + "/file/bot" + url.PathEscape(token) + "/"
	return nil
}

func (c *botClient) multipart(ctx context.Context, method string, params map[string]any, uploads []plannedUpload, writeFile func(context.Context, io.Writer, plannedUpload) error, result any) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, body := io.Pipe()
	writer := multipart.NewWriter(body)
	done := make(chan error, 1)
	go func() {
		err := writeMultipart(ctx, writer, params, uploads, writeFile)
		body.CloseWithError(err)
		done <- err
	}()
	err := c.request(ctx, method, writer.FormDataContentType(), reader, result)
	cancel()
	reader.CloseWithError(err)
	writeErr := <-done
	return errors.Join(err, writeErr)
}

func writeMultipart(ctx context.Context, writer *multipart.Writer, params map[string]any, uploads []plannedUpload, writeFile func(context.Context, io.Writer, plannedUpload) error) error {
	for name, value := range params {
		var field string
		if text, ok := value.(string); ok {
			field = text
		} else {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			field = string(encoded)
		}
		if err := writer.WriteField(name, field); err != nil {
			return err
		}
	}
	for _, upload := range uploads {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": upload.field, "filename": upload.filename}))
		contentType := upload.mime
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		header.Set("Content-Type", contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		if err := writeFile(ctx, part, upload); err != nil {
			return err
		}
	}
	return writer.Close()
}

func (c *botClient) request(ctx context.Context, method, contentType string, body io.Reader, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, body)
	if err != nil {
		return c.safeError(err)
	}
	req.Header.Set("Content-Type", contentType)
	response, err := c.http.Do(req)
	if err != nil {
		return c.safeError(err)
	}
	defer response.Body.Close()
	var envelope struct {
		OK          *bool           `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode telegram response: %w", c.safeError(err))
	}
	if envelope.OK == nil {
		return errors.New("telegram response has no ok field")
	}
	if raw, ok := result.(*json.RawMessage); ok {
		*raw = envelope.Result
	}
	if !*envelope.OK {
		status := response.StatusCode
		if envelope.ErrorCode != 0 {
			status = envelope.ErrorCode
		}
		return &apiError{status: status, description: c.redact.Replace(envelope.Description), retryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected telegram HTTP status %d", response.StatusCode)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("decode telegram result: %w", c.safeError(err))
	}
	return nil
}

// safeError 先复制 url.Error 再清理 URL 和底层文本，保留超时与取消判断。
func (c *botClient) safeError(err error) error {
	var original *url.Error
	if errors.As(err, &original) {
		clean := *original
		clean.URL = c.redact.Replace(original.URL)
		clean.Err = &sanitizedError{message: c.redact.Replace(original.Err.Error()), cause: original.Err}
		return &clean
	}
	return &sanitizedError{message: c.redact.Replace(err.Error()), cause: err}
}

type sanitizedError struct {
	message string
	cause   error
}

func (e *sanitizedError) Error() string        { return e.message }
func (e *sanitizedError) Is(target error) bool { return errors.Is(e.cause, target) }
func (e *sanitizedError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.cause, &timeout) && timeout.Timeout()
}
