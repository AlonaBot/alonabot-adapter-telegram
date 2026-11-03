package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIBaseURL = "https://api.telegram.org"

type botClient struct {
	http     *http.Client
	base     string
	fileBase string
	redact   *strings.Replacer
}

type apiError struct {
	status      int
	description string
	retryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram API status %d: %s", e.status, e.description)
}

func newBotClient(token, base, proxy string) (*botClient, error) {
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("invalid api_base_url")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	replacements := []string{token, "<redacted>", url.PathEscape(token), "<redacted>"}
	if proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5") || proxyURL.RawQuery != "" || proxyURL.Fragment != "" {
			return nil, errors.New("invalid proxy URL")
		}
		if proxyURL.User != nil {
			user := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			for _, secret := range []string{proxyURL.User.String(), user, password} {
				if secret != "" {
					replacements = append(replacements, secret, "<redacted>")
				}
			}
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	// 禁止跟随外部重定向，避免 Bot 凭证被传给新的 origin。
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base = strings.TrimRight(base, "/")
	botPath := "bot" + url.PathEscape(token) + "/"
	return &botClient{
		http: client, base: base + "/" + botPath, fileBase: base + "/file/" + botPath,
		redact: strings.NewReplacer(replacements...),
	}, nil
}

func (c *botClient) call(ctx context.Context, method string, request any, result any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bytes.NewReader(body))
	if err != nil {
		return c.safeError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return c.safeError(err)
	}
	defer response.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	err = json.NewDecoder(response.Body).Decode(&envelope)
	if response.StatusCode != http.StatusOK || !envelope.OK {
		status := response.StatusCode
		if envelope.ErrorCode != 0 {
			status = envelope.ErrorCode
		}
		description := envelope.Description
		if err != nil {
			description = err.Error()
		}
		return &apiError{status: status, description: c.redact.Replace(description), retryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if err != nil {
		return fmt.Errorf("decode telegram response: %w", c.safeError(err))
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
