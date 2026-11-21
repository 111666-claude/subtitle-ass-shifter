<p align="center">
<img width="256" height="256" alt="logo" src="https://github.com/user-attachments/assets/2ce4fa75-3c36-4f20-b6e2-910a88d14e2b" />
</p>

# subtitle ass shifter
Shift Subtitle of [`.ass`, `.srt`] format

> [!CAUTION]
> It will replace your subtitle, **backup** your subtitle files before run. Use `--dry` for test.  
> From version `v1.2.0`, the program will automatically convert subtitle files to UTF8 encoding.

`--dry` will still scan files, match the target range, and print what would be shifted, but it will **not write any changes back to disk**.


## Usage

```
ass-shifter [path] -t [shift ms]

  Positional Variables:
    path   the subtitle path to shift (Required)
  Flags:
       --version       Displays the program version string.
    -h --help          Displays help with available flag, subcommand, and positional value parameters.
    -t --shift         shift ms (default: 0)
    -s --start         start from HH:MM:SS
    -e --end           end at HH:MM:SS
    -sr --startRegexp  start from regular expression
    -er --endRegexp    end at regular expression
    -enc --encoding    force input encoding (e.g. gbk, shift_jis); skips auto-detection
    -d --dry           dry run
    -x --extract       extract cues intersecting --start/--end instead of shifting
    -o --output        output file or directory for --extract (default: stdout; '-' is stdout)
    -clip --clip       with --extract, clip cues to the range
    -rebase --rebase   with --extract, make timestamps relative to --start
```

Use `--encoding` when auto-detection picks the wrong charset (common with GBK/Shift_JIS subtitles misread as Latin-1).

The `--start` and `--end` parameters can be used to qualify the time range of the subtitle offset.   
The `--startRegexp` and `--endRegexp` parameters can be used to match the content of the subtitle with a regular expression as the start and end of the offset time range.  
`--start` and `--startRegexp` cannot be used together. `--end` and `--endRegexp` cannot be used together. However, the start condition and end condition can still be combined freely, for example `--start` with `--endRegexp`, or `--startRegexp` with `--end`.

## Example
The `start` and `end` parameters are optional. They can also be used together.
```bash
ass-shifter ../Better.Call.Saul/S03 -t 3200
ass-shifter ../Better.Call.Saul/S03 -t 3200 -s 0:06:13
ass-shifter ../Better.Call.Saul/S03 -t -3200 -s 0:06:13 -e 0:24:12
ass-shifter ../Better.Call.Saul/S06 -sr "第.季\s*第.+集" -t 3200
ass-shifter ../Better.Call.Saul/S06 -sr "第.季\s*第.+集" -e 0:24:12 -t 3200
```

The program prints the result of the execution like the following.
```bash
ass-shifter ../Better.Call.Saul/S06 -t -3200 -s 0:06:13
Better.Call.Saul.S06E01.2022.1080p.WEB-DL.x265.10bit.ass
From 0:06:13.00 to end, 1152 lines shifted -3200ms

Better.Call.Saul.S06E02.2022.1080p.WEB-DL.x265.10bit.ass
From 0:06:13.00 to end, 1222 lines shifted -3200ms

...

Better.Call.Saul.S06E12.2022.1080p.WEB-DL.x265.10bit.ass
From 0:06:13.00 to end, 1035 lines shifted -3200ms

Better.Call.Saul.S06E13.2022.1080p.WEB-DL.x265.10bit.ass
From 0:06:13.00 to end, 1629 lines shifted -3200ms

[Info] 13 subtitle files updated.
```

Dry run would NOT change your file. Good for test.```bash
ass-shifter ../Better.Call.Saul/S06 -sr "第.季\s*第.+集" -t 3234 --dry

Better.Call.Saul.S06E01.2022.1080p.WEB-DL.x265.10bit.ass
From 0:05:32.87 to end, 1155 lines shifted 3234ms

Better.Call.Saul.S06E02.2022.1080p.WEB-DL.x265.10bit.ass
From 0:07:20.77 to end, 1221 lines shifted 3234ms

...

Better.Call.Saul.S06E11.2022.1080p.WEB-DL.x265.10bit.ass
From 0:01:39.00 to end, 1387 lines shifted 3234ms

Better.Call.Saul.S06E12.2022.1080p.WEB-DL.x265.10bit.ass
From 0:02:50.43 to end, 1145 lines shifted 3234ms

Better.Call.Saul.S06E13.2022.1080p.WEB-DL.x265.10bit.ass
From 0:05:17.70 to end, 1651 lines shifted 3234ms

[Info] Dry run, no file changes.
```

## Range export (`--extract`)

After cutting the opening from a movie you can export only the subtitles for
the part you keep, instead of shifting the whole file. `--extract` keeps every
cue that intersects `--start`..`--end` and leaves the timing untouched unless
you ask for it.

```bash
# Keep cues from 12:00 to 28:00, unchanged timestamps, write a new file
ass-shifter movie.ass -x -s 0:12:00 -e 0:28:00 -o movie.clip.ass

# Cut the cues at the range edges and rebase timestamps so 12:00 becomes 0:00
ass-shifter movie.srt -x -s 0:12:00 -e 0:28:00 --clip --rebase -o movie.clip.srt

# Preview to stdout (diagnostics go to stderr, so it can be piped)
ass-shifter movie.ass -x -s 0:12:00 -e 0:28:00 --clip --rebase > clip.ass

# Whole season: mirror the tree into an output directory
ass-shifter ./S03 -x -s 0:12:00 -e 0:28:00 --clip --rebase -o ./S03-clip
```

Behaviour:

- **Intersection is half-open**: a cue ending exactly at `--start`, or starting
  exactly at `--end`, is not included (a cue with zero duration never matches).
- **`--clip`** trims cues that cross the boundaries to the range.
- **`--rebase`** subtracts `--start` from every timestamp. With `--clip` the
  first cue starts at `0:00:00`. Without `--clip` the tail is left alone and a
  lead-in overhang is clamped to zero.
- **ASS** keeps all non-`Dialogue` lines (headers, `[V4+ Styles]`, `Comment:`,
  etc.) exactly as-is, honours a custom `Format:` line in `[Events]`, and keeps
  text containing commas and override tags untouched.
- **SRT** renumbers the kept cues sequentially and preserves trailing position
  info after the timestamp. Blocks without a timing line are kept verbatim.
- **Encoding** is detected (or forced with `--encoding`) and the output is
  written back in that same encoding, including a BOM when the source had one.
- **Never overwrites the source**: `--output` pointing at the input path (or a
  symlink to it) is rejected. `--extract` never modifies the input file.
- **Errors are explicit**: empty range (`--start == --end`), reversed range,
  negative times, malformed time strings, and bad cue timestamps all exit
  non-zero without writing anything. A range with no matching cues produces an
  empty output file (or nothing on stdout) and still succeeds.
- Directory input requires `--output`; subtitle files are mirrored into that
  directory, non-subtitle files are skipped, and `--output -` is rejected.
- `--dry` prints what would be extracted without writing anything.
