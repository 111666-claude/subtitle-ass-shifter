package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
)

func resetExtractGlobals(t *testing.T) {
	t.Helper()
	fromStr, toStr, fromRegexp, toRegexp = "", "", "", ""
	gFrom, gTo = 0, 0
	shift = 0
	extractMode, clipCues, rebaseCues = false, false, false
	outputPath, inputPath, dry = "", "", false
	encOverr = ""
	inputEncoding, inputEncName = nil, ""
	fileUpdated = 0
	t.Cleanup(func() {
		fromStr, toStr, fromRegexp, toRegexp = "", "", "", ""
		gFrom, gTo = 0, 0
		shift = 0
		extractMode, clipCues, rebaseCues = false, false, false
		outputPath, inputPath, dry = "", "", false
		encOverr = ""
		inputEncoding, inputEncName = nil, ""
		fileUpdated = 0
	})
}

const assSample = `[Script Info]
Title: Demo
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize
Style: Default,Arial,20

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Comment: 0,0:09:00.00,0:09:10.00,Default,,0,0,0,,note before range
Dialogue: 0,0:11:59.00,0:12:30.00,Default,,0,0,0,,crosses start, with comma
Dialogue: 0,0:13:00.00,0:14:00.00,Default,,0,0,0,,inside,{\pos(1,2)}
Dialogue: 0,0:20:00.00,0:21:00.00,Default,,0,0,0,,inside two
Dialogue: 0,0:27:00.00,0:28:30.00,Default,,0,0,0,,crosses both
Dialogue: 0,0:27:30.00,0:28:00.00,Default,,0,0,0,,touches end
Dialogue: 0,0:28:00.00,0:29:00.00,Default,,0,0,0,,after end
Dialogue: 0,0:12:00.00,0:12:00.00,Default,,0,0,0,,zero length at start
`

const srtSample = `1
00:11:59,000 --> 00:12:30,000 X1:10 X2:20
cross start

2
00:13:00,000 --> 00:14:00,000
inside one
second line

3
00:20:00,000 --> 00:21:00,000
inside two

4
00:27:00,000 --> 00:28:30,000
crosses both

5
00:27:30,000 --> 00:28:00,000
touches end

6
00:28:00,000 --> 00:29:00,000
after end

7
00:10:00,000 --> 00:11:00,000
before range
`

func TestExtractASSKeepsStylesAndText(t *testing.T) {
	out, count, err := extractASS(assSample, 12*60*1000, 28*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}

	for _, want := range []string{
		"[Script Info]", "[V4+ Styles]", "Style: Default,Arial,20",
		"Format: Layer, Start, End", "Comment: 0,0:09:00.00,0:09:10.00,Default,,0,0,0,,note before range",
		"Dialogue: 0,0:11:59.00,0:12:30.00,Default,,0,0,0,,crosses start, with comma",
		"Dialogue: 0,0:13:00.00,0:14:00.00,Default,,0,0,0,,inside,{\\pos(1,2)}",
		"Dialogue: 0,0:20:00.00,0:21:00.00,Default,,0,0,0,,inside two",
		"Dialogue: 0,0:27:00.00,0:28:30.00,Default,,0,0,0,,crosses both",
		"Dialogue: 0,0:27:30.00,0:28:00.00,Default,,0,0,0,,touches end",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	for _, unwanted := range []string{
		"after end", "zero length at start",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output should not contain %q\n%s", unwanted, out)
		}
	}

	if !strings.HasSuffix(out, "\n") {
		t.Error("output lost its trailing newline")
	}
}

func TestExtractASSClipRebase(t *testing.T) {
	out, count, err := extractASS(assSample, 12*60*1000, 28*60*1000, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}

	for _, want := range []string{
		"Dialogue: 0,0:00:00.00,0:00:30.00,Default,,0,0,0,,crosses start, with comma",
		"Dialogue: 0,0:08:00.00,0:09:00.00,Default,,0,0,0,,inside two",
		"Dialogue: 0,0:01:00.00,0:02:00.00,Default,,0,0,0,,inside,{\\pos(1,2)}",
		"Dialogue: 0,0:15:00.00,0:16:00.00,Default,,0,0,0,,crosses both",
		"Dialogue: 0,0:15:30.00,0:16:00.00,Default,,0,0,0,,touches end",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Comment: 0,0:09:00.00") == false {
		t.Error("non-dialogue lines must be preserved verbatim under clip/rebase")
	}
}

func TestExtractRebaseWithoutClipClampsLeadingOverhang(t *testing.T) {
	// --rebase alone shifts the timestamps but does not trim the tail. A cue
	// that starts before the range has its start clamped to zero rather than
	// emitting a negative timestamp.
	out, _, err := extractASS(assSample, 12*60*1000, 28*60*1000, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Dialogue: 0,0:00:00.00,0:00:30.00,Default,,0,0,0,,crosses start, with comma") {
		t.Fatalf("expected clamped rebased start\n%s", out)
	}
	if !strings.Contains(out, "Dialogue: 0,0:15:00.00,0:16:30.00,Default,,0,0,0,,crosses both") {
		t.Fatalf("expected un-clipped tail\n%s", out)
	}
}

func TestExtractSRTClipRebase(t *testing.T) {
	out, count, err := extractSRT(srtSample, 12*60*1000, 28*60*1000, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}

	want := `1
00:00:00,000 --> 00:00:30,000 X1:10 X2:20
cross start

2
00:01:00,000 --> 00:02:00,000
inside one
second line

3
00:08:00,000 --> 00:09:00,000
inside two

4
00:15:00,000 --> 00:16:00,000
crosses both

5
00:15:30,000 --> 00:16:00,000
touches end
`
	if out != want {
		t.Fatalf("unexpected SRT output\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestExtractSRTKeepsOriginalTimes(t *testing.T) {
	out, count, err := extractSRT(srtSample, 12*60*1000, 28*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}

	want := `1
00:11:59,000 --> 00:12:30,000 X1:10 X2:20
cross start

2
00:13:00,000 --> 00:14:00,000
inside one
second line

3
00:20:00,000 --> 00:21:00,000
inside two

4
00:27:00,000 --> 00:28:30,000
crosses both

5
00:27:30,000 --> 00:28:00,000
touches end
`
	if out != want {
		t.Fatalf("unexpected SRT output\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestExtractBoundaryTangency(t *testing.T) {
	tests := []struct {
		name           string
		cueStart, cueE int
		start, end     int
		want           bool
	}{
		{"ends at start", 0, 1000, 1000, 2000, false},
		{"starts at end", 2000, 3000, 1000, 2000, false},
		{"exactly the range", 1000, 2000, 1000, 2000, true},
		{"inside", 1200, 1800, 1000, 2000, true},
		{"spans both", 0, 3000, 1000, 2000, true},
		{"ends at end", 1500, 2000, 1000, 2000, true},
		{"zero length inside", 1500, 1500, 1000, 2000, false},
		{"zero length at start", 1000, 1000, 1000, 2000, false},
		{"overlaps end", 1500, 2500, 1000, 2000, true},
	}
	for _, tt := range tests {
		if got := cueInRange(tt.cueStart, tt.cueE, tt.start, tt.end); got != tt.want {
			t.Errorf("%s: cueInRange(%d,%d,%d,%d) = %v, want %v",
				tt.name, tt.cueStart, tt.cueE, tt.start, tt.end, got, tt.want)
		}
	}
}

func TestExtractNoHits(t *testing.T) {
	out, count, err := extractSRT(srtSample, 60*60*1000, 61*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || out != "" {
		t.Fatalf("SRT no-hit: count=%d out=%q", count, out)
	}

	out, count, err = extractASS(assSample, 60*60*1000, 61*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ASS no-hit count = %d", count)
	}
	if strings.Contains(out, "Dialogue:") {
		t.Fatalf("ASS no-hit should drop all Dialogue lines\n%s", out)
	}
	if !strings.Contains(out, "[V4+ Styles]") || !strings.Contains(out, "Comment:") {
		t.Fatalf("ASS no-hit should keep headers/styles/non-dialogue lines\n%s", out)
	}
}

func TestExtractCRLFAndBOM(t *testing.T) {
	crlf := strings.ReplaceAll(srtSample, "\n", "\r\n")
	out, _, err := extractSRT(crlf, 12*60*1000, 28*60*1000, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Fatal("output mixed line endings")
	}
	if !strings.HasSuffix(out, "\r\n") {
		t.Fatalf("output should keep CRLF and trailing newline: %q", out)
	}
}

func TestParseExtractRangeErrors(t *testing.T) {
	tests := []struct {
		name         string
		from, to     string
		fromRE, toRE string
		wantErr      string
	}{
		{"missing end", "0:12:00", "", "", "", "requires both --start and --end"},
		{"missing start", "", "0:28:00", "", "", "requires both --start and --end"},
		{"empty range", "0:12:00", "0:12:00", "", "", "empty range"},
		{"empty range with fractions", "0:12:00.000", "0:12:00.000", "", "", "empty range"},
		{"reversed", "0:28:00", "0:12:00", "", "", "greater than --start"},
		{"negative start", "-0:12:00", "0:28:00", "", "", "must not be negative"},
		{"negative end", "0:12:00", "-0:28:00", "", "", "must not be negative"},
		{"bad start", "12:00", "0:28:00", "", "", "invalid --start"},
		{"bad seconds", "0:12:60", "0:28:00", "", "", "invalid --start"},
		{"bad end", "0:12:00", "0:28", "", "", "invalid --end"},
		{"garbage", "abc", "0:28:00", "", "", "invalid --start"},
		{"regex conflict", "0:12:00", "0:28:00", "foo", "", "cannot be used with --extract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetExtractGlobals(t)
			fromStr, toStr = tt.from, tt.to
			fromRegexp, toRegexp = tt.fromRE, tt.toRE
			_, _, err := parseExtractRange()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseExtractRangeAccepts(t *testing.T) {
	tests := []struct {
		from, to string
		wantS    int
		wantE    int
	}{
		{"0:12:00", "0:28:00", 12 * 60 * 1000, 28 * 60 * 1000},
		{"00:12:00.500", "0:28:00.05", 12*60*1000 + 500, 28*60*1000 + 50},
		{"1:02:03", "2:03:04", 3723000, 7384000},
	}
	for _, tt := range tests {
		resetExtractGlobals(t)
		fromStr, toStr = tt.from, tt.to
		s, e, err := parseExtractRange()
		if err != nil {
			t.Fatalf("%s/%s: %v", tt.from, tt.to, err)
		}
		if s != tt.wantS || e != tt.wantE {
			t.Fatalf("%s/%s: got %d/%d, want %d/%d", tt.from, tt.to, s, e, tt.wantS, tt.wantE)
		}
	}
}

func TestExtractBadCueTime(t *testing.T) {
	_, _, err := extractSRT("1\n00:0x:01,000 --> 00:00:02,000\nA\n", 0, 1000, false, false)
	if err == nil || !strings.Contains(err.Error(), "invalid start time") {
		t.Fatalf("SRT error = %v", err)
	}

	_, _, err = extractASS("[Events]\nDialogue: 0,0:00:01.00,0:00:0x.00,D,,0,0,0,,A\n", 0, 1000, false, false)
	if err == nil || !strings.Contains(err.Error(), "invalid end time") {
		t.Fatalf("ASS error = %v", err)
	}

	_, _, err = extractASS("[Events]\nDialogue: 0,0:00:02.00,0:00:01.00,D,,0,0,0,,A\n", 0, 5000, false, false)
	if err == nil || !strings.Contains(err.Error(), "before start time") {
		t.Fatalf("ASS reversed error = %v", err)
	}
}

func TestExtractEncodingPreservedGBK(t *testing.T) {
	gbk, name := charset.Lookup("gbk")
	if gbk == nil {
		t.Skip("gbk not available")
	}
	utf8Source := srtSample + "\n8\n00:12:00,000 --> 00:13:00,000\n中文字幕\n"
	g, _, err := transform.Bytes(gbk.NewEncoder(), []byte(utf8Source))
	if err != nil {
		t.Fatal(err)
	}

	resetExtractGlobals(t)
	inputEncoding, inputEncName = gbk, name
	out, count, _, _, err := extractFileData(g, ".srt", 12*60*1000, 28*60*1000, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("count = %d, want 6", count)
	}
	decoded, _, err := transform.Bytes(gbk.NewDecoder(), out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "中文字幕") {
		t.Fatalf("Chinese text lost\n%s", decoded)
	}
	if bytes.Contains(out, []byte("中")) {
		t.Fatal("output is not GBK encoded")
	}
}

func TestExtractEncodingPreservesBOM(t *testing.T) {
	src := []byte("\ufeff" + srtSample)
	resetExtractGlobals(t)
	out, _, encName, _, err := extractFileData(src, ".srt", 12*60*1000, 28*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if encName != "utf-8" {
		t.Fatalf("encName = %q", encName)
	}
	if !bytes.HasPrefix(out, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatalf("BOM lost, output starts with % x", out[:3])
	}
	if bytes.Count(out, []byte{0xef, 0xbb, 0xbf}) != 1 {
		t.Fatal("BOM duplicated")
	}
}

func TestResolveOutputTarget(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.srt")
	if err := os.WriteFile(input, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, toStdout, err := resolveOutputTarget(input, ""); err != nil || !toStdout {
		t.Fatalf("empty output: toStdout=%v err=%v", toStdout, err)
	}
	if _, toStdout, err := resolveOutputTarget(input, "-"); err != nil || !toStdout {
		t.Fatalf("- output: toStdout=%v err=%v", toStdout, err)
	}

	if _, _, err := resolveOutputTarget(input, input); err == nil {
		t.Fatal("expected refusal for identical output path")
	}
	if _, _, err := resolveOutputTarget(input, dir); err == nil {
		t.Fatal("expected refusal when output dir would place the file over the input")
	}

	target, _, err := resolveOutputTarget(input, filepath.Join(dir, "out.srt"))
	if err != nil || target != filepath.Join(dir, "out.srt") {
		t.Fatalf("target=%q err=%v", target, err)
	}

	outDir := filepath.Join(dir, "outdir")
	if err := os.Mkdir(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	target, _, err = resolveOutputTarget(input, outDir)
	if err != nil || target != filepath.Join(outDir, "movie.srt") {
		t.Fatalf("dir output target=%q err=%v", target, err)
	}

	if _, _, err := resolveOutputTarget(input, "-0"); err != nil {
		t.Fatalf("ordinary filename should be accepted: %v", err)
	}
}

func TestExtractOneFileNeverTouchesInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.srt")
	source := []byte(srtSample)
	if err := os.WriteFile(input, source, 0644); err != nil {
		t.Fatal(err)
	}

	err := extractOneFile(input, input, 12*60*1000, 28*60*1000, true, true, false)
	if err == nil {
		t.Fatal("expected refusal to overwrite input")
	}
	after, _ := os.ReadFile(input)
	if !bytes.Equal(after, source) {
		t.Fatal("input file was modified")
	}

	// Symlink pointing at the input must also be refused.
	link := filepath.Join(dir, "link.srt")
	if err := os.Symlink(input, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := extractOneFile(input, link, 12*60*1000, 28*60*1000, true, true, false); err == nil {
		t.Fatal("expected refusal for symlink to input")
	}
	after, _ = os.ReadFile(input)
	if !bytes.Equal(after, source) {
		t.Fatal("input file was modified through symlink")
	}
}

func TestExtractOneFileDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.srt")
	source := []byte(srtSample)
	if err := os.WriteFile(input, source, 0644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "out.srt")

	if err := extractOneFile(input, output, 12*60*1000, 28*60*1000, true, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("dry run created %s", output)
	}
	after, _ := os.ReadFile(input)
	if !bytes.Equal(after, source) {
		t.Fatal("dry run modified the input")
	}
}

func TestExtractOneFileWritesOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.ass")
	if err := os.WriteFile(input, []byte(assSample), 0644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "clip.ass")

	if err := extractOneFile(input, output, 12*60*1000, 28*60*1000, true, true, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "0:00:00.00,0:00:30.00") {
		t.Fatalf("unexpected output\n%s", got)
	}
	if src, _ := os.ReadFile(input); !bytes.Equal(src, []byte(assSample)) {
		t.Fatal("source file changed")
	}
}

func TestExtractDirectoryRequiresOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "movie.srt"), []byte(srtSample), 0644); err != nil {
		t.Fatal(err)
	}
	if err := extractDirectory(dir, "", 12*60*1000, 28*60*1000, true, true, false); err == nil {
		t.Fatal("expected an error when --output is missing")
	}
	if err := extractDirectory(dir, "-", 12*60*1000, 28*60*1000, true, true, false); err == nil {
		t.Fatal("expected an error for stdout with a directory input")
	}
}

func TestExtractDirectoryCopiesTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, "season"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "season", "e01.srt"), []byte(srtSample), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")

	if err := extractDirectory(src, out, 12*60*1000, 28*60*1000, true, true, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "season", "e01.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "00:00:00,000 --> 00:00:30,000") {
		t.Fatalf("unexpected extracted cue\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(out, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("non-subtitle files should not be copied")
	}
	if srcBytes, _ := os.ReadFile(filepath.Join(src, "season", "e01.srt")); !bytes.Equal(srcBytes, []byte(srtSample)) {
		t.Fatal("source tree was modified")
	}
}

func TestExtractDirectoryRejectsOutputInsideInput(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "e01.srt"), []byte(srtSample), 0644); err != nil {
		t.Fatal(err)
	}
	// Writing into the source tree is allowed only when the destination file
	// differs from its source; the guard covers exact-path collisions.
	if err := extractDirectory(src, filepath.Join(src, "e01.srt"), 12*60*1000, 28*60*1000, true, true, false); err == nil {
		t.Fatal("expected an error when the output is the source file")
	}
	if srcBytes, _ := os.ReadFile(filepath.Join(src, "e01.srt")); !bytes.Equal(srcBytes, []byte(srtSample)) {
		t.Fatal("source was modified")
	}
}

func TestShiftModeStillWorks(t *testing.T) {
	resetExtractGlobals(t)
	shift = 1000
	fromStr, toStr = "0:00:02", "0:00:03"
	if err := parseFromTo(); err != nil {
		t.Fatal(err)
	}
	src := "1\n00:00:02,000 --> 00:00:03,000\ninside\n\n2\n00:00:05,000 --> 00:00:06,000\noutside\n"
	out := subtitleShift(&srtType{content: []byte(src)}, shift)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "00:00:03,000 --> 00:00:04,000") {
		t.Fatalf("cue inside range was not shifted\n%s", joined)
	}
	if !strings.Contains(joined, "00:00:05,000 --> 00:00:06,000") {
		t.Fatalf("cue outside range was shifted\n%s", joined)
	}
}

func TestExtractDryRunViaRun(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.srt")
	if err := os.WriteFile(input, []byte(srtSample), 0644); err != nil {
		t.Fatal(err)
	}
	resetExtractGlobals(t)
	extractMode = true
	dry = true
	inputPath = input
	fromStr, toStr = "0:12:00", "0:28:00"

	// run() parses neither flags nor positionals under go test; assign directly.
	if err := runExtract(); err != nil {
		t.Fatal(err)
	}
	if src, _ := os.ReadFile(input); !bytes.Equal(src, []byte(srtSample)) {
		t.Fatal("dry-run extract modified the input")
	}
}

func TestExtractASSCustomEventsFormat(t *testing.T) {
	src := `[Events]
Format: Start, End, Style, Text, Layer
Dialogue: 0:12:10.00,0:12:20.00,Default,hello, with comma,5
Dialogue: 0:30:00.00,0:30:10.00,Default,outside,0
`
	out, count, err := extractASS(src, 12*60*1000, 28*60*1000, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(out, "Dialogue: 0:00:10.00,0:00:20.00,Default,hello, with comma,5") {
		t.Fatalf("custom format not preserved\n%s", out)
	}
	if !strings.Contains(out, "Format: Start, End, Style, Text, Layer") {
		t.Fatalf("Format line not preserved\n%s", out)
	}
}

func TestExtractASSUppercaseDialogue(t *testing.T) {
	src := "[Events]\nDIALOGUE: 0,0:12:10.00,0:12:20.00,Default,,0,0,0,,hello\n"
	out, count, err := extractASS(src, 12*60*1000, 28*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(out, "DIALOGUE: 0,0:12:10.00,0:12:20.00,Default,,0,0,0,,hello") {
		t.Fatalf("uppercase Dialogue mishandled: count=%d\n%s", count, out)
	}
}

func TestExtractSRTNumberOnlyBlockPreserved(t *testing.T) {
	src := "1\n00:12:10,000 --> 00:12:20,000\nhello\n\nstray text without timing\n\n2\n00:30:00,000 --> 00:30:10,000\noutside\n"
	out, count, err := extractSRT(src, 12*60*1000, 28*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(out, "stray text without timing") {
		t.Fatalf("non-cue block should be preserved\n%s", out)
	}
	if strings.Contains(out, "outside") {
		t.Fatalf("out-of-range cue kept\n%s", out)
	}
}

func TestExtractSRTCRLFRoundTrip(t *testing.T) {
	crlf := strings.ReplaceAll(assSample, "\n", "\r\n")
	out, _, err := extractASS(crlf, 12*60*1000, 28*60*1000, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Fatal("ASS output mixed line endings")
	}
}
