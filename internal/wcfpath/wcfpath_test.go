package wcfpath

import "testing"

func TestToWine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{`C:\Users\wechat\a.png`, `C:\Users\wechat\a.png`},
		{`Z:\app\data\upload\a.png`, `Z:\app\data\upload\a.png`},
		{"/app/data/upload/a.png", `Z:\app\data\upload\a.png`},
		{"/home/wechat/WeChat Files/x", `Z:\home\wechat\WeChat Files\x`},
		{"relative/path.png", "relative/path.png"},
	}
	for _, tc := range cases {
		if got := ToWine(tc.in); got != tc.want {
			t.Errorf("ToWine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
