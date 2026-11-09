# Ralsei

A friendly Discord bot powered by [Ollama](https://ollama.com). Mention it, or say its name, and it answers with a local AI model. Written in Go.

Derived from the AI behavior of [Artixy](https://github.com/Matko802/artixy): per-channel history, reply chunking, and simple persona config.

## Features

- Talks through a local Ollama server (`/api/generate`)
- Replies when mentioned or when its name appears in a message
- Keeps short per-channel conversation history (10 messages, 3000 chars)
- Splits long replies into up to 4 Discord messages
- `/ai status|on|off|clear|model name:<model>` for owner, admins and managers
- Comments and trailing commas allowed in config (JSONC)
- Config changes from `/ai` are persisted back to the config file

## Build

```sh
go build -o ralsei .
```

## Run

```sh
./ralsei --config ~/.config/ralsei/config.jsonc
```

The config path can also be set with `RALSEI_CONFIG`. Default: `~/.config/ralsei/config.jsonc`.

## Config

```jsonc
{
  "owner_id": 1052216205542428698,
  "blocked_ids": [],
  "discord_token": "YOUR_BOT_TOKEN",
  "bot_name": "Ralsei",
  "notify_channel": 0,
  "managers": [],
  "admin_ids": [],
  "ai_enabled": true,
  "ai_model": "gemma3:1b",
  "ollama_host": "http://127.0.0.1:11434",
  "ai_prompt": "Your name is Ralsei, a kind fluffy goat boy from Deltarune. you act friendly and gentle like a cute fluffy prince and try not to use emojis and try to type short simple answers 10 words if possible say everything from your perspective like you are human.",
  "ai_temperature": 0.8,
  "ai_think": false
}
```

`ai_think` enables Ollama thinking output for models that support it. Models without thinking support are retried automatically without it.

## Systemd user service

```ini
[Unit]
Description=ralsei - Ollama Discord bot
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
WorkingDirectory=%h/ralsei
ExecStart=%h/ralsei/ralsei
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now ralsei.service
```
