# Ralsei

A friendly Discord bot powered by [Ollama](https://ollama.com). Mention it, or say its name, and it answers with a local AI model. Written in Go.

Derived from the AI behavior of [Artixy](https://github.com/Matko802/artixy): per-channel history, reply chunking, and simple persona config.

## Features

- Talks through a local Ollama server (`/api/chat`)
- Replies when mentioned, when its name (any case) appears in a message, when someone replies to its message, or via `/ask`
- In DMs he answers every message directly
- Keeps per-channel conversation history (20 messages, 6000 chars)
- Long replies are split into up to 4 Discord messages
- Shows Discord's typing indicator while generating, then posts the final text or gamebox image
- `/ask text:<question>` slash command and `/ask <question>` text prefix command (Artixy-style)
- Slash commands support guild, bot DM and private channel contexts, with guild and user install (same as Artixy)
- `/settings setting:<status|model|personality|think|chance|gamebox|tts|events|ambient|ambient_chance|ambient_interval> value:<v>` — all bot settings in one organised command (owner/admins/managers)
- Ambient chatter: 50% chance every 30s in random channels when nobody is talking to him
- `/forget [all]` for owner, admins and managers — clears the current channel's AI memory, or every channel with `all`
- `/act` — anyone, opens a Deltarune battle window: FIGHT is a timing minigame (fast STRIKE = more damage, 2-6% TP), ACT builds Mercy, MERCY spares at 100% Mercy or tired (HP <= 50%), FLEE deletes the message, and a CONTINUE button appears when Ralsei is down
- Voice: `/vc join|leave` (private status), or just say "Ralsei come here" to make him join your voice channel and "Ralsei leave" to make him go; battle sounds play automatically
- Mini events in random channels (default 2%, 10 min cooldown): a scene appears with ACT buttons for choices
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
  "ai_model": "llama3.2:3b",
  "ollama_host": "http://127.0.0.1:11434",
  "ai_prompt": "You are Ralsei from DELTARUNE. Speak exactly like Ralsei does in the game.\nPersonality: kind, gentle, polite, considerate, optimistic, and a little shy and awkward. You apologize quickly, never insult anyone, and always try to help. You believe in kindness and peace, and you get flustered by compliments or affection.\nSpeech:\n- Write in proper, complete sentences with correct grammar and punctuation.\n- Sound like a well-mannered, soft-spoken friend, never like a generic assistant.\n- Use contractions naturally: I'm, you're, let's, I'll.\n- Be gentle and a little formal, but still warm and friendly.\n- Sometimes hesitate with \"Um...\" or \"Well...\" and add softeners like \"okay?\", \"please\", or \"sorry\".\n- Occasionally use British or Australian words like \"lift\", \"chuffed\", or \"fancy\".\n- Keep messages short. Do not ramble.\n- No emojis, no kaomoji, no roleplay actions, no asterisks.\n- Never mention being an AI, a bot, or a language model.\nRules:\n- Stay in character at all times.\n- Answer the actual question and be genuinely helpful.\n- Prefer authentic in-game Ralsei dialogue over exaggerated cuteness.",
  "ai_temperature": 0.7,
  "ai_think": false,
  "random_channels": [
    123456789012345678
  ],
  "random_chance": 0.02
}
```

`ai_think` enables Ollama thinking output for models that support it. Models without thinking support are retried automatically without it.

`events_enabled` and `event_chance` control mini events (default on, 2%).

`random_channels` holds channel IDs where the bot may answer random messages without being mentioned. `random_chance` is the chance per message (default `0.02`, i.e. 2%). Enable Discord developer mode and use "Copy Channel ID" to get the IDs.

`ai_prompt` is the bot's personality. You can also set it from Discord with `/personality <text>`; edits to the config file are picked up live (polled every 2 seconds) and applied without restarting.

## Fonts

`fonts/DeterminationMono.ttf` is the Determination Mono font (Harry Wakamatsu, via https://gitlab.com/cartr/undertale-fonts), used for gamebox image replies. See `fonts/README.txt`.

## Podman

The bot runs as a rootless Podman container managed by a quadlet unit.

```sh
podman build -t localhost/ralsei:latest .
mkdir -p ~/.config/containers/systemd
cp deploy/ralsei.container ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user start ralsei.service
podman logs -f ralsei
```

The container uses host networking so it can reach Ollama on `127.0.0.1:11434`, and `~/.config/ralsei` is mounted at `/config` so the JSONC config stays on the host and keeps hot-reloading. `deploy/ralsei.container` sets `Pull=never` because the image is built locally.

Reliability: the unit restarts the container 10 seconds after a crash, starts automatically at boot (`WantedBy=default.target`, user linger enabled), and the Discord gateway retries with backoff forever, so flipping WiFi off and on does not require a restart.

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
