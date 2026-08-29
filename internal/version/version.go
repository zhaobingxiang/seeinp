package version

import (
	"errors"
	"os"
	"regexp"
)

var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Banner 构建时注入 "seeinp-version:<版本号>"（见 scripts/build.sh），
// 上传升级包时服务端扫描该标识自动识别包内版本
var Banner = "seeinp-version:dev"

// VersionMagic 二进制内的版本标识前缀。
// 注意：本包及内嵌前端的代码中也含有该前缀字面量，因此扫描时必须
// 用"后随五段数字"的正则过滤，第一个非法匹配（如 JS 常量）会被跳过。
const VersionMagic = "seeinp-version:"

var versionRe = regexp.MustCompile(`seeinp-version:(\d{1,4}(?:\.\d{1,4}){4})`)

var ErrVersionNotFound = errors.New("二进制中未找到版本标识，请使用 scripts/build.sh 构建")

// ExtractVersionFromFile 从升级包二进制中提取构建时注入的版本号
func ExtractVersionFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, m := range versionRe.FindAllSubmatch(data, -1) {
		return string(m[1]), nil
	}
	return "", ErrVersionNotFound
}
