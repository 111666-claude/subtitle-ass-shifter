package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

// Time formats accepted by the range flags. Hours may be more than two digits,
// but minutes and seconds must be 00-59. A fractional part is optional.
var clockArgRE = regexp.MustCompile(`^(-?)(\d+):([0-5]\d):([0-5]\d)(?:[.,](\d{1,3}))?$`)

// Time formats accepted inside subtitle files.
var (
	srtCueTimeRE = regexp.MustCompile(`^(-?)(\d+):([0-5]\d):([0-5]\d)[,.](\d{1,3})$`)
	assCueTimeRE = regexp.MustCompile(`^(-?)(\d+):([0-5]\d):([0-5]\d)\.(\d{1,3})$`)
)

// parseClock converts a matched clock string to milliseconds. The fractional
// field is interpreted as a decimal fraction, so .5 means 500ms and .05 means
// 50ms, which is also how ASS centiseconds are usually written.
func parseClock(s string, re *regexp.Regexp) (int, error) {
	m := re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid time %q, expected HH:MM:SS or HH:MM:SS.mmm", s)
	}

	hours, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, fmt.Errorf("invalid time %q: %w", s, err)
	}
	minutes, _ := strconv.Atoi(m[3])
	seconds, _ := strconv.Atoi(m[4])

	ms := 0
	if m[5] != "" {
		ms, _ = strconv.Atoi(m[5])
		for i := len(m[5]); i < 3; i++ {
			ms *= 10
		}
	}

	total := ((hours*60+minutes)*60+seconds)*1000 + ms
	if m[1] == "-" {
		total = -total
	}
	return total, nil
}

func parseExtractRange() (int, int, error) {
	if fromRegexp != "" || toRegexp != "" {
		return 0, 0, fmt.Errorf("--startRegexp/--endRegexp cannot be used with --extract")
	}
	if fromStr == "" || toStr == "" {
		return 0, 0, fmt.Errorf("--extract requires both --start and --end")
	}

	start, err := parseClock(fromStr, clockArgRE)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid --start: %w", err)
	}
	end, err := parseClock(toStr, clockArgRE)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid --end: %w", err)
	}

	if start < 0 {
		return 0, 0, fmt.Errorf("--start must not be negative (got %s)", fromStr)
	}
	if end < 0 {
		return 0, 0, fmt.Errorf("--end must not be negative (got %s)", toStr)
	}
	if start == end {
		return 0, 0, fmt.Errorf("empty range: --start and --end are both %s", formatSRTTime(start))
	}
	if start > end {
		return 0, 0, fmt.Errorf("--end must be greater than --start (%s > %s)", formatSRTTime(start), formatSRTTime(end))
	}
	return start, end, nil
}

// formatSRTTime renders milliseconds for SRT, preserving the sign for negative
// values (which can occur in broken source files).
func formatSRTTime(totalMs int) string {
	sign := ""
	if totalMs < 0 {
		sign = "-"
		totalMs = -totalMs
	}
	return fmt.Sprintf("%s%02d:%02d:%02d,%03d", sign,
		totalMs/3600000,
		(totalMs/60000)%60,
		(totalMs/1000)%60,
		totalMs%1000)
}

// formatASSTime renders milliseconds for ASS. ASS has centisecond precision,
// so milliseconds are truncated just like the existing shift output does.
func formatASSTime(totalMs int) string {
	sign := ""
	if totalMs < 0 {
		sign = "-"
		totalMs = -totalMs
	}
	return fmt.Sprintf("%s%d:%02d:%02d.%02d", sign,
		totalMs/3600000,
		(totalMs/60000)%60,
		(totalMs/1000)%60,
		totalMs%1000/10)
}

// cueInRange uses a half-open range [start, end) for both the cue and the
// requested span. A cue that merely touches a boundary (ends exactly at start,
// or begins exactly at end) is not included, and zero-length cues never
// intersect anything.
func cueInRange(cueStart, cueEnd, start, end int) bool {
	if cueEnd <= cueStart {
		return false
	}
	return cueEnd > start && cueStart < end
}

func rangeCueTimes(cueStart, cueEnd, start, end int, clip, rebase bool) (int, int) {
	s, e := cueStart, cueEnd
	if clip {
		if s < start {
			s = start
		}
		if e > end {
			e = end
		}
	}
	if rebase {
		s -= start
		e -= start
		// A cue that starts before the range has no representable negative
		// timestamp. Clamp the leading overhang to zero; --clip additionally
		// trims the tail.
		if s < 0 {
			s = 0
		}
		if e < 0 {
			e = 0
		}
	}
	return s, e
}

func splitExtractText(content string) (lines []string, eol string, trailingNewline bool) {
	content = strings.TrimPrefix(content, "\ufeff")
	eol = "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	trailingNewline = strings.HasSuffix(content, "\n")
	lines = strings.Split(content, "\n")
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	return lines, eol, trailingNewline
}

func joinExtractText(lines []string, eol string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, eol)
	if trailingNewline {
		out += eol
	}
	return out
}

// assEventsFormat describes where the Start, End and Text fields sit in
// [Events] lines. The conventional order is the default; a custom Format: line
// in the Events section overrides it.
type assEventsFormat struct {
	fields []string
}

func defaultAssEventsFormat() assEventsFormat {
	return assEventsFormat{fields: []string{
		"Layer", "Start", "End", "Style", "Name", "MarginL", "MarginR", "MarginV", "Effect", "Text",
	}}
}

func (f *assEventsFormat) parseFormatLine(line string) bool {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return false
	}
	var fields []string
	for _, part := range strings.Split(line[colon+1:], ",") {
		fields = append(fields, strings.TrimSpace(part))
	}
	if len(fields) == 0 || !containsFold(fields, "Start") || !containsFold(fields, "End") || !containsFold(fields, "Text") {
		return false
	}
	f.fields = fields
	return true
}

func containsFold(fields []string, want string) bool {
	for _, f := range fields {
		if strings.EqualFold(f, want) {
			return true
		}
	}
	return false
}

func (f assEventsFormat) index(name string) int {
	for i, field := range f.fields {
		if strings.EqualFold(field, name) {
			return i
		}
	}
	return -1
}

// extractASS keeps every non-Dialogue line (headers, styles, comments, etc.)
// exactly as it was. Only Dialogue lines are filtered and optionally clipped
// and rebased.
func extractASS(content string, start, end int, clip, rebase bool) (string, int, error) {
	lines, eol, trailing := splitExtractText(content)
	out := make([]string, 0, len(lines))
	count := 0

	format := defaultAssEventsFormat()
	inEvents := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inEvents = strings.EqualFold(trimmed, "[Events]")
			out = append(out, line)
			continue
		}
		if inEvents && strings.HasPrefix(strings.ToLower(trimmed), "format:") {
			format.parseFormatLine(line)
			out = append(out, line)
			continue
		}
		if !strings.HasPrefix(strings.ToLower(trimmed), "dialogue:") {
			out = append(out, line)
			continue
		}

		cue, err := parseASSDialogue(line, format)
		if err != nil {
			return "", 0, fmt.Errorf("ASS line %d: %w", i+1, err)
		}
		if !cueInRange(cue.start, cue.end, start, end) {
			continue
		}

		ns, ne := rangeCueTimes(cue.start, cue.end, start, end, clip, rebase)
		out = append(out, cue.render(ns, ne))
		count++
	}

	return joinExtractText(out, eol, trailing), count, nil
}

type assDialogue struct {
	prefix   string   // "Dialogue:"
	lead     string   // whitespace between the colon and the first field
	fields   []string // all fields, with the text field kept whole
	startIdx int
	endIdx   int
	textIdx  int
	start    int
	end      int
}

func parseASSDialogue(line string, format assEventsFormat) (assDialogue, error) {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return assDialogue{}, fmt.Errorf("missing ':' in Dialogue line")
	}

	startIdx, endIdx, textIdx := format.index("Start"), format.index("End"), format.index("Text")
	if startIdx < 0 || endIdx < 0 || textIdx < 0 {
		return assDialogue{}, fmt.Errorf("Events Format line does not define Start, End and Text")
	}
	maxIdx := startIdx
	if endIdx > maxIdx {
		maxIdx = endIdx
	}
	if textIdx > maxIdx {
		maxIdx = textIdx
	}

	body := line[colon+1:]
	lead := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
	fields := strings.SplitN(strings.TrimLeft(body, " \t"), ",", maxIdx+1)
	if len(fields) != maxIdx+1 {
		return assDialogue{}, fmt.Errorf("Dialogue line has %d fields, expected at least %d", len(fields), maxIdx+1)
	}

	start, err := parseClock(fields[startIdx], assCueTimeRE)
	if err != nil {
		return assDialogue{}, fmt.Errorf("invalid start time: %w", err)
	}
	endTime, err := parseClock(fields[endIdx], assCueTimeRE)
	if err != nil {
		return assDialogue{}, fmt.Errorf("invalid end time: %w", err)
	}
	if endTime < start {
		return assDialogue{}, fmt.Errorf("end time %s is before start time %s", formatASSTime(endTime), formatASSTime(start))
	}

	return assDialogue{
		prefix:   line[:colon+1],
		lead:     lead,
		fields:   fields,
		startIdx: startIdx,
		endIdx:   endIdx,
		textIdx:  textIdx,
		start:    start,
		end:      endTime,
	}, nil
}

func (c assDialogue) render(start, end int) string {
	fields := append([]string(nil), c.fields...)
	fields[c.startIdx] = formatASSTime(start)
	fields[c.endIdx] = formatASSTime(end)
	return c.prefix + c.lead + strings.Join(fields, ",")
}

type srtCue struct {
	start  int
	end    int
	text   []string
	suffix string // anything after the end timestamp, e.g. position info
}

type srtBlock struct {
	cue     *srtCue
	raw     []string
	rawOnly bool
}

// extractSRT treats blank-line separated blocks as cues. Blocks without an
// arrow time line are preserved verbatim, and selected cues are renumbered
// sequentially.
func extractSRT(content string, start, end int, clip, rebase bool) (string, int, error) {
	lines, eol, trailing := splitExtractText(content)

	var blocks []srtBlock
	for i := 0; i < len(lines); {
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		begin := i
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			i++
		}
		block := lines[begin:i]

		arrow := -1
		for j, line := range block {
			if strings.Contains(line, "-->") {
				arrow = j
				break
			}
			// Catch obviously malformed time lines instead of silently
			// preserving them.
			if strings.Contains(line, "->") && looksLikeTimeLine(line) {
				return "", 0, fmt.Errorf("SRT line %d: malformed time line %q", begin+j+1, line)
			}
		}
		if arrow < 0 {
			raw := append([]string(nil), block...)
			blocks = append(blocks, srtBlock{raw: raw, rawOnly: true})
			continue
		}

		cue, err := parseSRTCue(block, arrow)
		if err != nil {
			return "", 0, fmt.Errorf("SRT line %d: %w", begin+arrow+1, err)
		}
		if arrow > 0 {
			for _, prefix := range block[:arrow] {
				if _, err := strconv.Atoi(strings.TrimSpace(prefix)); err != nil {
					return "", 0, fmt.Errorf("SRT line %d: expected a numeric cue index before the time line", begin+1)
				}
			}
		}
		blocks = append(blocks, srtBlock{cue: cue})
	}

	outBlocks := make([]string, 0, len(blocks))
	count := 0
	for _, block := range blocks {
		if block.rawOnly {
			outBlocks = append(outBlocks, strings.Join(block.raw, eol))
			continue
		}
		if !cueInRange(block.cue.start, block.cue.end, start, end) {
			continue
		}
		ns, ne := rangeCueTimes(block.cue.start, block.cue.end, start, end, clip, rebase)
		count++

		cueLines := []string{
			strconv.Itoa(count),
			formatSRTTime(ns) + " --> " + formatSRTTime(ne) + block.cue.suffix,
		}
		cueLines = append(cueLines, block.cue.text...)
		outBlocks = append(outBlocks, strings.Join(cueLines, eol))
	}

	if len(outBlocks) == 0 {
		return "", count, nil
	}
	out := strings.Join(outBlocks, eol+eol)
	if trailing {
		out += eol
	}
	return out, count, nil
}

func looksLikeTimeLine(line string) bool {
	return clockArgRE.MatchString(strings.TrimSpace(strings.Split(line, "->")[0])) ||
		srtCueTimeRE.MatchString(strings.TrimSpace(strings.Split(line, "->")[0]))
}

func parseSRTCue(block []string, arrow int) (*srtCue, error) {
	line := block[arrow]
	idx := strings.Index(line, "-->")
	left := strings.TrimSpace(line[:idx])
	right := line[idx+3:]

	right = strings.TrimLeft(right, " \t")
	fields := strings.Fields(right)
	if len(fields) == 0 {
		return nil, fmt.Errorf("time line has no end timestamp")
	}
	endToken := fields[0]
	suffix := right[len(endToken):]

	startTime, err := parseClock(left, srtCueTimeRE)
	if err != nil {
		return nil, fmt.Errorf("invalid start time: %w", err)
	}
	endTime, err := parseClock(endToken, srtCueTimeRE)
	if err != nil {
		return nil, fmt.Errorf("invalid end time: %w", err)
	}
	if endTime < startTime {
		return nil, fmt.Errorf("end time %s is before start time %s", formatSRTTime(endTime), formatSRTTime(startTime))
	}

	text := append([]string(nil), block[arrow+1:]...)
	return &srtCue{start: startTime, end: endTime, text: text, suffix: suffix}, nil
}

func extractDecoded(content, ext string, start, end int, clip, rebase bool) (string, int, error) {
	switch strings.ToLower(ext) {
	case ".ass":
		return extractASS(content, start, end, clip, rebase)
	case ".srt":
		return extractSRT(content, start, end, clip, rebase)
	default:
		return "", 0, fmt.Errorf("unsupported subtitle format %q", ext)
	}
}

// extractFileData decodes with the same encoding detection (or --encoding
// override) used by shift mode, then re-encodes the result with that same
// encoding so the export keeps the original charset and BOM.
func extractFileData(data []byte, ext string, start, end int, clip, rebase bool) (out []byte, count int, encName string, certain bool, err error) {
	var enc encoding.Encoding
	if inputEncoding != nil {
		enc = inputEncoding
		encName = inputEncName
		certain = true
	} else {
		enc, encName, certain = charset.DetermineEncoding(data, "")
	}

	decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return nil, 0, encName, certain, fmt.Errorf("decode as %s: %w", encName, err)
	}

	// Keep a byte order mark if the source had one. html/charset leaves it in
	// the decoded text; the parser strips it so it is not treated as content.
	text := string(decoded)
	bom := ""
	if strings.HasPrefix(text, "\ufeff") {
		bom = "\ufeff"
		text = strings.TrimPrefix(text, "\ufeff")
	}

	result, count, err := extractDecoded(text, ext, start, end, clip, rebase)
	if err != nil {
		return nil, 0, encName, certain, err
	}

	encoded, _, err := transform.Bytes(enc.NewEncoder(), []byte(bom+result))
	if err != nil {
		return nil, 0, encName, certain, fmt.Errorf("encode as %s: %w", encName, err)
	}
	return encoded, count, encName, certain, nil
}

func samePath(a, b string) bool {
	if shouldClean, ok := cleanPathForCompare(a); ok {
		if cleanB, okB := cleanPathForCompare(b); okB && shouldClean == cleanB {
			return true
		}
	}
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA == nil && errB == nil && os.SameFile(ai, bi) {
		return true
	}
	return false
}

func cleanPathForCompare(p string) (string, bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, true
	}
	dir := filepath.Dir(abs)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, filepath.Base(abs)), true
	}
	return abs, true
}

func pathWithin(child, parent string) bool {
	child, ok1 := cleanPathForCompare(child)
	parent, ok2 := cleanPathForCompare(parent)
	if !ok1 || !ok2 {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func runExtract() error {
	if shift != 0 {
		return fmt.Errorf("--shift cannot be combined with --extract")
	}

	start, end, err := parseExtractRange()
	if err != nil {
		return err
	}

	absInput, err := filepath.Abs(inputPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(absInput)
	if err != nil {
		return err
	}

	if err := initInputEncoding(encOverr); err != nil {
		return err
	}
	if inputEncoding != nil {
		fmt.Fprintf(extractLogWriter(outputPath), "Using encoding %s (override)\n", inputEncName)
	}

	if info.IsDir() {
		return extractDirectory(absInput, outputPath, start, end, clipCues, rebaseCues, dry)
	}
	return extractOneFile(absInput, outputPath, start, end, clipCues, rebaseCues, dry)
}

func isExistingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// extractLogWriter sends human-readable progress to stderr only when the
// subtitle itself is written to stdout.
func extractLogWriter(output string) *os.File {
	if output == "" || output == "-" {
		return os.Stderr
	}
	return os.Stdout
}

func resolveOutputTarget(input, output string) (target string, toStdout bool, err error) {
	if output == "" || output == "-" {
		return "", true, nil
	}

	info, statErr := os.Stat(output)
	switch {
	case statErr == nil && info.IsDir():
		target = filepath.Join(output, filepath.Base(input))
	case statErr != nil && os.IsNotExist(statErr) && isExistingDir(output):
		target = filepath.Join(output, filepath.Base(input))
	default:
		target = output
	}

	if samePath(input, target) {
		return "", false, fmt.Errorf("refusing to overwrite input file %s; choose a different --output", input)
	}
	return target, false, nil
}

func extractOneFile(input, output string, start, end int, clip, rebase, dryRun bool) error {
	target, toStdout, err := resolveOutputTarget(input, output)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(input)
	if err != nil {
		return err
	}

	// With --output - the subtitle goes to stdout, so all diagnostics go to
	// stderr and stdout stays a clean subtitle stream.
	log := extractLogWriter(output)
	fmt.Fprintln(log, "\n"+filepath.Base(input))
	out, count, encName, certain, err := extractFileData(data, filepath.Ext(input), start, end, clip, rebase)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(input), err)
	}
	if !certain {
		fmt.Fprintf(log, "Warning: uncertain encoding detected for %s, assuming %s\n", input, encName)
	}
	if dryRun {
		fmt.Fprintf(log, "Would extract %d cues from %s to %s\n", count, formatSRTTime(start), formatSRTTime(end))
		fmt.Fprintln(log, "\n[Info] Dry run, no file changes.")
		return nil
	}

	if toStdout {
		fmt.Fprintf(log, "Extracted %d cues from %s to %s\n", count, formatSRTTime(start), formatSRTTime(end))
		_, err = os.Stdout.Write(out)
		return err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(target, out, 0644); err != nil {
		return err
	}
	fmt.Fprintf(log, "Extracted %d cues from %s to %s -> %s\n", count, formatSRTTime(start), formatSRTTime(end), target)
	fmt.Fprintf(log, "\n[Info] %d subtitle file updated.\n", 1)
	return nil
}

func extractDirectory(inputDir, outputDir string, start, end int, clip, rebase, dryRun bool) error {
	if outputDir == "-" {
		return fmt.Errorf("--output - (stdout) cannot be used when the input is a directory")
	}
	if outputDir == "" && !dryRun {
		return fmt.Errorf("--output directory is required when extracting from a directory")
	}

	var absOutput string
	if outputDir != "" {
		var err error
		absOutput, err = filepath.Abs(outputDir)
		if err != nil {
			return err
		}
		inputRoot, _ := cleanPathForCompare(inputDir)
		outputRoot, _ := cleanPathForCompare(absOutput)
		if inputRoot == outputRoot {
			return fmt.Errorf("refusing to write into the input directory %s; choose a different --output", inputDir)
		}
		if info, err := os.Stat(absOutput); err == nil && !info.IsDir() {
			return fmt.Errorf("--output %s exists and is not a directory", absOutput)
		}
		if !dryRun {
			if err := os.MkdirAll(absOutput, 0755); err != nil {
				return err
			}
		}
	}

	filesUpdated := 0
	cuesTotal := 0
	failures := 0
	skipOutputTree := absOutput != "" && pathWithin(absOutput, inputDir)

	walkErr := filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			fmt.Println(walkErr)
			failures++
			return nil
		}
		if info.IsDir() {
			if path != inputDir && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			if skipOutputTree && pathWithin(path, absOutput) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))
		if _, ok := supportTypes[ext]; !ok {
			return nil
		}

		var target string
		if absOutput != "" {
			rel, err := filepath.Rel(inputDir, path)
			if err != nil {
				fmt.Printf("[ERROR] %s: %v\n", path, err)
				failures++
				return nil
			}
			target = filepath.Join(absOutput, rel)
			if samePath(path, target) {
				fmt.Printf("[ERROR] %s: refusing to overwrite source file\n", path)
				failures++
				return nil
			}
		}

		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("[ERROR] %s: %v\n", path, err)
			failures++
			return nil
		}
		fmt.Println("\n" + filepath.Base(path))
		out, count, encName, certain, err := extractFileData(data, ext, start, end, clip, rebase)
		if err != nil {
			fmt.Printf("[ERROR] %s: %v\n", path, err)
			failures++
			return nil
		}
		if !certain {
			fmt.Printf("Warning: uncertain encoding detected for %s, assuming %s\n", path, encName)
		}
		cuesTotal += count

		if dryRun {
			fmt.Printf("Would extract %d cues from %s to %s\n", count, formatSRTTime(start), formatSRTTime(end))
			return nil
		}
		if target == "" {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			fmt.Printf("[ERROR] %s: %v\n", target, err)
			failures++
			return nil
		}
		if err := os.WriteFile(target, out, 0644); err != nil {
			fmt.Printf("[ERROR] %s: %v\n", target, err)
			failures++
			return nil
		}
		filesUpdated++
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	if dryRun {
		fmt.Printf("\n[Info] Dry run, no file changes. %d cues would be extracted.\n", cuesTotal)
	} else {
		fmt.Printf("\n[Info] %d subtitle files updated, %d cues extracted.\n", filesUpdated, cuesTotal)
	}
	if failures > 0 {
		return fmt.Errorf("%d subtitle file(s) failed to extract", failures)
	}
	return nil
}
