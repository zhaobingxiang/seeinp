//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI（CryptProtectData/CryptUnprotectData）加密敏感字段，替代原 proxytovpn 的 XOR 弱加密。
// 密文与当前 Windows 用户绑定，换机/换用户后不可解（按未设置处理）。
var (
	crypt32       = windows.NewLazyDLL("crypt32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func cryptCall(proc *windows.LazyProc, in []byte) ([]byte, error) {
	var inBlob dataBlob
	if len(in) > 0 {
		inBlob.cbData = uint32(len(in))
		inBlob.pbData = &in[0]
	}
	var outBlob dataBlob
	r, _, err := proc.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&outBlob)),
	)
	if r == 0 {
		return nil, fmt.Errorf("DPAPI 调用失败: %v", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.pbData)))
	out := make([]byte, outBlob.cbData)
	copy(out, unsafe.Slice(outBlob.pbData, outBlob.cbData))
	return out, nil
}

func encryptString(plain string) (string, error) {
	b, err := cryptCall(procProtect, []byte(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func decryptString(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	b, err := cryptCall(procUnprotect, raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
