# CTFd Challenge Downloader

A small Go utility for downloading challenge metadata and files from a CTFd event or generating a score report.

## Build

```bash
go mod tidy
go build -o ctfd .
```

Install system-wide:

```bash
sudo install -m 755 ctfd /usr/local/bin/ctfd
```

## Usage

```text
ctfd <pull|score|store> <location> [options]
```

Download visible challenges:

```bash
ctfd pull ./challenges
```

Generate a score report:

```bash
ctfd score ./challenges
```

Options:

```text
--base <url>          CTFd base URL
--cookie <cookie>     CTFd session cookie
--group_limit <n>     Maximum concurrent requests
```

Example:

```bash
ctfd pull ./challenges \
    --base "https://example.ctfd.io" \
    --cookie "session=your-cookie" \
    --group_limit 5
```

The tool loads settings from `<location>/META.json` when present. Explicit command-line options override saved values.

## Output

```text
challenges/
├── META.json
├── SCORE.json
├── Crypto/
│   └── Example_Challenge/
│       ├── META.json
│       └── challenge.txt
└── Pwn/
    └── Buffer_Overflow/
        ├── META.json
        └── binary
```

Only challenges and files visible to the authenticated account are downloaded.

## Development

Run without building:

```bash
go run . pull ./challenges
```

Format and check:

```bash
gofmt -w .
go vet ./...
```

## TODO:
* `Get` does not reject non-2xx HTTP responses, so login/error pages may later appear as JSON parsing errors.
* Any metadata read error is treated as missing metadata, including malformed JSON and permission errors.
* `DownloadFiles` panics on a malformed file URL instead of returning an error.
