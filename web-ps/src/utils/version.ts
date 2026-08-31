// 从 seeinps/seeinpm 二进制包中提取构建时注入的版本号。
// 构建脚本在二进制内注入标识串 "seeinp-version:<版本号>"（见 scripts/build.sh 的 -ldflags Banner）。
// 浏览器本地识别：选择文件后立即执行，无需上传到服务器。

// 五段数字版本号：1.0.26.0829.01（大版本.大版本.年.月日.当日序号）
// 注意：二进制内嵌前端/后端源码中也会出现 "seeinp-version:" 字面量（如正则源码、dev 默认值），
// 因此必须要求 "seeinp-version:" 后紧随合法的五段数字，且需跳过所有非法命中直至取到真正的版本号。
const VERSION_RE = /seeinp-version:(\d{1,4}\.\d{1,4}\.\d{1,4}\.\d{1,4}\.\d{1,4})/g

export async function extractVersionFromFile(file: File): Promise<string | null> {
  const buf = await file.arrayBuffer()
  // latin1 解码：字节与字符一一对应，正则为原生实现，大文件扫描很快
  const text = new TextDecoder('latin1').decode(buf)
  // 从索引 0 开始全局扫描，命中 "seeinp-version:" 后随五段数字即视为构建注入的版本号；
  // 取首个合法结果（Go 侧脚本注入的 Banner 与源码字面量通常共存，首个五段数字命中即正确版本）
  VERSION_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = VERSION_RE.exec(text)) !== null) {
    const v = m[1]
    if (isValidVersion(v)) return v
  }
  return null
}

function isValidVersion(v: string): boolean {
  return !!v && /^\d{1,4}(\.\d{1,4}){4}$/.test(v)
}
