package version

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractVersionFromFile 验证从二进制中提取构建时注入的版本标识，
// 覆盖：标识跨读取块边界、版本号位于文件末尾、以及未包含标识的文件
func TestExtractVersionFromFile(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		content []byte
		want    string
		wantErr bool
	}{
		{
			name:    "常规",
			content: append([]byte("go build junk\x00seeinp-version:"), []byte("1.0.26.0830.04\x00more junk")...),
			want:    "1.0.26.0830.04",
		},
		{
			name:    "版本号位于文件末尾",
			content: append([]byte("junk\x00seeinp-version:"), []byte("1.0.26.0830.04")...),
			want:    "1.0.26.0830.04",
		},
		{
			name:    "跳过 JS 常量形式的非数字匹配",
			content: []byte("var MAGIC = 'seeinp-version:'; real \x00seeinp-version:1.0.26.0830.04\x00"),
			want:    "1.0.26.0830.04",
		},
		{
			name:    "无标识",
			content: []byte("some other binary content without marker"),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".bin")
			if err := os.WriteFile(p, tc.content, 0644); err != nil {
				t.Fatal(err)
			}
			got, err := ExtractVersionFromFile(p)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
