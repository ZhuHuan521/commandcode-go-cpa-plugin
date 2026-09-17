# commandcode-go-cpa-plugin

CLIProxyAPI provider plugin for [commandcode.ai](https://api.commandcode.ai).
This is a faithful Go port of the business logic in
`commandcode-proxy-master`: the plugin does **not** call the public
`/provider/v1/chat/completions` endpoint. It speaks the same private CLI wire
protocol as the Node proxy:

- `POST /alpha/generate` with the 9-key CLI request envelope
  (`config / memory / taste / skills / permissionMode / threadId / mode /
  promptCache / params`)
- deterministic per-api-key device fingerprint (`/alpha/fingerprint/record`)
- lifecycle events (`/alpha/lifecycle-events`)
- per-key 12h sessions with random jitter
- OpenAI Chat Completions, Anthropic Messages and Responses translation is
  handled by the host; this executor emits standard OpenAI chunks with
  `reasoning_content`, tool calls and usage
- streaming / non-streaming upstream idle watchdogs, zero-output 429 guards,
  incomplete-stream 502 mapping, client abort propagation and in-flight caps
  are preserved

## Install

Build as a c-shared library and place it in the CLIProxyAPI plugins directory:

```yaml
plugins:
  enabled: true
  configs:
    commandcode:
      enabled: true
      priority: 100
      api_key: user_YOUR_KEY
```

Restart `cli-proxy-api`, then open the management center. The AI Providers
workbench shows a **CommandCode** entry backed by the plugin config.

## Build

Windows PowerShell with Go >= 1.26:

```powershell
.\build.ps1
```

Debian/glibc Docker toolchain (recommended for Linux hosts):

```bash
./build.sh
```

The plugin is found as `plugins/<goos>/<goarch>/commandcode-v1.0.0.*`.

## Config

See `config.example.yaml`. Legacy `api_key` and weighted `api_keys` pools are
supported; each key may carry its own `proxy_url`. The following proxy options
are also available: `base_url`, `protocol_version`, `fingerprint_salt`,
`device_project_dir`, `cli_mode`, `cli_session_mode`,
`empty_system_placeholder`, `use_provider_models`, `zdr`, `stream_idle_ms`,
`nonstream_idle_ms`, `max_body_mb`, `max_inflight` and
`client_drain_timeout_ms`.

Model claims follow `models: [{alias, name, display_name}]`; omitting `models`
uses the built-in catalog from `commandcode-proxy-master`.

## Highlights

- **Fingerprint**: same candidate pools, same derivation salt and same
  `sha256` hashing as the Node proxy, so one API key always reports one device.
- **Session**: client session headers are preferred; otherwise a per-key UUID
  session is reused for 12h + random jitter.
- **Model catalog**: lazily fetched from `/provider/v1/models` with a 5-minute
  cache and hardcoded fallback.
- **Framing**: `/v1/messages` receives one `data: ` prefix per executor chunk;
  `/v1/chat/completions` and `/v1/responses` stay bare for the host handlers.
