package messagetext

import "testing"

func TestNeutralizeImagePaths(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{name: "trailing absolute path", in: "Kanit: /srv/probot/lms/kanıt.png", want: "Kanit: `/srv/probot/lms/kanıt.png`", count: 1},
		{name: "path at line end mid-message", in: "first line\n/path.jpg\nlast line", want: "first line\n`/path.jpg`\nlast line", count: 1},
		{name: "two paths split by Claude", in: "a.png /b.jpg", want: "`a.png` `/b.jpg`", count: 2},
		{name: "single quoted path", in: "'/x.png'", want: "`'/x.png'`", count: 1},
		{name: "quoted Windows path", in: `"C:\x.PNG"`, want: "`\"C:\\x.PNG\"`", count: 1},
		{name: "relative suffix", in: "see foo.webp", want: "`see foo.webp`", count: 1},
		{name: "preserve surrounding whitespace", in: "  /x.png  ", want: "  `/x.png`  ", count: 1},
		{name: "path followed by text", in: "see /x.png please", want: "see /x.png please", count: 0},
		{name: "already backticked", in: "`/x.png`", want: "`/x.png`", count: 0},
		{name: "suffix is not final", in: "/x.png.", want: "/x.png.", count: 0},
		{name: "multiline Turkish", in: "İlk satır\nkanıt: /tmp/öğrenci.gif\nson satır", want: "İlk satır\nkanıt: `/tmp/öğrenci.gif`\nson satır", count: 1},
		{name: "CRLF-free multiline and blanks", in: "birinci\n\nikinci /x.jpeg\nüçüncü", want: "birinci\n\nikinci `/x.jpeg`\nüçüncü", count: 1},
		{name: "slash commands", in: "/rename worker-1", want: "/rename worker-1", count: 0},
		{name: "compact command", in: "/compact", want: "/compact", count: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, count := NeutralizeImagePaths(tt.in)
			if got != tt.want || count != tt.count {
				t.Fatalf("NeutralizeImagePaths(%q) = %q, %d; want %q, %d", tt.in, got, count, tt.want, tt.count)
			}
			again, secondCount := NeutralizeImagePaths(got)
			if again != got || secondCount != 0 {
				t.Fatalf("neutralization is not idempotent: second result %q, %d", again, secondCount)
			}
		})
	}
}

func TestClaudeImageTransformMatchesPasteSplit(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "Kanit: /srv/kanıt.png", want: "Kanit:", ok: true},
		{in: "a.png /b.jpg\nkeep this", want: "keep this", ok: true},
		{in: "see /x.png please", want: "see /x.png please", ok: false},
	} {
		got, ok := ClaudeImageTransform(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("ClaudeImageTransform(%q) = %q, %t; want %q, %t", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
