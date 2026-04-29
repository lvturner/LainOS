# Camofox Browser Server

[Camofox](https://github.com/jo-inc/camofox-browser) is an anti-detection headless browser server for AI agents. It wraps [Camoufox](https://camoufox.com) (a Firefox fork with C++-level fingerprint spoofing) in a REST API with accessibility snapshots, stable element refs, and session isolation.

## Service Management

Camofox runs as a user-level systemd service under the `lainos` user.

```bash
# Restart
systemctl --user restart camofox

# Check status
systemctl --user status camofox

# View logs
journalctl --user -u camofox -f
```

## Configuration

### Port

Default: `9377` (set via `CAMOFOX_PORT` in the systemd unit).

### Environment Variables

Key variables are set in `~/.config/systemd/user/camofox.service`. Edit and reload:

```bash
systemctl --user edit camofox
systemctl --user daemon-reload
systemctl --user restart camofox
```

| Variable | Description | Default |
|---|---|---|
| `CAMOFOX_PORT` | Server port | `9377` |
| `CAMOFOX_API_KEY` | Enable cookie import (disabled if unset) | — |
| `CAMOFOX_ACCESS_KEY` | Bearer token auth for all routes (disabled if unset) | — |
| `CAMOFOX_CRASH_REPORT_ENABLED` | Anonymized telemetry | `false` (disabled) |
| `CAMOFOX_COOKIES_DIR` | Cookie files directory | `~/.camofox/cookies` |
| `CAMOFOX_PROFILE_DIR` | Persisted session profiles | `~/.camofox/profiles` |
| `MAX_SESSIONS` | Max concurrent browser sessions | `50` |
| `SESSION_TIMEOUT_MS` | Session inactivity timeout | `1800000` (30min) |
| `BROWSER_IDLE_TIMEOUT_MS` | Kill browser when idle | `300000` (5min) |

### Session Persistence

Sessions are persisted to `~/.camofox/profiles/`. Cookies and localStorage survive browser restarts.

```
~/.camofox/
├── cookies/          # Bootstrap cookie files (Netscape format)
└── profiles/         # Persisted session state (auto-managed)
    └── <hashed-userId>/
        └── storage_state.json
```

## API Quick Reference

Server endpoint: `http://localhost:9377`

### Tab Lifecycle

```bash
# Create a tab
curl -X POST http://localhost:9377/tabs \
  -H 'Content-Type: application/json' \
  -d '{"userId": "agent1", "url": "https://example.com"}'

# List tabs
curl http://localhost:9377/tabs?userId=agent1

# Close a tab
curl -X DELETE http://localhost:9377/tabs/TAB_ID

# Close all tabs for a user
curl -X DELETE http://localhost:9377/sessions/agent1
```

### Page Interaction

```bash
# Accessibility snapshot with element refs (e1, e2, ...)
curl http://localhost:9377/tabs/TAB_ID/snapshot?userId=agent1

# Click element by ref
curl -X POST http://localhost:9377/tabs/TAB_ID/click \
  -H 'Content-Type: application/json' \
  -d '{"userId": "agent1", "ref": "e1"}'

# Type into element
curl -X POST http://localhost:9377/tabs/TAB_ID/type \
  -H 'Content-Type: application/json' \
  -d '{"userId": "agent1", "ref": "e2", "text": "hello", "pressEnter": true}'

# Navigate
curl -X POST http://localhost:9377/tabs/TAB_ID/navigate \
  -H 'Content-Type: application/json' \
  -d '{"userId": "agent1", "url": "https://example.com"}'

# Search macro
curl -X POST http://localhost:9377/tabs/TAB_ID/navigate \
  -H 'Content-Type: application/json' \
  -d '{"userId": "agent1", "macro": "@google_search", "query": "best coffee beans"}'

# Scroll
curl -X POST http://localhost:9377/tabs/TAB_ID/scroll \
  -H 'Content-Type: application/json' \
  -d '{"userId": "agent1", "direction": "down"}'

# Screenshot
curl http://localhost:9377/tabs/TAB_ID/screenshot?userId=agent1
```

### Search Macros

`@google_search` | `@youtube_search` | `@amazon_search` | `@reddit_search` | `@reddit_subreddit` | `@wikipedia_search` | `@twitter_search` | `@yelp_search` | `@spotify_search` | `@netflix_search` | `@linkedin_search` | `@instagram_search` | `@tiktok_search` | `@twitch_search`

### YouTube Transcripts

```bash
curl -X POST http://localhost:9377/youtube/transcript \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "languages": ["en"]}'
```

### Health Check

```bash
curl http://localhost:9377/health
```

## Full Documentation

See the upstream README for complete API docs, proxy configuration, cookie import, session tracing, and more:

https://github.com/jo-inc/camofox-browser#readme

Interactive API docs are available at `http://localhost:9377/docs` when the server is running.
