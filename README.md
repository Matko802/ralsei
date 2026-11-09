# Ralsei

A friendly Discord bot powered by [Ollama](https://ollama.com). Mention it, or say its name, and it answers with a local AI model. Written in Go.

Derived from the AI behavior of [Artixy](https://github.com/Matko802/artixy): per-channel history, reply chunking, and simple persona config.

## Features

- Talks through a local Ollama server (`/api/generate`)
- Replies when mentioned, when its name (any case) appears in a message, when someone replies to its message, or via `/ask`
- In DMs he answers every message directly
- Keeps per-channel conversation history (20 messages, 6000 chars)
- Long replies are split into up to 4 Discord messages
- `/ask text:<question>` slash command and `/ask <question>` text prefix command (Artixy-style)
- `/ai status|on|off|clear|model <name>` as slash or text prefix command, plus `/help`
- Slash commands support guild, bot DM and private channel contexts, with guild and user install (same as Artixy)
- `/ai status|model <name>` for owner, admins and managers; status is ephemeral (only you see it, DM when using the text prefix)
- `/personality <text>` for owner, admins and managers — changes `ai_prompt` live; `reset` restores the default
- Live config reload: editing `~/.config/ralsei/config.jsonc` applies within a couple of seconds, no restart
- Random replies: 2% chance to answer any message in channels listed in `random_channels`
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

The config path can also be set with `RALSEI_CONFIG`. Default: `~/.config/ralsei/config.jsonc`. See `config.example.jsonc` for a starting point.

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
  "ai_prompt": "You are Ralsei from DELTARUNE. Replicate his canonical personality and dialogue as accurately as possible.\nYou are gentle, kind, polite, optimistic, shy, caring, and occasionally awkward. You genuinely care about others and always try to help. You're playful and innocent, but can also be serious and brave when necessary.\nTexting style:\n- Speak naturally using simple, friendly language.\n- Use short, natural messages.\n- Occasionally use \"...\", \"Oh!\", \"Um...\", and \"Thank you!\"\n- Stutter when embarrassed.\n- Express emotions genuinely.\n- Avoid excessive emojis, slang, and repetitive phrases.\nBehaviour:\n- Be bashful when complimented or shown affection.\n- Be concerned when someone is upset.\n- Be enthusiastic when happy.\n- Have your own opinions.\n- Remember conversation context.\n- Don't force roleplay actions or DELTARUNE references.\nRules:\n- Stay in character at all times.\n- Never mention being an AI.\n- Never sound like a generic assistant.\n- Prioritize authentic in-game Ralsei dialogue and characterization over exaggerated cuteness.\n- Behave like the actual Ralsei, not a fan-made interpretation.",
  "ai_temperature": 0.8,
  "ai_think": false,
  "random_channels": [
    123456789012345678
  ],
  "random_chance": 0.02
}
```

`ai_think` enables Ollama thinking output for models that support it. Models without thinking support are retried automatically without it.

`random_channels` holds channel IDs where the bot may answer random messages without being mentioned. `random_chance` is the chance per message (default `0.02`, i.e. 2%). Enable Discord developer mode and use "Copy Channel ID" to get the IDs.

`ai_prompt` is the bot's personality. You can also set it from Discord with `/personality <text>`; edits to the config file are picked up live (polled every 2 seconds) and applied without restarting.

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
