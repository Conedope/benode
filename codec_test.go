package benode

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func mustEncode(t *testing.T, v Value) []byte {
	t.Helper()
	out := Encode(v)
	if err := EncodeString(v, &bytes.Buffer{}); err != nil {
		t.Fatalf("EncodeString failed: %v", err)
	}
	return out
}

func TestEncodeGoldens(t *testing.T) {
	cases := []struct {
		name string
		v    Value
		want string
	}{
		{"int positive", Int(42), "i42e"},
		{"int negative", Int(-1), "i-1e"},
		{"int zero", Int(0), "i0e"},
		{"int min", Int(-9223372036854775807 - 1), "i-9223372036854775808e"},
		{"int max", Int(9223372036854775807), "i9223372036854775807e"},
		{"string abc", Bytes("abc"), "3:abc"},
		{"empty string", Bytes(""), "0:"},
		{"string binary", Bytes{0x00, 0xff, 0x10}, "3:\x00\xff\x10"},
		{"list of two", List{Bytes("abc"), Int(-1)}, "l3:abci-1ee"},
		{"empty list", List{}, "le"},
		{"nested list", List{List{Int(1)}, List{}}, "lli1eelee"},
		{"dict single", Dict{"a": Int(42)}, "d1:ai42ee"},
		{"dict two keys", Dict{"a": Int(42), "b": Bytes("cd")}, "d1:ai42e1:b2:cde"},
		{"empty dict", Dict{}, "de"},
		{"dict with list", Dict{"a": List{Int(1)}, "b": Dict{}}, "d1:ali1ee1:bdee"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustEncode(t, tc.v); string(got) != tc.want {
				t.Fatalf("Encode(%v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

func TestEncodeSortedKeys(t *testing.T) {
	d := Dict{"c": Int(1), "a": Int(2), "b": Int(3)}
	got := string(mustEncode(t, d))
	want := "d1:ai2e1:bi3e1:ci1ee"
	if got != want {
		t.Fatalf("got %q, want %q (keys must be bytewise ascending)", got, want)
	}
	// Bytewise, not alphabetic: uppercase 'Z' sorts before lowercase 'a'.
	d2 := Dict{"a": Int(1), "Z": Int(2)}
	if got := string(mustEncode(t, d2)); got != "d1:Zi2e1:ai1ee" {
		t.Fatalf("bytewise sort violation: got %q", got)
	}
	// Non-UTF8 keys sort by their raw bytes.
	d3 := Dict{"\xff": Int(1), "\x00": Int(2)}
	if got := string(mustEncode(t, d3)); got != "d1:\x00i2e1:\xffi1ee" {
		t.Fatalf("bytewise sort violation: got %q", got)
	}
}

func TestEncodeStringWriter(t *testing.T) {
	var b bytes.Buffer
	if err := EncodeString(Dict{"x": List{Int(7)}}, &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "d1:xli7eee" {
		t.Fatalf("got %q", b.String())
	}
}

func TestDecodeGolden(t *testing.T) {
	cases := []struct {
		in   string
		want Value
	}{
		{"i42e", Int(42)},
		{"i-1e", Int(-1)},
		{"i0e", Int(0)},
		{"i9223372036854775807e", Int(9223372036854775807)},
		{"i-9223372036854775808e", Int(-9223372036854775807 - 1)},
		{"3:abc", Bytes("abc")},
		{"0:", Bytes("")},
		{"le", List{}},
		{"de", Dict{}},
		{"l3:abci-1ee", List{Bytes("abc"), Int(-1)}},
		{"d1:ai42e1:b3:cdde", Dict{"a": Int(42), "b": Bytes("cdd")}},
		{"d1:ai42e1:ali1ei2ee1:bdee", Dict{"a": List{Int(1), Int(2)}, "b": Dict{}}},
	}
	for _, tc := range cases {
		v, n, err := Decode([]byte(tc.in))
		if err != nil {
			t.Fatalf("Decode(%q): %v", tc.in, err)
		}
		if n != len(tc.in) {
			t.Errorf("Decode(%q) consumed %d, want %d", tc.in, n, len(tc.in))
		}
		if !equal(v, tc.want) {
			t.Errorf("Decode(%q) = %#v, want %#v", tc.in, v, tc.want)
		}
	}
}

func TestDecodePositions(t *testing.T) {
	// "3:abc" starts at offset 0, so a following i1e starts at offset 5.
	v, n, err := Decode([]byte("3:abc"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || !equal(v, Bytes("abc")) {
		t.Fatalf("n=%d v=%#v", n, v)
	}

	// Plausible input spans: decode a value embedded at a known offset.
	data := []byte("XXd1:ai2ee")
	sub := data[2:]
	v, n, err = Decode(sub)
	if err != nil || n != len(sub) {
		t.Fatalf("Decode at offset 2: n=%d err=%v", n, err)
	}
	if !equal(v, Dict{"a": Int(2)}) {
		t.Fatalf("embedded dict = %#v", v)
	}
	// ...and report the absolute position of a container's children.
	second, n2, err := Decode([]byte("d1:ai2eefoo"))
	if err == nil {
		t.Fatalf("expected trailing error, got %#v n=%d", second, n2)
	}
	var te *TrailingError
	if !errors.As(err, &te) || te.Offset != 8 {
		t.Fatalf("TrailingError offset = %v, want 8", err)
	}

	// Nested: dict "d" + key 1:a + value = list 3:abc, then i-1e
	in := "d1:al3:abci-1eee"
	v, n, err = Decode([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	d := v.(Dict)
	if !equal(d["a"], List{Bytes("abc"), Int(-1)}) {
		t.Fatalf("nested list mismatch: %#v", d["a"])
	}
	if n != len(in) {
		t.Fatalf("consumed %d, want %d", n, len(in))
	}
}

func equal(a, b Value) bool {
	switch av := a.(type) {
	case Int:
		bi, ok := b.(Int)
		return ok && av == bi
	case Bytes:
		bb, ok := b.(Bytes)
		return ok && bytes.Equal(av, bb)
	case List:
		bl, ok := b.(List)
		if !ok || len(av) != len(bl) {
			return false
		}
		for i := range av {
			if !equal(av[i], bl[i]) {
				return false
			}
		}
		return true
	case Dict:
		bd, ok := b.(Dict)
		if !ok || len(av) != len(bd) {
			return false
		}
		for k, vv := range av {
			if !equal(vv, bd[k]) {
				return false
			}
		}
		return true
	}
	return false
}

func TestDecodeAllAdjacent(t *testing.T) {
	vs, err := DecodeAll([]byte("i1ei-1e3:abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Value{Int(1), Int(-1), Bytes("abc")}
	for i := range want {
		if !equal(vs[i], want[i]) {
			t.Fatalf("value %d = %#v, want %#v", i, vs[i], want[i])
		}
	}

	// Single value is fine.
	if vs, err := DecodeAll([]byte("li1ee")); err != nil || len(vs) != 1 {
		t.Fatalf("single value: vs=%v err=%v", vs, err)
	}

	// Leading trash is never skipped.
	if _, err := DecodeAll([]byte("garbagei1e")); err == nil {
		t.Fatal("expected error on leading garbage")
	} else {
		var se *SyntaxError
		if !errors.As(err, &se) || se.Offset != 0 {
			t.Fatalf("want SyntaxError at offset 0, got %#v", err)
		}
	}

	// Empty input is an error.
	if _, err := DecodeAll(nil); err == nil {
		t.Fatal("expected error on empty input")
	}
}

func syntaxOff(t *testing.T, err error, want int) {
	t.Helper()
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("want *SyntaxError, got %v", err)
	}
	if se.Offset != want {
		t.Fatalf("offset = %d, want %d (%v)", se.Offset, want, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		off  int
	}{
		{"integer overflow", "i9223372036854775808e", 0},
		{"integer underflow", "i-9223372036854775809e", 0},
		{"huge integer", "i99999999999999999999999999e", 0},
		{"empty integer", "ie", 0},
		{"dangling minus", "i-e", 0},
		{"leading zero", "i01e", 0},
		{"negative zero", "i-0e", 0},
		{"string length overrun", "5:abc", 0},
		{"string length overrun mid", "l4:abc", 1},
		{"string missing colon", "4xx", 0},
		{"unterminated string length", "4", 0},
		{"unterminated integer", "i42", 0},
		{"garbage token", "x", 0},
		{"unterminated list", "l3:abc", 0},
		{"unterminated dict", "d1:ai42e", 0},
		{"dict missing value", "d1:ae", 4},
		{"list garbage inside", "lxe", 1},
		{"negative string length invalid", "-1:a", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Decode([]byte(tc.in))
			if err == nil {
				t.Fatalf("Decode(%q): expected error", tc.in)
			}
			if !errors.Is(err, ErrSyntax) {
				t.Fatalf("Decode(%q): error does not wrap ErrSyntax: %v", tc.in, err)
			}
			syntaxOff(t, err, tc.off)
		})
	}
}

func TestDecodeTrailing(t *testing.T) {
	v, n, err := Decode([]byte("i1ex"))
	if err == nil {
		t.Fatal("expected ErrTrailing")
	}
	if !errors.Is(err, ErrTrailing) {
		t.Fatalf("want ErrTrailing, got %v", err)
	}
	if !equal(v, Int(1)) {
		t.Fatalf("value should still be returned: %#v", v)
	}
	if n != 3 {
		t.Fatalf("consumed %d, want 3", n)
	}
	var te *TrailingError
	if !errors.As(err, &te) || te.Offset != 3 {
		t.Fatalf("want TrailingError at offset 3, got %v", err)
	}
}

func TestDecodeDuplicateKeyLastWins(t *testing.T) {
	in := "d1:ai1e1:ai2e1:b3:cdde"
	v, _, err := Decode([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	d := v.(Dict)
	if !equal(d["a"], Int(2)) {
		t.Fatalf("duplicate key: a = %#v, want Int(2) (LAST wins)", d["a"])
	}
	if !equal(d["b"], Bytes("cdd")) {
		t.Fatalf("b = %#v, want Bytes(\"cdd\")", d["b"])
	}
	// Re-encoding collapses the duplicates deterministically.
	re := Encode(d)
	if string(re) != "d1:ai2e1:b3:cdde" {
		t.Fatalf("re-encode = %q", re)
	}
}

func TestRoundTripCorpus(t *testing.T) {
	samples := []Value{
		Int(0),
		Int(1),
		Int(-1),
		Int(9223372036854775807),
		Int(-9223372036854775807 - 1),
		Bytes(""),
		Bytes("hello"),
		Bytes("héllo wörld — ☃"),
		Bytes("\x00\x01\x02"),
		Bytes{0xff, 0xfe, 0xfd},
		List{},
		List{Int(1), Int(2), Int(3)},
		Dict{},
		Dict{"": Int(1)},
		Dict{"a": Bytes("\x00binary"), "b": List{Dict{"n": Int(42)}}},
		// 4-level nesting
		List{
			List{
				List{
					List{Int(7), Bytes("deep")},
					Dict{"k": Bytes("v"), "pieces": Bytes(strings.Repeat("x", 20))},
				},
			},
		},
		// keys with non-UTF8 bytes
		Dict{"\xff\xfe": Int(1), "a": Bytes("z"), "\x00": Int(2)},
		// deeply mixed chest
		Dict{
			"info": Dict{
				"name":         Bytes("sample"),
				"piece length": Int(32768),
				"pieces":       Bytes(strings.Repeat("0123456789abcdefghij", 3)),
				"private":      Int(1),
			},
			"announce": Bytes("http://t.example/a"),
		},
	}
	for i, v := range samples {
		data := Encode(v)
		got, n, err := Decode(data)
		if err != nil {
			t.Fatalf("sample %d: decode: %v", i, err)
		}
		if n != len(data) {
			t.Fatalf("sample %d: consumed %d of %d", i, n, len(data))
		}
		if !equal(got, v) {
			t.Fatalf("sample %d: round-trip mismatch:\n got %#v\nwant %#v", i, got, v)
		}
		if re := Encode(got); !bytes.Equal(re, data) {
			t.Fatalf("sample %d: double-encode not stable:\n %q\n %q", i, re, data)
		}
	}
}

func TestPretty(t *testing.T) {
	cases := []struct {
		v    Value
		want string
	}{
		{Int(42), "42"},
		{Int(-1), "-1"},
		{Bytes("abc"), "3:abc"},
		{Bytes(""), "0:"},
		{List{Int(1)}, "l1e"},
		{List{Bytes("abc"), Int(-1)}, "l3:abc-1e"},
		{Dict{"a": Bytes("bcd"), "d": Bytes("e")}, "d1:a3:bcd1:d1:ee"},
		{Dict{"a": Int(42)}, "d1:a42e"},
		{Dict{"b": List{Int(1)}, "a": Dict{}}, "d1:ade1:bl1ee"},
		{Bytes{0x01}, "{1 bytes}"},
	}
	for _, tc := range cases {
		if got := Pretty(tc.v); got != tc.want {
			t.Errorf("Pretty(%#v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}
