// 从 seeinps/seeinpm 二进制包中提取构建时注入的版本号。
// 构建脚本在二进制内注入标识串 "seeinp-version:<版本号>"（见 scripts/build.sh 的 -ldflags Banner）。
// 浏览器本地识别：选择文件后立即执行，无需上传到服务器。

const MAGIC = 'seeinp-version:'

export async function extractVersionFromFile(file: File): Promise<string | null> {
  const buf = await file.arrayBuffer()
  // latin1 解码：字节与字符一一对应，V8 的 indexOf 为原生实现，大文件扫描很快
  const text = new TextDecoder('latin1').decode(buf)
  const idx = text.indexOf(MAGIC)
  if (idx < 0) return null
  // 版本号为可打印 ASCII（数字与点），遇到非可打印字节（NUL 等）即结束
  const m = /^[\x21-\x7e]{1,32}/.exec(text.slice(idx + MAGIC.length, idx + MAGIC.length + 32))
  return m ? m[0] : null
}
