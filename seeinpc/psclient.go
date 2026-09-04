package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// psClient seeinps 管理端 API 客户端（登录防护与 web-ps 同源：错3次验证码、6次锁定30分）
type psClient struct {
	base string // http://host:port
	hc   *http.Client
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func newPSClient(addr string) *psClient {
	a := strings.TrimSpace(addr)
	a = strings.TrimPrefix(a, "http://")
	a = strings.TrimPrefix(a, "https://")
	if !strings.Contains(a, ":") {
		a += ":65443" // seeinps 管理端默认端口（conf/seeinps.toml [local] bend_addr）
	}
	return &psClient{
		base: "http://" + a,
		hc:   &http.Client{Timeout: 12 * time.Second},
	}
}

func (c *psClient) call(ctx context.Context, method, path, token string, body any) (*apiEnvelope, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("无法连接 seeinps（%s）: %w", c.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("seeinps 响应非预期（HTTP %d）", resp.StatusCode)
	}
	return &env, resp.StatusCode, nil
}

// loginResult 登录结果（含验证码/锁定交互信息）
type loginResult struct {
	OK            bool
	Token         string
	NeedCaptcha   bool
	Locked        bool
	LockRemaining int
	Message       string
}

func (c *psClient) login(ctx context.Context, user, pwd, captchaID, captchaText string) (*loginResult, error) {
	body := map[string]string{
		"username":     user,
		"password":     pwd,
		"captcha_id":   captchaID,
		"captcha_text": captchaText,
	}
	env, _, err := c.call(ctx, http.MethodPost, "/api/v1/auth/login", "", body)
	if err != nil {
		return nil, err
	}
	switch env.Code {
	case 0:
		var d struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return nil, fmt.Errorf("解析令牌失败: %w", err)
		}
		return &loginResult{OK: true, Token: d.Token}, nil
	case 1001: // 用户名或密码错误
		var d struct {
			NeedCaptcha bool `json:"need_captcha"`
			FailedCount int  `json:"failed_count"`
		}
		_ = json.Unmarshal(env.Data, &d)
		return &loginResult{NeedCaptcha: d.NeedCaptcha, Message: "用户名或密码错误"}, nil
	case 1103: // 锁定
		var d struct {
			LockRemaining int `json:"lock_remaining"`
		}
		_ = json.Unmarshal(env.Data, &d)
		return &loginResult{Locked: true, LockRemaining: d.LockRemaining,
			Message: fmt.Sprintf("失败次数过多，账号已锁定（剩余 %d 秒）", d.LockRemaining)}, nil
	case 1104: // 验证码错误
		return &loginResult{NeedCaptcha: true, Message: "验证码错误，请重新输入"}, nil
	case 3002:
		return &loginResult{Message: "该 seeinps 尚未初始化"}, nil
	default:
		return &loginResult{Message: env.Message}, nil
	}
}

// captcha 获取图形验证码（连续错 3 次后需要）
func (c *psClient) captcha(ctx context.Context) (id, imageB64 string, err error) {
	env, _, err := c.call(ctx, http.MethodGet, "/api/v1/auth/captcha", "", nil)
	if err != nil {
		return "", "", err
	}
	if env.Code != 0 {
		return "", "", fmt.Errorf("获取验证码失败: %s", env.Message)
	}
	var d struct {
		CaptchaID   string `json:"captcha_id"`
		ImageBase64 string `json:"image_base64"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return "", "", err
	}
	return d.CaptchaID, d.ImageBase64, nil
}

// opsProxy seeinps 的一条运维 HTTP 代理
type opsProxy struct {
	ID        string   `json:"id"`
	OpsID     int      `json:"opsId"` // 运维ID = 对外端口（forwardPort）
	ProxyUser string   `json:"proxyUsername"`
	ACL       []string `json:"acl"`
}

// opsProxies 拉取 type=ops_http 的代理列表
func (c *psClient) opsProxies(ctx context.Context, token string) ([]opsProxy, error) {
	env, _, err := c.call(ctx, http.MethodGet, "/api/v1/proxies", token, nil)
	if err != nil {
		return nil, err
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("获取代理列表失败: %s", env.Message)
	}
	var items []map[string]any
	if err := json.Unmarshal(env.Data, &items); err != nil {
		return nil, err
	}
	out := make([]opsProxy, 0, len(items))
	for _, it := range items {
		if fmt.Sprint(it["type"]) != "ops_http" {
			continue
		}
		p := opsProxy{}
		p.ID = fmt.Sprint(it["id"])
		if v, ok := it["opsId"].(float64); ok {
			p.OpsID = int(v)
		}
		if v, ok := it["proxyUsername"].(string); ok {
			p.ProxyUser = v
		}
		if arr, ok := it["acl"].([]any); ok {
			for _, a := range arr {
				if s, ok2 := a.(string); ok2 {
					p.ACL = append(p.ACL, s)
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// loginFallback 按固定服务器地址列表逐个尝试登录 seeinps 管理端。
// seeinps 管理端口（65443）通常不对外映射，仅 web-ui 外部端口可达，服务器地址固定不可填；
// 仅"连接失败"才回退下一台，登录业务结果（密码错/锁定/验证码）以首台可达者为准。
func loginFallback(hosts []string, port int, user, pwd, captchaID, captchaText string) (*psClient, *loginResult, error) {
	var lastErr error
	for _, h := range hosts {
		c := newPSClient(net.JoinHostPort(h, strconv.Itoa(port)))
		lr, err := c.login(context.Background(), user, pwd, captchaID, captchaText)
		if err != nil {
			lastErr = err
			continue
		}
		return c, lr, nil
	}
	return nil, nil, lastErr
}
