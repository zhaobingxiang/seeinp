// Package guard 提供登录防护：连续失败计数、账号锁定与一次性图形验证码。
// 面向 intranet 单管理端/B端本地控制台，状态保存在内存中（进程重启即清零，
// 服务不会自行重启，足以阻挡口令暴力破解）。
package guard

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

const (
	// CaptchaThreshold 密码连续错误达到该值后，后续登录除密码外还需验证码
	CaptchaThreshold = 3
	// LockThreshold 密码连续错误达到该值后锁定账号
	LockThreshold = 6
	// LockDuration 锁定时长
	LockDuration = 30 * time.Minute
	// CaptchaTTL 验证码有效期
	CaptchaTTL = 2 * time.Minute
	captchaLen = 4
	// captchaIDBytes 验证码 id 随机字节长度
	captchaIDBytes = 16
)

// codeChars 去掉易混淆字符（0/O、1/I/L、2/Z、5/S、8/B、6/G、9/P 等），
// 保证生成的验证码清晰可辨且字符集足够。
const codeChars = "2347ACDEFHJKMNPRTUVWXY"

type captcha struct {
	code   string
	expire time.Time
}

// LoginGuard 按账号维护连续失败计数、锁定状态与一次性图形验证码。
type LoginGuard struct {
	mu          sync.Mutex
	failed      map[string]int
	lockedUntil map[string]time.Time
	captchas    map[string]captcha
}

func New() *LoginGuard {
	return &LoginGuard{
		failed:      make(map[string]int),
		lockedUntil: make(map[string]time.Time),
		captchas:    make(map[string]captcha),
	}
}

// Locked 返回账号是否处于锁定状态；若已过期则自动清除并视为未锁定。
// 返回剩余锁定秒数（仅锁定状态有意义）。
func (g *LoginGuard) Locked(username string) (locked bool, remaining int64, failed int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	failed = g.failed[username]
	until, ok := g.lockedUntil[username]
	if !ok {
		return false, 0, failed
	}
	if time.Now().Before(until) {
		return true, int64(time.Until(until).Seconds()) + 1, failed
	}
	delete(g.lockedUntil, username)
	return false, 0, failed
}

// OnFailure 记录一次密码连续失败；到达 LockThreshold 时锁定账号。
// 返回最新连续失败次数；若本次触发锁定则返回剩余锁定秒数（否则为 0）。
func (g *LoginGuard) OnFailure(username string) (failed int, lockSec int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failed[username]++
	failed = g.failed[username]
	if failed >= LockThreshold {
		g.lockedUntil[username] = time.Now().Add(LockDuration)
		lockSec = int64(LockDuration / time.Second)
	}
	return failed, lockSec
}

// Reset 登录成功后清零失败计数并解除锁定。
func (g *LoginGuard) Reset(username string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failed, username)
	delete(g.lockedUntil, username)
}

// NeedsCaptcha 判断当前连续失败是否已达到需要验证码的阈值。
func (g *LoginGuard) NeedsCaptcha(username string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.failed[username] >= CaptchaThreshold
}

// CreateCaptcha 生成一次性图形验证码，返回 id 与可直接用于 <img src> 的 data URL。
func (g *LoginGuard) CreateCaptcha() (id, imageBase64 string) {
	code := captchaCode()
	id = captchaID()
	g.mu.Lock()
	g.captchas[id] = captcha{code: code, expire: time.Now().Add(CaptchaTTL)}
	g.mu.Unlock()
	return id, "data:image/svg+xml;base64," +
		base64.StdEncoding.EncodeToString([]byte(svgCaptcha(code)))
}

// VerifyCaptcha 校验验证码（大小写不敏感、一次性、过期无效），成功即作废。
func (g *LoginGuard) VerifyCaptcha(id, answer string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.captchas[id]
	if !ok {
		return false
	}
	delete(g.captchas, id)
	if time.Now().After(c.expire) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(answer), c.code)
}

// ---- 随机数与 SVG 生成 ----

var (
	randMu   sync.Mutex
	randSrc  = rand.New(rand.NewSource(time.Now().UnixNano()))
	hexChars = "0123456789abcdef"
)

func randInt(n int) int {
	randMu.Lock()
	defer randMu.Unlock()
	return randSrc.Intn(n)
}

func captchaCode() string {
	var sb strings.Builder
	for i := 0; i < captchaLen; i++ {
		sb.WriteByte(codeChars[randInt(len(codeChars))])
	}
	return sb.String()
}

func captchaID() string {
	b := make([]byte, captchaIDBytes)
	for i := range b {
		b[i] = hexChars[randInt(len(hexChars))]
	}
	return string(b)
}

// svgCaptcha 用系统字体绘制带轻微旋转/平移/干扰线的 4 位验证码，接口返回 SVG。
// 验证码仅作口令爆破节流闸门（其后还有失败锁定兜底），非强安全要素，系统字体即可。
func svgCaptcha(code string) string {
	const w, h = 132, 42
	var sb strings.Builder
	light := func() string {
		return fmt.Sprintf("#%02x%02x%02x", 198+randInt(55), 198+randInt(55), 198+randInt(55))
	}
	dark := func() string {
		return fmt.Sprintf("#%02x%02x%02x", randInt(70), randInt(70), randInt(70))
	}
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d">`, w, h))
	sb.WriteString(fmt.Sprintf(`<rect width="%d" height="%d" fill="%s"/>`, w, h, light()))
	// 干扰线
	for i := 0; i < 4; i++ {
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="1" stroke-opacity="0.55"/>`,
			randInt(w), randInt(h), randInt(w), randInt(h), dark()))
	}
	// 干扰点
	for i := 0; i < 24; i++ {
		sb.WriteString(fmt.Sprintf(`<circle cx="%d" cy="%d" r="1.2" fill="%s" fill-opacity="0.6"/>`,
			randInt(w), randInt(h), dark()))
	}
	// 字符
	step := w / (captchaLen + 1)
	for i := 0; i < captchaLen; i++ {
		x := step*(i+1) - 6 + randInt(12)
		y := 29 + randInt(7)
		rot := randInt(50) - 25
		fs := 22 + randInt(5)
		sb.WriteString(fmt.Sprintf(`<text x="%d" y="%d" font-family="monospace" font-size="%d" font-weight="bold" fill="%s" transform="rotate(%d %d %d)">%c</text>`,
			x, y, fs, dark(), rot, x, y, code[i]))
	}
	sb.WriteString(`</svg>`)
	return sb.String()
}