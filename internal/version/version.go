package version

import (
	"bytes"
	"errors"
	"io"
	"os"
)

var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Banner 构建时注入 "seeinp-version:<版本号>"（见 scripts/build.sh），
// 上传升级包时服务端扫描该标识自动识别包内版本
var Banner = "seeinp-version:dev"

const versionMagic = "seeinp-version:"

var ErrVersionNotFound = errors.New("二进制中未找到版本标识，请使用 scripts/build.sh 构建")

// ExtractVersionFromFile 从升级包二进制中扫描构建时注入的版本号（流式扫描 + 定位精读）
func ExtractVersionFromFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	magic := []byte(versionMagic)
	buf := make([]byte, 512*1024)
	var carry []byte // 上一块尾部，防 magic 跨块
	var base int64   // buf 当前块起始的文件偏移
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			data := append(append([]byte{}, carry...), buf[:n]...)
			if idx := bytes.Index(data, magic); idx >= 0 {
				// magic 结束处在文件中的绝对偏移
				absEnd := base - int64(len(carry)) + int64(idx) + int64(len(magic))
				tail := make([]byte, 64)
				n2, _ := f.ReadAt(tail, absEnd)
				end := 0
				for end < n2 && tail[end] > ' ' && end < 32 {
					end++
				}
				if end == 0 {
					return "", ErrVersionNotFound
				}
				return string(tail[:end]), nil
			}
			base += int64(n)
			keep := len(magic) - 1
			if len(data) > keep {
				carry = data[len(data)-keep:]
			} else {
				carry = data
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return "", ErrVersionNotFound
			}
			return "", rerr
		}
	}
}
