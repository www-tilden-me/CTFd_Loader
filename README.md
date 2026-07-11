# CTFd Challenge Downloader

A small Python utility for downloading challenge metadata and files from a CTFd event, or generating a score summary.

## Install

```bash
python3 -m pip install -r requirements.txt
```

## Usage

Download all visible challenges:

```bash
python3 ctfd.py pull <site_url> <location>
```

Generate a score report:

```bash
python3 ctfd.py score <site_url> <location>
```

With authentication:

```bash
python3 ctfd.py pull <site_url> <location> \
    --session "<session_cookie>" \
    --auth "<api_token>"
```

```bash
python3 ctfd.py score <site_url> <location> \
    --session "<session_cookie>" \
    --auth "<api_token>"
```

You can also store the API token in a `.env` file:

```env
CTFD_TOKEN=your_api_token
```

## Output

Downloaded challenges are organized by category and challenge name:

```text
challenges/
├── Crypto/
│   └── Example Challenge/
│       ├── META.json
│       └── challenge.txt
└── Pwn/
    └── Buffer Overflow/
        ├── META.json
        └── binary
```

The `score` command writes results to:

```text
RESULTS.json
```

Only challenges and files visible to the authenticated account are downloaded.

## TODO:
- Add Async Loading for challenges and files
- Replace the "Saved:" and "Scored:" Messages with a progress bar (maybe only on -p)