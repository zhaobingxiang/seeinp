package guard

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// 连续失败 1-3 次：不锁定、不触发验证码阈值
func TestFailuresBelowThreshold(t *testing.T) {
	g := New()
	for i := 1; i <= 3; i++ {
		failed, lockSec := g.OnFailure("admin")
		if failed != i {
			t.Fatalf("第 %d 次失败后计数应为 %d, got %d", i, i, failed)
		}
		if lockSec != 0 {
			t.Fatalf("未达锁定阈值不应有锁定时长, got %d", lockSec)
		}
		if i < 3 && g.NeedsCaptcha("admin") {
			t.Fatalf("失败 3 次前不应要求验证码")
		}
		if i == 3 && !g.NeedsCaptcha("admin") {
			t.Fatalf("失败 3 次后应要求验证码")
		}
		if locked, _, _ := g.Locked("admin"); locked {
			t.Fatalf("失败 3 次不应锁定")
		}
	}
}

// 到达 CaptchaThreshold 后，下一次登录需验证码；锁定在第 6 次失败触发
func TestThresholdLock(t *testing.T) {
	g := New()
	for i := 1; i <= 3; i++ {
		g.OnFailure("admin")
	}
	if !g.NeedsCaptcha("admin") {
		t.Fatalf("失败 3 次后应要求验证码")
	}
	// 继续失败到第 6 次应触发锁定
	for i := 4; i <= 6; i++ {
		failed, lockSec := g.OnFailure("admin")
		if failed != i {
			t.Fatalf("计数应为 %d, got %d", i, failed)
		}
		if i == 6 {
			if lockSec <= 0 {
				t.Fatalf("第 6 次失败应触发锁定")
			}
			if locked, rem, _ := g.Locked("admin"); !locked || rem <= 0 {
				t.Fatalf("第 6 次失败后应处于锁定状态 (locked=%v rem=%d)", locked, rem)
			}
		} else if lockSec != 0 {
			t.Fatalf("第 %d 次失败不应触发锁定", i)
		}
	}
}

// 锁定用时长
func TestLockDuration(t *testing.T) {
	g := New()
	for i := 0; i < 6; i++ {
		g.OnFailure("u")
	}
	_, rem, _ := g.Locked("u")
	if rem < 1795 || rem > 1810 {
		t.Fatalf("锁定时长应约 1800s, got %d", rem)
	}
}

// 登录成功清零并解除锁定
func TestReset(t *testing.T) {
	g := New()
	for i := 0; i < 6; i++ {
		g.OnFailure("u")
	}
	if locked, _, _ := g.Locked("u"); !locked {
		t.Fatalf("应处于锁定")
	}
	g.Reset("u")
	if locked, _, failed := g.Locked("u"); locked || failed != 0 {
		t.Fatalf("重置后不应锁定且计数清零, locked=%v failed=%d", locked, failed)
	}
	if g.NeedsCaptcha("u") {
		t.Fatalf("重置后不应要求验证码")
	}
}

// 验证码生成 + 一次性 + 大小写不敏感 + 过期失效
func TestCaptcha(t *testing.T) {
	g := New()
	id, img := g.CreateCaptcha()
	if id == "" || !strings.HasPrefix(img, "data:image/svg+xml;base64,") {
		t.Fatalf("验证码 id/dataURL 格式异常: id=%q img prefix=%q", id, img[:40])
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(img, "data:image/svg+xml;base64,")); err != nil {
		t.Fatalf("SVG base64 解码失败: %v", err)
	}

	// 无法从接口拿到明文 code，这里通过内部 map 校验一次性语义
	g.mu.Lock()
	code := g.captchas[id].code
	g.mu.Unlock()
	if code == "" {
		t.Fatalf("未存储验证码")
	}
	if !g.VerifyCaptcha(id, strings.ToLower(code)) {
		t.Fatalf("正确验证码（忽略大小写）应通过")
	}
	if g.VerifyCaptcha(id, code) {
		t.Fatalf("验证码应一次性使用，重复校验应失败")
	}
	if g.VerifyCaptcha("no-such-id", code) {
		t.Fatalf("不存在的验证码 id 应校验失败")
	}

	// 过期失效
	id2, _ := g.CreateCaptcha()
	g.mu.Lock()
	code2 := g.captchas[id2].code
	g.captchas[id2] = captcha{code: code2, expire: time.Now().Add(-time.Second)}
	g.mu.Unlock()
	if g.VerifyCaptcha(id2, code2) {
		t.Fatalf("过期验证码应失效")
	}
}

// 不同账号独立计数
func TestIsolationByAccount(t *testing.T) {
	g := New()
	for i := 0; i < 6; i++ {
		g.OnFailure("attacker")
	}
	if locked, _, _ := g.Locked("attacker"); !locked {
		t.Fatalf("attacker 应被锁定")
	}
	if locked, _, failed := g.Locked("good"); locked || failed != 0 {
		t.Fatalf("good 账号不应受 attacker 影响, locked=%v failed=%d", locked, failed)
	}
}