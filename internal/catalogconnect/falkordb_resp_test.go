package catalogconnect

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
)

func falkorTestBulk(s string) string {
	return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n"
}

func falkorTestArray(frames ...string) string {
	return "*" + strconv.Itoa(len(frames)) + "\r\n" + strings.Join(frames, "")
}

func falkorTestModule(name string, version int, args ...string) string {
	frames := make([]string, 0, len(args))
	for _, arg := range args {
		frames = append(frames, falkorTestBulk(arg))
	}
	return falkorTestArray(
		falkorTestBulk("path"), falkorTestBulk("/test/module.so"),
		falkorTestBulk("ver"), fmt.Sprintf(":%d\r\n", version),
		falkorTestBulk("args"), falkorTestArray(frames...),
		falkorTestBulk("name"), falkorTestBulk(name),
	)
}

func falkorTestHello(modules ...string) string {
	return falkorTestArray(
		falkorTestBulk("modules"), falkorTestArray(modules...),
		falkorTestBulk("id"), ":17\r\n",
		falkorTestBulk("role"), falkorTestBulk("master"),
		falkorTestBulk("proto"), ":2\r\n",
		falkorTestBulk("version"), falkorTestBulk("7.2.4"),
		falkorTestBulk("server"), falkorTestBulk("redis"),
		falkorTestBulk("mode"), falkorTestBulk("standalone"),
	)
}

func falkorTestConfig(n int64) string {
	return falkorTestArray(falkorTestBulk("RESULTSET_SIZE"), fmt.Sprintf(":%d\r\n", n))
}

func TestFalkorRESPFragmentationAndTruncation(t *testing.T) {
	for _, frame := range []string{
		falkorTestHello(falkorTestModule("graph", 40202, "RESULTSET_SIZE", "10000"), falkorTestModule("aux", 7, "")),
		falkorTestConfig(-1),
	} {
		if _, err := newFalkorRESP(iotest.OneByteReader(strings.NewReader(frame))).reply(); err != nil {
			t.Fatal("fragmented frame failed")
		}
		for end := range len(frame) {
			if _, err := newFalkorRESP(strings.NewReader(frame[:end])).reply(); err == nil {
				t.Fatalf("accepted a truncated frame at offset %d", end)
			}
		}
	}
}

func TestFalkorRESPRejectsMalformedAndOversizedFrames(t *testing.T) {
	fullBulk := falkorTestBulk(strings.Repeat("b", 8192))
	cases := map[string]string{
		"bare LF": ":1\n", "embedded CR": ":1\r2\r\n", "bad bulk end": "$1\r\na\n\r", "invalid integer": ":q\r\n",
		"null bulk": "$-1\r\n", "null array": "*-1\r\n", "negative bulk": "$-2\r\n", "negative array": "*-2\r\n",
		"leading zero": "$01\r\na\r\n", "plus sign": ":+1\r\n", "negative zero": ":-0\r\n", "whitespace": ": 1\r\n",
		"overflow integer": ":9223372036854775808\r\n", "overflow length": "$9223372036854775808\r\n", "overflow count": "*9223372036854775808\r\n",
		"bulk length": "$8193\r\n", "array count": "*257\r\n", "framing": ":" + strings.Repeat("1", 129) + "\r\n",
		"depth": strings.Repeat("*1\r\n", 5) + ":1\r\n", "aggregate bytes": "*8\r\n" + strings.Repeat(fullBulk, 8),
		"aggregate nodes": "*8\r\n" + strings.Repeat("*256\r\n"+strings.Repeat(":0\r\n", 256), 8),
		"error payload":   "-ERR " + strings.Repeat("x", 1024) + "\r\n",
		"nested error":    "*1\r\n-NOPERM hostile detail\r\n", "surplus": ":1\r\n:2\r\n",
		"simple string": "+OK\r\n", "RESP3 map": "%0\r\n", "RESP3 push": ">0\r\n", "RESP3 null": "_\r\n",
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newFalkorRESP(strings.NewReader(frame)).reply(); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
	}
	for _, frame := range []string{
		fullBulk, ":9223372036854775807\r\n", ":-9223372036854775808\r\n",
		"*256\r\n" + strings.Repeat(":0\r\n", 256), strings.Repeat("*1\r\n", 4) + ":1\r\n",
	} {
		if _, err := newFalkorRESP(strings.NewReader(frame)).reply(); err != nil {
			t.Fatal("valid boundary rejected")
		}
	}
}

func TestFalkorRESPBudgetsSpanReplies(t *testing.T) {
	// Each frame fits on its own. Taken together they exceed the session budget.
	for _, frame := range []string{
		"*5\r\n" + strings.Repeat(falkorTestBulk(strings.Repeat("b", 8192)), 5),
		"*5\r\n" + strings.Repeat("*256\r\n"+strings.Repeat(":0\r\n", 256), 5),
	} {
		r := newFalkorRESP(io.MultiReader(strings.NewReader(frame), strings.NewReader(frame)))
		if _, err := r.reply(); err != nil {
			t.Fatal("first reply did not fit")
		}
		if _, err := r.reply(); err == nil {
			t.Fatal("reply reset a session budget")
		}
	}
}

func TestFalkorHELLOAndConfigSchemas(t *testing.T) {
	valid := falkorTestHello(falkorTestModule("graph", 40202), falkorTestModule("other", 77, "private arg"))
	for _, frame := range []string{valid, strings.Replace(valid, falkorTestBulk("master"), falkorTestBulk("replica"), 1)} {
		value, err := newFalkorRESP(strings.NewReader(frame)).reply()
		if err != nil || validateFalkorHello(value) != nil {
			t.Fatal("valid HELLO rejected")
		}
	}
	tooManyArgs := make([]string, 65)
	tooManyModules := make([]string, 33)
	for i := range tooManyModules {
		tooManyModules[i] = falkorTestModule("module"+strconv.Itoa(i), 42)
	}
	tooManyModules[0] = falkorTestModule("graph", 40202)
	cases := map[string]string{
		"plain Redis": falkorTestHello(), "older graph": falkorTestHello(falkorTestModule("graph", 21200)),
		"future graph": falkorTestHello(falkorTestModule("graph", 40203)), "wrong module name": falkorTestHello(falkorTestModule("falkordb", 40202)),
		"duplicate graph":            falkorTestHello(falkorTestModule("graph", 40202), falkorTestModule("graph", 40202)),
		"duplicate unrelated module": falkorTestHello(falkorTestModule("graph", 40202), falkorTestModule("aux", 1), falkorTestModule("aux", 2)),
		"malformed extra module":     falkorTestHello(falkorTestModule("graph", 40202), falkorTestArray(falkorTestBulk("path"))),
		"excess args":                falkorTestHello(falkorTestModule("graph", 40202, tooManyArgs...)),
		"excess modules":             falkorTestHello(tooManyModules...), "wrong arity": strings.Replace(valid, "*14", "*13", 1),
		"wrong module version type": strings.Replace(valid, ":40202\r\n", falkorTestBulk("40202"), 1),
		"duplicate key":             strings.Replace(valid, falkorTestBulk("server"), falkorTestBulk("mode"), 1),
		"unknown key":               strings.Replace(valid, falkorTestBulk("server"), falkorTestBulk("unrecognized"), 1),
		"string proto":              strings.Replace(valid, ":2\r\n", falkorTestBulk("2"), 1),
		"wrong proto":               strings.Replace(valid, ":2\r\n", ":3\r\n", 1),
		"zero connection ID":        strings.Replace(valid, ":17\r\n", ":0\r\n", 1),
		"sentinel":                  strings.Replace(valid, falkorTestBulk("standalone"), falkorTestBulk("sentinel"), 1),
		"cluster":                   strings.Replace(valid, falkorTestBulk("standalone"), falkorTestBulk("cluster"), 1),
		"wrong role":                strings.Replace(valid, falkorTestBulk("master"), falkorTestBulk("leader"), 1),
		"bad core version":          strings.Replace(valid, falkorTestBulk("7.2.4"), falkorTestBulk("not a version"), 1),
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			value, err := newFalkorRESP(strings.NewReader(frame)).reply()
			if err == nil && validateFalkorHello(value) == nil {
				t.Fatal("invalid HELLO accepted")
			}
		})
	}

	for _, n := range []int64{10000, 73, 0, -1, math.MinInt64, math.MaxInt64} {
		value, err := newFalkorRESP(strings.NewReader(falkorTestConfig(n))).reply()
		if err != nil || !validFalkorConfig(value) {
			t.Fatal("canonical config value rejected")
		}
	}
	for _, frame := range []string{
		falkorTestArray(falkorTestBulk("TIMEOUT"), ":73\r\n"),
		falkorTestArray(falkorTestBulk("RESULTSET_SIZE"), falkorTestBulk("73")),
		falkorTestArray(falkorTestBulk("RESULTSET_SIZE"), falkorTestArray(":73\r\n")),
		falkorTestArray(falkorTestBulk("RESULTSET_SIZE"), ":73\r\n", ":0\r\n"),
	} {
		value, err := newFalkorRESP(strings.NewReader(frame)).reply()
		if err == nil && validFalkorConfig(value) {
			t.Fatal("invalid config accepted")
		}
	}
}

type falkorShortWriter struct {
	bytes.Buffer
	step int
}

func (w *falkorShortWriter) Write(p []byte) (int, error) {
	return w.Buffer.Write(p[:min(w.step, len(p))])
}

func TestFalkorRESPWriterHandlesShortWritesAndPreservesArguments(t *testing.T) {
	args := []string{"HELLO", "2", "AUTH", "test:fixture", "a *2 $4 :1"}
	want := []byte(falkorTestArray(falkorTestBulk(args[0]), falkorTestBulk(args[1]), falkorTestBulk(args[2]), falkorTestBulk(args[3]), falkorTestBulk(args[4])))
	w := &falkorShortWriter{step: 3}
	if writeFalkorCommand(w, args...) != nil || !slices.Equal(w.Bytes(), want) {
		t.Fatal("encoded command differs from fixed RESP bulk arguments")
	}
	if writeFalkorCommand(&falkorShortWriter{}, args...) == nil {
		t.Fatal("non-progressing writer accepted")
	}
	var tooLong bytes.Buffer
	if writeFalkorCommand(&tooLong, "HELLO", strings.Repeat("b", 8192)) == nil || tooLong.Len() != 0 {
		t.Fatal("oversized write accepted")
	}
}

func FuzzFalkorRESP(f *testing.F) {
	f.Add([]byte(falkorTestHello(falkorTestModule("graph", 40202, "RESULTSET_SIZE", "10000"))))
	f.Add([]byte(falkorTestConfig(-1)))
	f.Add([]byte("*1\r\n*1\r\n*1\r\n*1\r\n*1\r\n:0\r\n"))
	f.Add([]byte("-WRONGPASS hostile text\r\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		r := bytes.NewReader(input)
		value, err := newFalkorRESP(r).reply()
		if len(input)-r.Len() > 64*1024 {
			t.Fatal("decoder read past its session budget")
		}
		if err == nil {
			_ = validateFalkorHello(value)
			_ = validFalkorConfig(value)
		}
	})
}
