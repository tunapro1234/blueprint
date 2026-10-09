// Command core exports Go reference results for the Rust bp-core crate into
// testdata/golden/core/*.json. Run from the repository root:
//
//	go run ./tools/goldenexport/core [-out testdata/golden/core]
//
// Every corpus here is deterministic, so regenerating without a Go behavior
// change produces identical files.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blueprint/internal/config"
	"blueprint/internal/messagetext"
)

func main() {
	out := flag.String("out", "testdata/golden/core", "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}
	write(*out, "gojson.json", gojsonGolden())
	write(*out, "gotime.json", timeGolden())
	write(*out, "messagetext.json", messagetextGolden())
	write(*out, "config.json", configGolden())
	write(*out, "update_remote.json", updateRemoteGolden())
}

func write(dir, name string, value any) {
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0o644); err != nil {
		panic(err)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustMarshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return string(data)
}

func mustIndent(v any, prefix, indent string) string {
	data, err := json.MarshalIndent(v, prefix, indent)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return string(data)
}

func encodeNoHTML(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "ERROR: " + err.Error()
	}
	return buf.String()
}

// ---- gojson ----

type floatCase struct {
	Bits string `json:"bits"`
	Out  string `json:"out"`
	Out2 string `json:"out32"`
}

type stringCase struct {
	In     string `json:"in_b64"`
	HTML   string `json:"html"`
	NoHTML string `json:"nohtml"`
}

type valueCase struct {
	In      string `json:"in_b64"`
	Error   string `json:"error,omitempty"`
	Marshal string `json:"marshal,omitempty"`
	Indent  string `json:"indent,omitempty"`
	Prefix  string `json:"indent_prefix,omitempty"`
	Encoder string `json:"encoder_nohtml,omitempty"`
}

// Sample is mirrored by a Rust struct in crates/bp-core/src/gojson/tests.rs.
type Sample struct {
	Name     string            `json:"name"`
	Note     string            `json:"note,omitempty"`
	Count    int               `json:"count"`
	Ratio    float64           `json:"ratio"`
	Small    float32           `json:"small"`
	Flag     bool              `json:"flag,omitempty"`
	Tags     []string          `json:"tags"`
	Empty    []string          `json:"empty,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Scores   map[string]int    `json:"scores"`
	Blob     []byte            `json:"blob,omitempty"`
	Ptr      *int              `json:"ptr"`
	When     time.Time         `json:"when"`
	Wait     time.Duration     `json:"wait"`
	Nested   *Inner            `json:"nested,omitempty"`
	Raw      json.RawMessage   `json:"raw,omitempty"`
	Anything any               `json:"anything"`
}

type Inner struct {
	Path string `json:"path"`
	Deep []int  `json:"deep"`
}

type structCase struct {
	ID      int    `json:"id"`
	Marshal string `json:"marshal"`
	Indent  string `json:"indent"`
	Encoder string `json:"encoder_nohtml"`
}

// Lenient is decoded case-insensitively by Go; the Rust test mirrors it.
type Lenient struct {
	Name  string            `json:"name"`
	Count int               `json:"count"`
	Inner LenientInner      `json:"inner"`
	List  []LenientInner    `json:"list"`
	Map   map[string]string `json:"map"`
	Ptr   *string           `json:"ptr"`
}

type LenientInner struct {
	X string `json:"x"`
	Y int    `json:"y"`
}

type lenientCase struct {
	In    string `json:"in_b64"`
	Error bool   `json:"error"`
	Out   string `json:"out,omitempty"`
}

func gojsonGolden() any {
	r := rand.New(rand.NewSource(20261009))
	floats := []float64{0, math.Copysign(0, -1), 1, -1, 0.1, 0.2, 0.3, 1.5, 100, 1e6, 1e20, 1e21, 1e22, -1e21,
		123456789012345678, 1.7976931348623157e308, 5e-324, 2.2250738585072014e-308, 1e-6, 9.999999e-7, 1e-7,
		0.000001, 0.0000012345, 1.23456789e-10, 3.14159265358979, 2.5e-5, 12345.678, 1 << 53, 1<<53 + 1, 1e15, 1e16,
		4.35e21, 1.0e-100, 7e-10, 33.33333333333333, float64(float32(0.1)), float64(float32(1e-7))}
	for i := 0; i < 300; i++ {
		var f float64
		switch i % 3 {
		case 0:
			f = math.Float64frombits(r.Uint64())
		case 1:
			f = r.NormFloat64() * math.Pow(10, float64(r.Intn(60)-30))
		default:
			f = float64(r.Int63n(1<<40)) / math.Pow(10, float64(r.Intn(12)))
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		floats = append(floats, f)
	}
	var fc []floatCase
	for _, f := range floats {
		fc = append(fc, floatCase{Bits: fmt.Sprintf("%016x", math.Float64bits(f)), Out: mustMarshal(f), Out2: mustMarshal(float32(f))})
	}

	strs := []string{"", "plain", "<script>&amp;</script>", "quote\"back\\slash", "\b\f\n\r\t\x00\x01\x1f\x7f",
		"line\u2028sep\u2029", "emoji 👩‍💻 ünïcödé", "bad\xffutf8\xe2\x82", "\xc0\xaf", "\xed\xa0\x80 surrogate bytes",
		"nbsp\u00a0and\u0085", "\u200e\u202e bidi", "tab\tthen < > &", "\ufeffbom", "\xf4\x90\x80\x80 too high"}
	pieces := []string{"a", "<", ">", "&", "\"", "\\", "\n", "\x00", "\x1b", "\u2028", "\u2029", "é", "😀", "\xff", "\xe2", "\x80", " ", "\u00ad"}
	for i := 0; i < 200; i++ {
		var b strings.Builder
		for j := r.Intn(12); j >= 0; j-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		strs = append(strs, b.String())
	}
	var sc []stringCase
	for _, s := range strs {
		var nohtml bytes.Buffer
		enc := json.NewEncoder(&nohtml)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		sc = append(sc, stringCase{In: b64(s), HTML: mustMarshal(s), NoHTML: strings.TrimSuffix(nohtml.String(), "\n")})
	}

	values := []string{
		`{}`, `[]`, `null`, `true`, `0`, `-0`, `1.0`, `1e2`, `12345678901234567890`, `1e21`, `0.0000001`,
		`{"b":1,"a":2,"A":3,"aa":{"z":[],"y":{}},"é":"x","_":null}`,
		`{"k":"<html>&</html>","k":"dup wins"}`,
		`[1,"two",[3,[4,[]]],{"five":{"six":[{}]}}]`,
		"{\"bad\":\"\xff\xfe\",\"surr\":\"\\ud800x\\udc00\",\"pair\":\"\\ud83d\\ude00\"}",
		`{"agents":[{"name":"a","archivedAt":"","color":"33","nums":[1.5,2,3e-7]}],"updated":"2026-10-09"}`,
		`  {"spaces" :  [ 1 , 2 ] }  `,
		`{"x":`, `[1,]`, `{"a" 1}`, `"\u2028\u2029"`, `"tab\there"`, `{"":""}`,
		`{"z":1,"y":2,"x":3,"w":4,"v":5,"u":6,"t":7,"10":8,"9":9,"Z":10}`,
		`123.456e-5`, `-1.5e300`, `[0.1,0.2,0.30000000000000004]`,
	}
	var vc []valueCase
	for _, in := range values {
		var v any
		c := valueCase{In: b64(in)}
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			c.Error = err.Error()
		} else {
			c.Marshal = mustMarshal(v)
			c.Indent = mustIndent(v, "", "  ")
			c.Prefix = mustIndent(v, ">", "\t")
			c.Encoder = encodeNoHTML(v)
		}
		vc = append(vc, c)
	}

	one := 1
	when := time.Date(2026, 10, 9, 12, 30, 45, 120000000, time.FixedZone("", 3*3600))
	samples := []Sample{
		{},
		{Name: "a<b>&c", Note: "note", Count: -3, Ratio: 1e21, Small: 0.1, Flag: true, Tags: []string{"x", "y"},
			Empty: []string{}, Labels: map[string]string{"z": "1", "a": "2", "M": "3"}, Scores: map[string]int{"b": 2, "a": 1},
			Blob: []byte("hello\x00world"), Ptr: &one, When: when, Wait: 90 * time.Second,
			Nested: &Inner{Path: "/tmp/x", Deep: []int{}}, Raw: json.RawMessage(` { "k" : [1, 2], "h":"<" } `),
			Anything: map[string]any{"q": []any{1.5, "s", nil, true}}},
		{Name: "\u2028", Tags: nil, Scores: map[string]int{}, Ratio: 3e-7, Small: 1e-7, When: when.UTC(), Nested: &Inner{}, Anything: "x"},
	}
	var stc []structCase
	for i, s := range samples {
		stc = append(stc, structCase{ID: i, Marshal: mustMarshal(s), Indent: mustIndent(s, "", "  "), Encoder: encodeNoHTML(s)})
	}

	lenient := []string{
		`{"name":"a","count":2}`,
		`{"NAME":"upper","Name":"mixed"}`,
		`{"name":"first","NAME":"second"}`,
		`{"inner":{"x":"1"},"INNER":{"Y":2}}`,
		`{"inner":{"x":"1"},"inner":null}`,
		`{"name":null,"count":null,"ptr":null}`,
		`{"ptr":"p","unknown":{"deep":1},"list":[{"X":"a"},{"y":3}]}`,
		`{"map":{"A":"1","a":"2"},"MAP":{"b":"3"}}`,
		`{"list":[{"x":"a"}],"list":[{"x":"b"}]}`,
		"{\"name\":\"bad \xff utf8\"}",
		`{"name":"\ud800"}`,
		`{"count":"notanumber"}`,
		`{"name":`,
		`{"ſ":"long s is not a field","naMe":"fold"}`,
	}
	var lc []lenientCase
	for _, in := range lenient {
		var v Lenient
		c := lenientCase{In: b64(in)}
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			c.Error = true
		} else {
			c.Out = mustMarshal(v)
		}
		lc = append(lc, c)
	}

	return map[string]any{"floats": fc, "strings": sc, "values": vc, "structs": stc, "lenient": lc}
}

// ---- time ----

type durationCase struct {
	Nanos int64  `json:"nanos"`
	Out   string `json:"out"`
}

type parseCase struct {
	In    string `json:"in"`
	Nanos int64  `json:"nanos"`
	Error string `json:"error,omitempty"`
}

type timeCase struct {
	Unix   int64  `json:"unix"`
	Nanos  int64  `json:"nanos"`
	Offset int    `json:"offset"`
	Out    string `json:"out"`
	JSON   string `json:"json"`
}

func timeGolden() any {
	r := rand.New(rand.NewSource(7))
	durations := []int64{0, 1, 999, 1000, 1001, 1500, 999999, 1000000, 1234567, 999999999, 1e9, 1e9 + 1, 59e9, 60e9, 61e9,
		3599e9, 3600e9, 3601e9, 86400e9, -1, -1e9, -90e9, math.MaxInt64, math.MinInt64}
	for i := 0; i < 100; i++ {
		durations = append(durations, r.Int63n(1<<uint(r.Intn(62)+1))*int64(1-2*r.Intn(2)))
	}
	var dc []durationCase
	for _, d := range durations {
		dc = append(dc, durationCase{Nanos: d, Out: time.Duration(d).String()})
	}
	inputs := []string{"0", "", "+0", "-0", "5s", "-5s", "1.5h", ".5s", "5.s", ".s", "1h2m3s4ms5us6ns", "3µs", "3μs",
		"1", "1x", "1hh", "1.0000000000000000000001s", "9223372036854775807ns", "9223372036854775808ns",
		"-9223372036854775808ns", "-9223372036854775809ns", "2562047h47m16.854775807s", "2562047h47m16.854775808s",
		"0.100000000000000000000h", "1e3s", "1.h", "+-1s", "100000000000000000000ns", "1m-1s", " 1s", "1s ", "1µ",
		"0.0000000001s", "1.0000000001s", "15m30.918273645s"}
	var pc []parseCase
	for _, in := range inputs {
		d, err := time.ParseDuration(in)
		c := parseCase{In: in, Nanos: int64(d)}
		if err != nil {
			c.Error = err.Error()
			c.Nanos = 0
		}
		pc = append(pc, c)
	}
	var tc []timeCase
	offsets := []int{0, 3 * 3600, -7 * 3600, 5*3600 + 30*60, -(3*3600 + 45*60)}
	stamps := []time.Time{time.Unix(0, 0), time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 5, 100000000, time.UTC), time.Date(1999, 12, 31, 23, 59, 59, 1, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)}
	for i := 0; i < 20; i++ {
		stamps = append(stamps, time.Unix(r.Int63n(4e9), r.Int63n(1e9)))
	}
	for _, s := range stamps {
		for _, off := range offsets {
			t := s.In(time.FixedZone("", off))
			tc = append(tc, timeCase{Unix: s.Unix(), Nanos: int64(s.Nanosecond()), Offset: off, Out: t.Format(time.RFC3339Nano), JSON: mustMarshal(t)})
		}
	}
	tc = append(tc, timeCase{Unix: time.Time{}.Unix(), Offset: 0, Out: time.Time{}.Format(time.RFC3339Nano), JSON: mustMarshal(time.Time{})})
	return map[string]any{"format": dc, "parse": pc, "times": tc}
}

// ---- messagetext ----

type textCase struct {
	In         string `json:"in_b64"`
	Validate   string `json:"validate"`
	Label      string `json:"label"`
	Sender     string `json:"sender"`
	Neutral    string `json:"neutral"`
	Count      int    `json:"count"`
	Transform  string `json:"transform"`
	Transforms bool   `json:"transforms"`
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func messagetextGolden() any {
	r := rand.New(rand.NewSource(42))
	pieces := []string{"a", "b", " ", "  ", "\n", "\t", "/", "/tmp/x", ".png", ".PNG", ".jpg", ".jpeg", ".gif", ".webp", ".txt",
		"'", "\"", "`", "\\", "C:\\", "c:\\", "kanıt", "İ", "ö", "\u00a0", "\u2028", "\u2029", "\ufeff", "\u3000", "\u200e",
		"\u202e", "\x1b", "\x7f", "\u0085", "\x00", "\xff", "[", "]", "server-main", "unknown", "bilinmiyor", "\r", "😀"}
	corpus := []string{"", " ", "unknown", " unknown ", "bilinmiyor", "server-main", "[x]", "Kanit: /srv/a.png",
		"a.png /b.jpg", "  /x.png  ", "'/x.png'", "\"C:\\x.PNG\"", "see /x.png please", "x\\.png", "\\\\server\\a.gif",
		"/a.png\u3000", "\ufeff/a.png\ufeff", "a\u00a0/b.png", "/x.png\n/y.png", "\n\n/z.gif\n"}
	for i := 0; i < 600; i++ {
		var b strings.Builder
		for j := r.Intn(10); j >= 0; j-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		corpus = append(corpus, b.String())
	}
	var out []textCase
	for _, s := range corpus {
		neutral, count := messagetext.NeutralizeImagePaths(s)
		transform, ok := messagetext.ClaudeImageTransform(s)
		out = append(out, textCase{In: b64(s), Validate: errText(messagetext.Validate(s)), Label: errText(messagetext.Label(s)),
			Sender: errText(messagetext.Sender(s)), Neutral: b64(neutral), Count: count, Transform: b64(transform), Transforms: ok})
	}
	return out
}

// ---- config ----

type configCase struct {
	Name   string         `json:"name"`
	File   string         `json:"file"`
	Body   string         `json:"body"`
	Error  bool           `json:"error"`
	Result map[string]any `json:"result,omitempty"`
}

const goldenUserHome = "/home/golden"

func configGolden() any {
	cases := []struct{ file, body string }{
		{"", ""},
		{"config.yaml", ""},
		{"config.yaml", "# only a comment\n"},
		{"config.yaml", "{}"},
		{"config.yaml", "---\n"},
		{"config.yaml", "~\n"},
		{"config.yaml", string(config.ExampleYAML)},
		{"config.yaml", "updateCheck: false\nlocalMouse: no\nlocalObservation: off\nwaBridge: yes\n"},
		{"config.yaml", "waBridge: \"yes\"\n"},
		{"config.yaml", "waBridge: \"true\"\n"},
		{"config.yaml", "waBridge: True\n"},
		{"config.yaml", "msgqRoot: /abs//q/\nstateDir: rel/./state\nwaOutbox: ~/out\nwaStore: ~\nusageBin: \"\"\nclipboardDir: ../clips\n"},
		{"config.yaml", "agentbooks: [a.json, /b.json, ~/c.json]\n"},
		{"config.yaml", "agentbooks: [a.json]\ntokenAgentbooks: [t.json]\n"},
		{"config.yaml", "tokenAgentbooks: []\n"},
		{"config.yaml", "agentbooks: ~\n"},
		{"config.yaml", "bar:\n  defaultColor: Purple\n  context: remaining\n  widgets: [clock, talk]\n"},
		{"config.yaml", "bar:\n  defaultColor: 300\n"},
		{"config.yaml", "bar:\n  widgets: [ctx, nope]\n"},
		{"config.yaml", "windows: {}\n"},
		{"config.yaml", "windows:\n  resetColor: ' 33 '\n"},
		{"config.yaml", "windows:\n  resetColor: 256\n"},
		{"config.yaml", "lifecycle:\n  ephemeralDefault: false\n"},
		{"config.yaml", "lifecycle: ~\n"},
		{"config.yaml", "codex:\n  sockets: [a.sock, ~/b.sock]\n"},
		{"config.yaml", "codex:\n  disabled: true\n"},
		{"config.yaml", "ntfy:\n  url: https://n.example\n  topic: t\n"},
		{"config.yaml", "ntfy:\n  url: https://n.example\n  bogus: 1\n"},
		{"config.yaml", "cliUpdates:\n  claude: []\n  newcli: [x, y]\n"},
		{"config.yaml", "remotes:\n  server:\n    host: server.example\n    identity: ~/.ssh/id\n"},
		{"config.yaml", "remotes:\n  server:\n    host: h\n    port: 0\n    transport: mosh\n    moshPorts: \"60001\"\n    elevate: sudo -i -u root\n"},
		{"config.yaml", "remotes:\n  'bad name':\n    host: h\n"},
		{"config.yaml", "remotes:\n  s:\n    host: h\n    user: 'a b'\n"},
		{"config.yaml", "remotes:\n  s:\n    host: h\n    moshPorts: 1:2:3\n"},
		{"config.yaml", "remotes:\n  s:\n    host: h\n    port: -1\n"},
		{"config.yaml", "remotes:\n  s: {host: h}\n  s: {host: g}\n"},
		{"config.yaml", "fed:\n  mode: hub\n  listen: 127.0.0.1:7877\n  peerName: hub\n"},
		{"config.yaml", "fed:\n  mode: hub\n  listen: localhost:7877\n  peerName: hub\n"},
		{"config.yaml", "fed:\n  mode: hub\n  listen: '[::1]:7877'\n  peerName: hub\n"},
		{"config.yaml", "fed:\n  mode: hub\n  listen: 127.0.0.1\n  peerName: hub\n"},
		{"config.yaml", "fed:\n  mode: hub\n  listen: 127.0.0.1:1\n  peerName: '..'\n"},
		{"config.yaml", "fed:\n  mode: client\n  hub: http://127.0.0.1:7877\n  peerName: c\n  token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n  expose: [a, b.c]\n"},
		{"config.yaml", "fed:\n  mode: client\n  hub: http://hub.example\n  peerName: c\n  token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"},
		{"config.yaml", "fed:\n  mode: client\n  hub: HTTPS://Hub.Example:8443/path\n  peerName: c\n  token: 0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef\n"},
		{"config.yaml", "fed:\n  mode: client\n  hub: https://hub.example\n  peerName: c\n  token: xyz\n"},
		{"config.yaml", "fed:\n  mode: client\n  hub: ftp://hub.example\n  peerName: c\n  token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"},
		{"config.yaml", "fed:\n  mode: client\n  hub: https://hub.example\n  peerName: c\n  token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n  expose: ['bad name']\n"},
		{"config.yaml", "fed:\n  mode: other\n  peerName: c\n"},
		{"config.yaml", "p2p:\n  enabled: true\n  listen: [/ip4/0.0.0.0/tcp/4001, /ip6/::/udp/4001/quic-v1]\n  advertise: [/dns4/example.com/tcp/443/wss]\n  mdns: true\n"},
		{"config.yaml", "p2p:\n  enabled: true\n  rendezvous: [/dns4/relay.example/tcp/443/wss/p2p/12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5SA]\n"},
		{"config.yaml", "p2p:\n  enabled: true\n  rendezvous: [/dns4/relay.example/tcp/443/wss]\n"},
		{"config.yaml", "p2p:\n  peers:\n    laptop:\n      id: 12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5SA\n      expose: [main]\n      addresses: [/ip4/10.0.0.2/tcp/4001]\n"},
		{"config.yaml", "p2p:\n  peers:\n    laptop:\n      id: QmYyQSo1c1Ym7orWxLYvCrM2EmxFTANf8wXmmE7DWjhx5N\n"},
		{"config.yaml", "p2p:\n  peers:\n    laptop:\n      id: bafzaajaiaejcbzdibmxyzdjbtaxkpi2tmqbndwmfzu4nyuktt7jkvwmkmnfpqvfo\n"},
		{"config.yaml", "p2p:\n  peers:\n    a:\n      id: 12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5SA\n    b:\n      id: 12D3KooWD3eckifWpRn9wQpMG9R9hX3sD158z7EqHWmweQAJU5SA\n"},
		{"config.yaml", "p2p:\n  listen: [/ip4/1.2.3/tcp/1]\n"},
		{"config.yaml", "p2p:\n  listen: [/ip4/1.2.3.4/tcp/65536]\n"},
		{"config.yaml", "p2p:\n  listen: [/unix/tmp/sock, /dns/a.b/tcp/1/http-path/%2Fx, /memory/5, /ip6zone/eth0/ip6/fe80::1/tcp/1]\n"},
		{"config.yaml", "p2p:\n  listen: [ip4/1.2.3.4]\n"},
		{"config.yaml", "p2p:\n  listen: [/nope/1]\n"},
		{"config.yaml", "claudeAccounts:\n  threshold: 50\n  limits: {a: 1, '2': 100}\n  keepAliveModel: ' opus '\n"},
		{"config.yaml", "claudeAccounts:\n  threshold: 50.0\n"},
		{"config.yaml", "claudeAccounts:\n  threshold: 0x20\n"},
		{"config.yaml", "claudeAccounts:\n  threshold: '20'\n"},
		{"config.yaml", "stateDir: [a]\n"},
		{"config.yaml", "- a\n- b\n"},
		{"config.yaml", "just a string\n"},
		{"config.yaml", "stateDir: 123\nmsgqRoot: true\n"},
		{"config.yaml", "a: 1\n"},
		{"config.yaml", "stateDir: x\n...\n"},
		{"config.yml", "stateDir: yml-state\n"},
		{"config.json", `{"stateDir":"json-state","agentbooks":["rel.json"],"bar":{"context":"bogus","widgets":["nope"]}}`},
		{"config.json", `{"STATEDIR":"folded","WaBridge":true,"unknownKey":1}`},
		{"config.json", `{"stateDir":"a","stateDir":"b"}`},
		{"config.json", `{"lifecycle":{"ephemeralDefault":false},"lifecycle":{"archiveOnClose":false}}`},
		{"config.json", `{"waBridge":"yes"}`},
		{"config.json", `{"fed":`},
		{"config.json", `[]`},
		{"config.json", `null`},
		{"config.json", `{"stateDir":null,"bar":null,"windows":{"resetColor":""}}`},
		{"config.json", `{"windows":{"resetColor":"nope"}}`},
		{"config.json", `{"remotes":{"s":{"host":"h","identity":"~/id"}}}`},
		{"config.json", "{\"stateDir\":\"bad\xffutf\"}"},
		{"config.json", `{"claudeAccounts":{"threshold":1.5}}`},
	}
	var out []configCase
	for i, c := range cases {
		home, err := os.MkdirTemp("", "bp-golden-config-")
		if err != nil {
			panic(err)
		}
		if c.file != "" {
			if err := os.WriteFile(filepath.Join(home, c.file), []byte(c.body), 0o600); err != nil {
				panic(err)
			}
		}
		os.Setenv("BP_HOME", home)
		os.Setenv("HOME", goldenUserHome)
		cfg, err := config.Load()
		cc := configCase{Name: fmt.Sprintf("case%02d", i), File: c.file, Body: b64(c.body), Error: err != nil}
		if err == nil {
			cc.Result = describeConfig(cfg, home)
		}
		out = append(out, cc)
		os.RemoveAll(home)
	}
	return out
}

func describeConfig(c config.Config, home string) map[string]any {
	data, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	data = bytes.ReplaceAll(data, []byte(home), []byte("$HOME"))
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	m["Path"] = strings.ReplaceAll(c.Path, home, "$HOME")
	m["Home"] = strings.ReplaceAll(c.Home, home, "$HOME")
	m["Legacy"] = c.Legacy
	m["InvalidConfig"] = c.InvalidConfig != ""
	return m
}

// ---- UpdateRemote ----

type remoteCase struct {
	In     string               `json:"in"`
	Name   string               `json:"name"`
	Remote *config.RemoteConfig `json:"remote"`
	Error  string               `json:"error,omitempty"`
	Out    string               `json:"out"`
	JSON   bool                 `json:"json"`
}

func updateRemoteGolden() any {
	r1 := &config.RemoteConfig{Host: "server.example", Identity: "~/.ssh/id"}
	r2 := &config.RemoteConfig{Host: "h2", Port: 2222, User: "tuna", MoshPorts: "60001", Elevate: "sudo -i", Identity: "/x # y", Transport: "mosh"}
	r3 := &config.RemoteConfig{Host: "10.0.0.1", MoshPorts: "60000:61000", Identity: "~"}
	r4 := &config.RemoteConfig{Host: "h4", Identity: "/keys/it's: here", Elevate: "doas"}
	r5 := &config.RemoteConfig{Host: "true", Identity: "123", User: "yes", Elevate: "sudo -u null"}
	bad := &config.RemoteConfig{Host: "-bad"}
	cases := []struct {
		in     string
		name   string
		remote *config.RemoteConfig
		json   bool
	}{
		{"# keep this comment\nlocalMouse: false\n", "server", r1, false},
		{"# keep this comment\nlocalMouse: false\nremotes:\n  server:\n    host: server.example\n    identity: ~/.ssh/id\n    transport: ssh\n", "server", nil, false},
		{"", "server", r1, false},
		{"# only comment\n", "server", r1, false},
		{"localMouse: false\nremotes:\n  a:\n    host: a\n    transport: ssh\n# trailing\n", "b", r2, false},
		{"localMouse: false\nremotes:\n  a:\n    host: a\n    transport: ssh\n  b:\n    host: b\n    transport: ssh\nupdateCheck: true\n", "a", nil, false},
		{"localMouse: false\nremotes:\n  a:\n    host: a\n    transport: ssh\nupdateCheck: true\n", "a", r2, false},
		{"localMouse: false\nremotes:\n  a:\n    host: a\n    transport: ssh\nupdateCheck: true\n", "b", r3, false},
		{"localMouse: false\nremotes:\n  a:\n    host: a\n    transport: ssh\n  b:\n    host: b\n    transport: ssh\n", "b", r4, false},
		{"bar:\n  context: used # keep\n  widgets: [ctx, temp]\nremotes:\n  # head of a\n  a:\n    host: a # line\n    transport: ssh\n  z:\n    host: z\n    transport: ssh\n", "a", nil, false},
		{"stateDir: state\n", "five", r5, false},
		{"localMouse: false\n", "b", nil, false},
		{"localMouse: false\n", "bad name", r1, false},
		{"localMouse: false\n", "b", bad, false},
		{"- a\n", "b", r1, false},
		{"remotes: [a]\n", "b", r1, false},
		{"remotes:\n  a:\n    host: a\n    transport: ssh\n", "a", nil, false},
		{`{"localMouse":false,"bar":{"widgets":["model"]}}`, "server", r1, true},
		{`{"remotes":{"a":{"host":"a"},"b":{"host":"b","port":22}},"z":1.50,"a":[1, 2]}`, "a", nil, true},
		{`{"remotes":{"a":{"host":"a"}}}`, "a", nil, true},
		{`{"remotes":{"a":{"host":"a"}}}`, "c", nil, true},
		{``, "s", r2, true},
		{`{"html":"<&>"}`, "s", r3, true},
	}
	var out []remoteCase
	for _, c := range cases {
		dir, err := os.MkdirTemp("", "bp-golden-remote-")
		if err != nil {
			panic(err)
		}
		name := "config.yaml"
		if c.json {
			name = "config.json"
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(c.in), 0o600); err != nil {
			panic(err)
		}
		err = config.UpdateRemote(path, c.name, c.remote)
		data, _ := os.ReadFile(path)
		rc := remoteCase{In: c.in, Name: c.name, Remote: c.remote, Out: string(data), JSON: c.json}
		if err != nil {
			rc.Error = err.Error()
		}
		out = append(out, rc)
		os.RemoveAll(dir)
	}
	return out
}
