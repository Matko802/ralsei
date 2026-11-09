package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Matko802/ralsei/internal/ai"
	"github.com/Matko802/ralsei/internal/audio"
	"github.com/Matko802/ralsei/internal/config"
	"github.com/Matko802/ralsei/internal/events"
	"github.com/Matko802/ralsei/internal/gamebox"
	"github.com/Matko802/ralsei/internal/term"
	"github.com/Matko802/ralsei/internal/vm"
)

type App struct {
	mu        sync.Mutex
	eventMu   sync.Mutex
	battleMu  sync.Mutex
	voiceMu   sync.Mutex
	lastEvent map[string]time.Time
	battles   map[string]*battleState
	cfg       *config.Config
	cfgPath   string
	eng       *ai.Engine
	gb        *gamebox.Renderer
	dg        *discordgo.Session
	voice     *discordgo.VoiceConnection
	voiceCh   string
	soundQ    chan audio.Sound
	termMu    sync.Mutex
	termMode  map[string]bool
	termMsg   map[string]string
	term      *term.Renderer
	vmc       vm.Client
}

func main() {
	cfgPath := flag.String("config", "", "path to config.jsonc")
	flag.Parse()
	path := strings.TrimSpace(*cfgPath)
	if path == "" {
		path = config.Path()
	}

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	app := &App{cfg: cfg, cfgPath: path, lastEvent: make(map[string]time.Time), battles: make(map[string]*battleState), soundQ: make(chan audio.Sound, 16), termMode: make(map[string]bool), termMsg: make(map[string]string)}
	app.vmc = vm.Client{URI: cfg.VMURI, Name: cfg.VMName}
	app.term = loadTerm(cfg.TerminalFont)
	app.eng = ai.New(ai.Config{
		Host:        cfg.OllamaHost,
		Model:       cfg.AIModel,
		System:      cfg.AIPrompt,
		Temperature: cfg.AITemperature,
		Think:       cfg.AIThink,
	})
	app.gb = loadGamebox(path)
	if app.gb != nil {
		log.Println("gamebox renderer ready")
	}

	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Fatalf("discord: %v", err)
	}
	dg.AddHandler(app.ready)
	dg.AddHandler(app.messageCreate)
	dg.AddHandler(app.interactionCreate)
	dg.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageTyping |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageTyping |
		discordgo.IntentsMessageContent)

	app.dg = dg
	if err := dg.Open(); err != nil {
		log.Fatalf("gateway: %v", err)
	}
	go app.watchConfig(path, cfg.DiscordToken)
	go app.soundWorker()

	log.Println("ralsei is running, press ctrl+c to exit")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	dg.Close()
}

func (a *App) ready(s *discordgo.Session, e *discordgo.Ready) {
	meName := a.botName()
	log.Printf("logged in as %s (%s)", e.User.String(), meName)
	go a.loadAvatar(e.User)
	go a.warmModel()

	cmds := []*discordgo.ApplicationCommand{
		{
			Name:        "ai",
			Description: "control the AI",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "action",
					Description: "what to do with the AI",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "status", Value: "status"},
						{Name: "model", Value: "model"},
					},
				},
				{
					Name:        "name",
					Description: "ollama model name, used with action=model",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
				},
			},
		},
		{
			Name:        "ask",
			Description: "ask the AI something",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "text",
					Description: "your question",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
		{
			Name:        "personality",
			Description: "change how the AI behaves",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "text",
					Description: "new personality text, or reset to restore the default",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
				},
			},
		},
		{
			Name:        "think",
			Description: "turn AI thinking on or off",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "value",
					Description: "true or false, leave empty to show the current setting",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "true", Value: "true"},
						{Name: "false", Value: "false"},
					},
				},
			},
		},
		{
			Name:        "chance",
			Description: "set the random reply chance in percent",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "percent",
					Description: "0 to 100, leave empty to show the current chance",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
				},
			},
		},
		{
			Name:        "gamebox",
			Description: "toggle Deltarune-style image replies",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "value",
					Description: "on or off, leave empty to show the current setting",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
					},
				},
			},
		},
		{
			Name:        "forget",
			Description: "clear the AI memory",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "scope",
					Description: "channel (default) or all channels",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "channel", Value: "channel"},
						{Name: "all", Value: "all"},
					},
				},
			},
		},
		{
			Name:        "event",
			Description: "toggle mini events",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "value",
					Description: "on or off, leave empty to show the current setting",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
					},
				},
			},
		},
		{
			Name:        "act",
			Description: "ACT on Ralsei",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
		},
		{
			Name:        "vc",
			Description: "join or leave your voice channel",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "action",
					Description: "join or leave",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "join", Value: "join"},
						{Name: "leave", Value: "leave"},
						{Name: "terminalmode", Value: "terminalmode"},
					},
				},
			},
		},
		{
			Name:        "tts",
			Description: "toggle text to speech in voice",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
				discordgo.InteractionContextBotDM,
				discordgo.InteractionContextPrivateChannel,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "value",
					Description: "on or off, leave empty to show the current setting",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
					},
				},
			},
		},
		{
			Name:        "terminalmode",
			Description: "toggle the AI terminal in the artix VM",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "value",
					Description: "on or off",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
					},
				},
			},
		},
		{
			Name:        "term",
			Description: "run a command in the artix VM",
			Contexts: &[]discordgo.InteractionContextType{
				discordgo.InteractionContextGuild,
			},
			IntegrationTypes: &[]discordgo.ApplicationIntegrationType{
				discordgo.ApplicationIntegrationGuildInstall,
				discordgo.ApplicationIntegrationUserInstall,
			},
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "command",
					Description: "command to run",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
	}
	guildCmds := withContexts(cmds, []discordgo.InteractionContextType{
		discordgo.InteractionContextGuild,
	})
	dmCmds := withContexts(cmds, []discordgo.InteractionContextType{
		discordgo.InteractionContextBotDM,
		discordgo.InteractionContextPrivateChannel,
	})
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", dmCmds); err != nil {
		log.Printf("command register global: %v", err)
	}
	for _, g := range e.Guilds {
		if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, g.ID, guildCmds); err != nil {
			log.Printf("command register guild %s: %v", g.ID, err)
		}
	}
}

func withContexts(cmds []*discordgo.ApplicationCommand, ctx []discordgo.InteractionContextType) []*discordgo.ApplicationCommand {
	out := make([]*discordgo.ApplicationCommand, 0, len(cmds))
	for _, c := range cmds {
		clone := *c
		clone.Contexts = &ctx
		out = append(out, &clone)
	}
	return out
}

func (a *App) snapshot() *config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *App) botName() string {
	return strings.ToLower(strings.TrimSpace(a.snapshot().BotName))
}

func enabled(cfg *config.Config) bool {
	return cfg.AIEnabled
}

func (a *App) isElevated(uid uint64) bool {
	cfg := a.snapshot()
	if uid == cfg.OwnerID {
		return true
	}
	for _, id := range cfg.AdminIDs {
		if id == uid {
			return true
		}
	}
	for _, id := range cfg.Managers {
		if id == uid {
			return true
		}
	}
	return false
}

func (a *App) isBlocked(uid uint64) bool {
	cfg := a.snapshot()
	for _, id := range cfg.BlockedIDs {
		if id == uid {
			return true
		}
	}
	return false
}

func u64(s string) (uint64, error) {
	var v uint64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an id: %q", s)
		}
		v = v*10 + uint64(r-'0')
	}
	return v, nil
}

func stripIDTag(content, id string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(content, "<@"+id+">", ""), "<@!"+id+">", ""))
}

func stripName(content, name string) string {
	if name == "" {
		return strings.TrimSpace(content)
	}
	var kept []string
	for _, w := range strings.Fields(content) {
		if strings.Contains(strings.ToLower(w), name) {
			continue
		}
		kept = append(kept, w)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

func (a *App) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.ID == s.State.User.ID || m.Author.Bot || m.WebhookID != "" {
		return
	}
	uid, err := u64(m.Author.ID)
	if err == nil && a.isBlocked(uid) {
		return
	}

	trimmedRaw := strings.TrimSpace(m.Content)
	lowerRaw := strings.ToLower(trimmedRaw)
	if lowerRaw == "/ai" || strings.HasPrefix(lowerRaw, "/ai ") {
		a.handlePrefixAI(s, m)
		return
	}
	if lowerRaw == "/personality" || strings.HasPrefix(lowerRaw, "/personality ") {
		a.handlePrefixPersonality(s, m)
		return
	}
	if lowerRaw == "/think" || strings.HasPrefix(lowerRaw, "/think ") {
		a.handlePrefixThink(s, m)
		return
	}
	if lowerRaw == "/chance" || strings.HasPrefix(lowerRaw, "/chance ") {
		a.handlePrefixChance(s, m)
		return
	}
	if lowerRaw == "/gamebox" || strings.HasPrefix(lowerRaw, "/gamebox ") {
		a.handlePrefixGamebox(s, m)
		return
	}
	if lowerRaw == "/forget" || strings.HasPrefix(lowerRaw, "/forget ") {
		a.handlePrefixForget(s, m)
		return
	}
	if lowerRaw == "/event" || strings.HasPrefix(lowerRaw, "/event ") {
		a.handlePrefixEvent(s, m)
		return
	}
	if lowerRaw == "/act" || strings.HasPrefix(lowerRaw, "/act ") {
		a.handleActCommand(s, m)
		return
	}
	if lowerRaw == "/vc" || strings.HasPrefix(lowerRaw, "/vc ") {
		a.handlePrefixVc(s, m)
		return
	}
	if lowerRaw == "/tts" || strings.HasPrefix(lowerRaw, "/tts ") {
		a.handlePrefixTTS(s, m)
		return
	}
	if lowerRaw == "/terminalmode" || strings.HasPrefix(lowerRaw, "/terminalmode ") {
		a.handlePrefixTerminalMode(s, m)
		return
	}
	if lowerRaw == "/term" || strings.HasPrefix(lowerRaw, "/term ") {
		a.handlePrefixTerm(s, m)
		return
	}
	if lowerRaw == "/help" || lowerRaw == "/commands" {
		a.sendHelp(s, m)
		return
	}

	cfg := a.snapshot()
	id := s.State.User.ID
	name := a.botName()
	content := m.Content
	lower := strings.ToLower(content)
	trimmed := strings.TrimSpace(lower)

	isAsk := trimmed == "/ask" || strings.HasPrefix(trimmed, "/ask ")
	isDM := m.GuildID == ""
	mentioned := strings.Contains(content, "<@"+id+">") ||
		strings.Contains(content, "<@!"+id+">") ||
		(name != "" && strings.Contains(lower, name))

	if m.GuildID != "" && mentioned {
		body := strings.ToLower(stripName(stripIDTag(content, id), name))
		if strings.HasPrefix(body, "come") || strings.Contains(body, "come here") {
			a.handleCome(s, m)
			return
		}
		if strings.HasPrefix(body, "leave") || strings.Contains(body, "leave now") {
			a.handleVoiceLeave(s, m)
			return
		}
	}

	if a.terminalOn(m.ChannelID) {
		uid, err := u64(m.Author.ID)
		if err == nil && a.isElevated(uid) && !strings.HasPrefix(strings.TrimSpace(content), "/") {
			go a.runTerminalPrompt(m.ChannelID, uid, strings.TrimSpace(content))
			return
		}
	}
	repliedToBot := a.repliedToBot(s, m, id)

	var prompt string
	switch {
	case isAsk:
		prompt = strings.TrimSpace(strings.TrimSpace(m.Content)[4:])
		if prompt == "" {
			prompt = "hello"
		}
	case repliedToBot || mentioned || isDM:
		prompt = stripName(stripIDTag(content, id), name)
		if strings.TrimSpace(prompt) == "" {
			prompt = "hello"
		}
	default:
		if a.eventHit(cfg, m.ChannelID) {
			a.triggerEvent(s, m)
			return
		}
		if !a.randomHit(cfg, m.ChannelID) {
			return
		}
		prompt = strings.TrimSpace(content)
		if len([]rune(prompt)) < 2 {
			return
		}
		log.Printf("random reply in %s", m.ChannelID)
	}

	if !enabled(cfg) {
		if isAsk || mentioned || repliedToBot || isDM {
			_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI is off right now.", m.Reference())
		}
		return
	}

	historyKey, err := u64(m.ChannelID)
	if err != nil {
		return
	}
	display := m.Author.GlobalName
	if display == "" {
		display = m.Author.Username
	}
	a.askAndReply(s, m.ChannelID, m.Reference(), historyKey, ai.SpeakerTag(display), prompt)
}

func (a *App) repliedToBot(s *discordgo.Session, m *discordgo.MessageCreate, botID string) bool {
	if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil {
		return m.ReferencedMessage.Author.ID == botID
	}
	if m.MessageReference != nil && m.MessageReference.MessageID != "" {
		ref, err := s.ChannelMessage(m.ChannelID, m.MessageReference.MessageID)
		if err == nil && ref.Author != nil {
			return ref.Author.ID == botID
		}
	}
	return false
}

func (a *App) editInteractionImage(s *discordgo.Session, i *discordgo.InteractionCreate, img []byte) error {
	att := []*discordgo.MessageAttachment{}
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Attachments: &att,
		Files:       []*discordgo.File{{Name: "ralsei.png", Reader: bytes.NewReader(img)}},
	})
	return err
}

func (a *App) askAndReply(s *discordgo.Session, channelID string, ref *discordgo.MessageReference, historyKey uint64, speaker, prompt string) {
	stopTyping := a.startTyping(channelID)
	reply, err := a.eng.Ask(historyKey, speaker, prompt)
	close(stopTyping)
	if err != nil {
		log.Printf("ai: %v", err)
		_, _ = s.ChannelMessageSendReply(channelID, "AI error, try again later.", ref)
		return
	}
	a.sendReply(s, channelID, ref, reply)
	a.speak(reply)
}

func (a *App) sendReply(s *discordgo.Session, channelID string, ref *discordgo.MessageReference, text string) {
	cfg := a.snapshot()
	if cfg.Gamebox && a.gb != nil {
		img, err := a.gb.Render(text)
		if err == nil {
			_, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
				Files:     []*discordgo.File{{Name: "ralsei.png", Reader: bytes.NewReader(img)}},
				Reference: ref,
			})
			if err == nil {
				return
			}
			log.Printf("gamebox send: %v", err)
		} else {
			log.Printf("gamebox render: %v", err)
		}
	}
	for i, c := range ai.ChunkReply(text) {
		if i == 0 {
			if _, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
				Content:   c,
				Reference: ref,
			}); err != nil {
				log.Printf("send: %v", err)
				return
			}
		} else {
			if _, err := s.ChannelMessageSend(channelID, c); err != nil {
				log.Printf("send: %v", err)
				return
			}
		}
	}
}

func (a *App) eventHit(cfg *config.Config, channelID string) bool {
	if !cfg.EventsEnabled {
		return false
	}
	cid, err := u64(channelID)
	if err != nil {
		return false
	}
	allowed := false
	for _, id := range cfg.RandomChannels {
		if id == cid {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if time.Since(a.lastEvent[channelID]) < 10*time.Minute {
		return false
	}
	chance := cfg.EventChance
	if chance <= 0 {
		chance = 0.02
	}
	if chance > 1 {
		chance = 1
	}
	if rand.Float64() < chance {
		a.lastEvent[channelID] = time.Now()
		return true
	}
	return false
}

func buttonStyle(name string) discordgo.ButtonStyle {
	switch name {
	case "success":
		return discordgo.SuccessButton
	case "danger":
		return discordgo.DangerButton
	case "primary":
		return discordgo.PrimaryButton
	default:
		return discordgo.SecondaryButton
	}
}

func (a *App) eventComponents(index int, ev events.Event) []discordgo.MessageComponent {
	row := make([]discordgo.MessageComponent, 0, len(ev.Actions))
	for j, act := range ev.Actions {
		row = append(row, discordgo.Button{
			Label:    act.Label,
			Style:    buttonStyle(act.Style),
			CustomID: fmt.Sprintf("evt:%d:%d", index, j),
		})
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: row}}
}

func (a *App) triggerEvent(s *discordgo.Session, m *discordgo.MessageCreate) {
	all := events.All()
	idx := rand.IntN(len(all))
	ev := all[idx]
	log.Printf("mini event %d in %s", idx, m.ChannelID)
	a.playSound("event")

	msg := &discordgo.MessageSend{
		Components: a.eventComponents(idx, ev),
		Reference:  m.Reference(),
	}
	cfg := a.snapshot()
	if cfg.Gamebox && a.gb != nil {
		if img, err := a.gb.Render(ev.Title + "\n" + ev.Text); err == nil {
			msg.Files = []*discordgo.File{{Name: "ralsei.png", Reader: bytes.NewReader(img)}}
		}
	}
	if len(msg.Files) == 0 {
		msg.Content = "**" + ev.Title + "**\n" + ev.Text
	}
	if _, err := s.ChannelMessageSendComplex(m.ChannelID, msg); err != nil {
		log.Printf("event send: %v", err)
	}
}

type actAction struct {
	Label string
	Style string
	Text  string
}

func actActions() []actAction {
	return []actAction{
		{Label: "Compliment", Style: "success", Text: "(You tell Ralsei he is doing great. He looks away, ears pink.) That means a lot, thank you."},
		{Label: "Hug", Style: "primary", Text: "(You hug Ralsei. He goes very still, then hugs back.) Oh! Okay. This is nice."},
		{Label: "Flirt", Style: "danger", Text: "(You flirt with Ralsei. He turns bright red and hides behind his scarf.) I, um. I do not know what to say to that!"},
		{Label: "Check", Style: "secondary", Text: "(RALSEI - ATK 3 DEF 3. A gentle prince who believes in you. His scarf is very soft.)"},
	}
}

type battleState struct {
	HP         int
	MaxHP      int
	Mercy      int
	TP         int
	FightStart time.Time
}

func (a *App) newBattle() (string, *battleState) {
	id := fmt.Sprintf("%x", rand.Uint64())
	st := &battleState{HP: 70, MaxHP: 70}
	a.battleMu.Lock()
	a.battles[id] = st
	a.battleMu.Unlock()
	return id, st
}

func (a *App) dropBattle(id string) {
	a.battleMu.Lock()
	delete(a.battles, id)
	a.battleMu.Unlock()
}

func battleComponents(id string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "FIGHT", Style: discordgo.DangerButton, CustomID: "battle:" + id + ":fight"},
		discordgo.Button{Label: "ACT", Style: discordgo.PrimaryButton, CustomID: "battle:" + id + ":act"},
		discordgo.Button{Label: "MERCY", Style: discordgo.SuccessButton, CustomID: "battle:" + id + ":mercy"},
		discordgo.Button{Label: "FLEE", Style: discordgo.SecondaryButton, CustomID: "battle:" + id + ":flee"},
	}}}
}

func actMenuComponents(id string) []discordgo.MessageComponent {
	actions := actActions()
	row := make([]discordgo.MessageComponent, 0, len(actions)+1)
	for i, act := range actions {
		row = append(row, discordgo.Button{
			Label:    act.Label,
			Style:    buttonStyle(act.Style),
			CustomID: fmt.Sprintf("battle:%s:act:%d", id, i),
		})
	}
	row = append(row, discordgo.Button{Label: "Back", Style: discordgo.SecondaryButton, CustomID: "battle:" + id + ":back"})
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: row}}
}

func strikeComponents(id string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "STRIKE", Style: discordgo.DangerButton, CustomID: "battle:" + id + ":strike"},
	}}}
}

func continueComponents(id string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "CONTINUE", Style: discordgo.PrimaryButton, CustomID: "battle:" + id + ":continue"},
		discordgo.Button{Label: "FLEE", Style: discordgo.SecondaryButton, CustomID: "battle:" + id + ":flee"},
	}}}
}

func spareable(st *battleState) bool {
	return st.Mercy >= 100 || st.HP <= st.MaxHP/2
}

func battleText(st *battleState) string {
	mark := ""
	if spareable(st) {
		mark = " *"
	}
	return fmt.Sprintf("RALSEI%s   HP %d/%d\nMercy %d%%   TP %d%%\nWhat will you do?", mark, st.HP, st.MaxHP, st.Mercy, st.TP)
}

func actText(id string) string {
	return "RALSEI   HP 70/70\nACT"
}

func (a *App) sendBattle(s *discordgo.Session, channelID string, ref *discordgo.MessageReference) {
	id, st := a.newBattle()
	a.playSound("menu")
	msg := &discordgo.MessageSend{
		Components: battleComponents(id),
		Reference:  ref,
	}
	cfg := a.snapshot()
	if cfg.Gamebox && a.gb != nil {
		if img, err := a.gb.Render(battleText(st)); err == nil {
			msg.Files = []*discordgo.File{{Name: "ralsei.png", Reader: bytes.NewReader(img)}}
		}
	}
	if len(msg.Files) == 0 {
		msg.Content = battleText(st)
	}
	if _, err := s.ChannelMessageSendComplex(channelID, msg); err != nil {
		log.Printf("battle send: %v", err)
		a.dropBattle(id)
	}
}

func (a *App) handleActCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	a.sendBattle(s, m.ChannelID, m.Reference())
}

func (a *App) handleActInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	id, st := a.newBattle()
	go func() {
		a.autoJoinVoice(i)
		a.playSound("menu")
	}()
	a.respondGamebox(s, i, battleText(st), battleComponents(id))
}

func (a *App) fleeMenu(s *discordgo.Session, i *discordgo.InteractionCreate, id string) {
	a.dropBattle(id)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		log.Printf("flee ack: %v", err)
	}
	if i.Message != nil {
		if err := s.ChannelMessageDelete(i.ChannelID, i.Message.ID); err != nil {
			log.Printf("flee delete: %v", err)
		}
	}
}

func (a *App) respondGamebox(s *discordgo.Session, i *discordgo.InteractionCreate, text string, components []discordgo.MessageComponent) {
	typ := discordgo.InteractionResponseChannelMessageWithSource
	if i.Type == discordgo.InteractionMessageComponent {
		typ = discordgo.InteractionResponseUpdateMessage
	}
	att := []*discordgo.MessageAttachment{}
	cfg := a.snapshot()
	if cfg.Gamebox && a.gb != nil {
		if img, err := a.gb.Render(text); err == nil {
			err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: typ,
				Data: &discordgo.InteractionResponseData{
					Files:       []*discordgo.File{{Name: "ralsei.png", Reader: bytes.NewReader(img)}},
					Attachments: &att,
					Components:  components,
				},
			})
			if err == nil {
				return
			}
			log.Printf("gamebox respond: %v", err)
		}
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: typ,
		Data: &discordgo.InteractionResponseData{
			Content:     text,
			Components:  components,
			Attachments: &att,
		},
	})
	if err != nil {
		log.Printf("respond: %v", err)
	}
}

func (a *App) handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) == 0 {
		return
	}
	switch parts[0] {
	case "battle":
		if len(parts) < 3 {
			return
		}
		id := parts[1]
		action := parts[2]
		a.battleMu.Lock()
		st := a.battles[id]
		if st == nil {
			a.battleMu.Unlock()
			respond(s, i, "This battle is over. Use /act to start a new one.")
			return
		}
		switch action {
		case "fight":
			st.FightStart = time.Now()
			a.playSound("select")
			text := fmt.Sprintf("RALSEI   HP %d/%d\nFIGHT\nPress STRIKE with good timing!", st.HP, st.MaxHP)
			a.battleMu.Unlock()
			a.respondGamebox(s, i, text, strikeComponents(id))
		case "strike":
			elapsed := time.Since(st.FightStart)
			acc := 100 - int(elapsed.Milliseconds())/40
			if acc > 100 {
				acc = 100
			}
			if acc < 0 {
				acc = 0
			}
			dmg := 0
			if acc >= 10 {
				dmg = acc * 22 / 100
				if dmg < 1 {
					dmg = 1
				}
			}
			st.TP += acc / 20
			if st.TP > 100 {
				st.TP = 100
			}
			if dmg == 0 {
				a.playSound("miss")
			} else {
				a.playSound("hit")
			}
			var text string
			if dmg == 0 {
				text = "MISS! (Your timing was off.) Ralsei looks relieved."
			} else {
				st.HP -= dmg
				if st.HP < 0 {
					st.HP = 0
				}
				reaction := "Ow! That was a good hit."
				if st.HP <= 0 {
					reaction = "(Ralsei falls over.) Oh... that is okay. I can get back up."
				} else if st.HP <= st.MaxHP/2 {
					reaction = "(Ralsei is tired and breathing hard.)"
				}
				text = fmt.Sprintf("(You strike Ralsei. Accuracy %d%%, %d damage.) %s", acc, dmg, reaction)
			}
			if st.HP <= 0 {
				a.battleMu.Unlock()
				a.speak(text)
				a.respondGamebox(s, i, text+"\n\nRalsei is down.", continueComponents(id))
				return
			}
			menu := battleText(st)
			a.battleMu.Unlock()
			a.speak(text)
			a.respondGamebox(s, i, text+"\n\n"+menu, battleComponents(id))
		case "continue":
			a.playSound("continue")
			st.HP = st.MaxHP
			menu := battleText(st)
			a.battleMu.Unlock()
			a.speak("I am back. Let us keep going.")
			a.respondGamebox(s, i, "(Ralsei gets back up.)\n\n"+menu, battleComponents(id))
		case "mercy":
			if spareable(st) {
				a.battleMu.Unlock()
				a.dropBattle(id)
				a.playSound("win")
				a.speak("Thank you for sparing me.")
				a.respondGamebox(s, i, "YOU WON!\nRalsei was spared.", nil)
				return
			}
			a.playSound("miss")
			st.Mercy += 10
			if st.Mercy > 100 {
				st.Mercy = 100
			}
			menu := battleText(st)
			a.battleMu.Unlock()
			a.respondGamebox(s, i, "You tried to SPARE Ralsei, but his name was not YELLOW...\n(+10% MERCY)\n\n"+menu, battleComponents(id))
		case "flee":
			a.battleMu.Unlock()
			a.playSound("flee")
			a.fleeMenu(s, i, id)
		case "act":
			if len(parts) == 3 {
				a.battleMu.Unlock()
				a.playSound("menu")
				a.respondGamebox(s, i, actText(id), actMenuComponents(id))
				return
			}
			idx, err := strconv.Atoi(parts[3])
			if err != nil {
				a.battleMu.Unlock()
				return
			}
			actions := actActions()
			if idx < 0 || idx >= len(actions) {
				a.battleMu.Unlock()
				return
			}
			act := actions[idx]
			if act.Label != "Check" {
				st.Mercy += 25
				if st.Mercy > 100 {
					st.Mercy = 100
				}
			}
			a.playSound("act")
			menu := battleText(st)
			a.battleMu.Unlock()
			a.speak(act.Text)
			a.respondGamebox(s, i, act.Text+"\n\n"+menu, battleComponents(id))
		case "back":
			a.playSound("menu")
			menu := battleText(st)
			a.battleMu.Unlock()
			a.respondGamebox(s, i, menu, battleComponents(id))
		default:
			a.battleMu.Unlock()
		}
	case "act":
		if len(parts) != 2 {
			return
		}
		idx, err := strconv.Atoi(parts[1])
		if err != nil {
			return
		}
		actions := actActions()
		if idx < 0 || idx >= len(actions) {
			return
		}
		a.respondGamebox(s, i, actions[idx].Text, nil)
	case "evt":
		if len(parts) != 3 {
			return
		}
		evIdx, err1 := strconv.Atoi(parts[1])
		actIdx, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			return
		}
		all := events.All()
		if evIdx < 0 || evIdx >= len(all) {
			return
		}
		ev := all[evIdx]
		if actIdx < 0 || actIdx >= len(ev.Actions) {
			return
		}
		a.playSound("act")
		a.speak(ev.Actions[actIdx].Text)
		a.respondGamebox(s, i, ev.Actions[actIdx].Text, a.eventComponents(evIdx, ev))
	}
}

func (a *App) randomHit(cfg *config.Config, channelID string) bool {
	cid, err := u64(channelID)
	if err != nil {
		return false
	}
	allowed := false
	for _, id := range cfg.RandomChannels {
		if id == cid {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	chance := cfg.RandomChance
	if chance <= 0 {
		chance = 0.02
	}
	if chance > 1 {
		chance = 1
	}
	return rand.Float64() < chance
}

func (a *App) startTyping(channel string) chan struct{} {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		a.dg.ChannelTyping(channel)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				a.dg.ChannelTyping(channel)
			}
		}
	}()
	return stop
}

func (a *App) interactionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type == discordgo.InteractionMessageComponent {
		a.handleComponent(s, i)
		return
	}
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := i.ApplicationCommandData()
	if data.Name == "ask" {
		a.handleAskCommand(s, i)
		return
	}
	if data.Name == "personality" {
		a.handlePersonalityCommand(s, i)
		return
	}
	if data.Name == "think" {
		a.handleThinkCommand(s, i)
		return
	}
	if data.Name == "chance" {
		a.handleChanceCommand(s, i)
		return
	}
	if data.Name == "gamebox" {
		a.handleGameboxCommand(s, i)
		return
	}
	if data.Name == "forget" {
		a.handleForgetCommand(s, i)
		return
	}
	if data.Name == "event" {
		a.handleEventCommand(s, i)
		return
	}
	if data.Name == "act" {
		a.handleActInteraction(s, i)
		return
	}
	if data.Name == "vc" {
		a.handleVcCommand(s, i)
		return
	}
	if data.Name == "tts" {
		a.handleTTSCommand(s, i)
		return
	}
	if data.Name == "terminalmode" {
		a.handleTerminalModeCommand(s, i)
		return
	}
	if data.Name == "term" {
		a.handleTermCommand(s, i)
		return
	}
	if data.Name != "ai" {
		return
	}
	var action, model string
	for _, o := range data.Options {
		switch o.Name {
		case "action":
			action = o.StringValue()
		case "name":
			model = o.StringValue()
		}
	}
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to control the AI.")
		return
	}

	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()

	switch action {
	case "status":
		respondPrivate(s, i, a.statusText())
	case "model":
		if strings.TrimSpace(model) == "" {
			respond(s, i, "usage: /ai model name:<model>")
			return
		}
		if !validModelName(model) {
			respond(s, i, "invalid model name.")
			return
		}
		a.mu.Lock()
		cfg.AIModel = model
		a.mu.Unlock()
		a.applyAIConfig()
		go a.warmModel()
		a.persist()
		respond(s, i, "AI model set to "+model+".")
	default:
		respond(s, i, "unknown action.")
	}
}

func (a *App) statusText() string {
	cfg := a.snapshot()
	st := a.eng.Status()
	ollama := "not reachable"
	model := cfg.AIModel
	if st.Running {
		ollama = "running (local)"
		installed := false
		for _, m := range st.Models {
			if m == cfg.AIModel || strings.HasPrefix(m, cfg.AIModel+":") {
				installed = true
				break
			}
		}
		if installed {
			model += " (installed)"
		} else {
			model += " (not installed)"
		}
	}
	return fmt.Sprintf("model: %s. Ollama is %s.", model, ollama)
}

func (a *App) handlePrefixAI(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to control the AI.", m.Reference())
		return
	}
	trimmed := strings.TrimSpace(m.Content)
	args := strings.Fields(trimmed[3:])
	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "status":
		if m.GuildID == "" {
			_, _ = s.ChannelMessageSendReply(m.ChannelID, a.statusText(), m.Reference())
			return
		}
		dm, err := s.UserChannelCreate(m.Author.ID)
		if err != nil {
			log.Printf("dm open: %v", err)
			return
		}
		_, _ = s.ChannelMessageSend(dm.ID, a.statusText())
	case "model":
		name := ""
		if strings.HasPrefix(action, "model:") {
			name = strings.TrimSpace(args[0][6:])
		}
		if name == "" && len(args) > 1 {
			name = strings.TrimSpace(args[1])
		}
		if name == "" {
			_, _ = s.ChannelMessageSendReply(m.ChannelID, "usage: /ai model <name>", m.Reference())
			return
		}
		if !validModelName(name) {
			_, _ = s.ChannelMessageSendReply(m.ChannelID, "invalid model name.", m.Reference())
			return
		}
		a.mu.Lock()
		a.cfg.AIModel = name
		a.mu.Unlock()
		a.applyAIConfig()
		go a.warmModel()
		a.persist()
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI model set to "+name+".", m.Reference())
	default:
		if strings.HasPrefix(action, "model:") {
			name := strings.TrimSpace(args[0][6:])
			if name == "" || !validModelName(name) {
				_, _ = s.ChannelMessageSendReply(m.ChannelID, "usage: /ai model <name>", m.Reference())
				return
			}
			a.mu.Lock()
			a.cfg.AIModel = name
			a.mu.Unlock()
			a.applyAIConfig()
			go a.warmModel()
			a.persist()
			_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI model set to "+name+".", m.Reference())
			return
		}
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "usage: /ai status | model <name>", m.Reference())
	}
}

func (a *App) setPersonality(text string) string {
	cfg := a.snapshot()
	text = strings.TrimSpace(text)
	if text == "" {
		return "current personality: " + cfg.AIPrompt
	}
	if strings.EqualFold(text, "reset") {
		text = config.DefaultPrompt(cfg.BotName)
	}
	a.mu.Lock()
	a.cfg.AIPrompt = text
	a.mu.Unlock()
	a.applyAIConfig()
	a.persist()
	return "personality updated."
}

func (a *App) handlePersonalityCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to change the personality.")
		return
	}
	var text string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "text" {
			text = o.StringValue()
		}
	}
	respondPrivate(s, i, a.setPersonality(text))
}

func (a *App) handlePrefixPersonality(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to change the personality.", m.Reference())
		return
	}
	text := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/personality"):])
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setPersonality(text), m.Reference())
}

func (a *App) setThink(text string) string {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return fmt.Sprintf("think is %v.", a.snapshot().AIThink)
	case "true", "on", "1", "yes":
		a.mu.Lock()
		a.cfg.AIThink = true
		a.mu.Unlock()
		a.applyAIConfig()
		a.persist()
		return "think enabled."
	case "false", "off", "0", "no":
		a.mu.Lock()
		a.cfg.AIThink = false
		a.mu.Unlock()
		a.applyAIConfig()
		a.persist()
		return "think disabled."
	default:
		return "usage: /think true|false"
	}
}

func (a *App) handleThinkCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to change thinking.")
		return
	}
	var value string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "value" {
			value = o.StringValue()
		}
	}
	respond(s, i, a.setThink(value))
}

func (a *App) handlePrefixThink(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to change thinking.", m.Reference())
		return
	}
	value := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/think"):])
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setThink(value), m.Reference())
}

func pctText(v float64) string {
	return strconv.FormatFloat(v*100, 'f', -1, 64) + "%"
}

func (a *App) setChance(text string) string {
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "%"))
	if text == "" {
		return "random reply chance is " + pctText(a.snapshot().RandomChance) + "."
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || v < 0 || v > 100 {
		return "usage: /chance <0-100>"
	}
	a.mu.Lock()
	a.cfg.RandomChance = v / 100
	a.mu.Unlock()
	a.persist()
	return "random reply chance set to " + pctText(v/100) + "."
}

func (a *App) handleChanceCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to change the chance.")
		return
	}
	var value string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "percent" {
			value = o.StringValue()
		}
	}
	respond(s, i, a.setChance(value))
}

func (a *App) handlePrefixChance(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to change the chance.", m.Reference())
		return
	}
	value := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/chance"):])
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setChance(value), m.Reference())
}

func (a *App) warmModel() {
	if err := a.eng.Warm(); err != nil {
		log.Printf("warm: %v", err)
		return
	}
	log.Printf("model warm")
}

func (a *App) loadAvatar(u *discordgo.User) {
	if a.gb == nil || u == nil {
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u.AvatarURL("256"))
	if err != nil {
		log.Printf("avatar fetch: %v", err)
		return
	}
	defer resp.Body.Close()
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		log.Printf("avatar decode: %v", err)
		return
	}
	a.gb.SetAvatar(img)
	log.Printf("gamebox avatar loaded")
}

func loadGamebox(cfgPath string) *gamebox.Renderer {
	exe, _ := os.Executable()
	paths := []string{}
	if p := os.Getenv("RALSEI_FONT"); p != "" {
		paths = append(paths, p)
	}
	paths = append(paths,
		filepath.Join(filepath.Dir(exe), "fonts", "DeterminationMono.ttf"),
		"/usr/local/share/ralsei/DeterminationMono.ttf",
		filepath.Join(filepath.Dir(cfgPath), "DeterminationMono.ttf"),
	)
	gb, err := gamebox.Load(paths...)
	if err != nil {
		log.Printf("gamebox unavailable: %v", err)
		return nil
	}
	return gb
}

func (a *App) setGamebox(text string) string {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return fmt.Sprintf("gamebox is %v.", a.snapshot().Gamebox)
	case "on", "true", "1", "yes":
		a.mu.Lock()
		a.cfg.Gamebox = true
		a.mu.Unlock()
		a.persist()
		if a.gb == nil {
			return "gamebox enabled, but the font is not loaded, replies stay text."
		}
		return "gamebox enabled."
	case "off", "false", "0", "no":
		a.mu.Lock()
		a.cfg.Gamebox = false
		a.mu.Unlock()
		a.persist()
		return "gamebox disabled."
	default:
		return "usage: /gamebox on|off"
	}
}

func (a *App) handleGameboxCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to change gamebox.")
		return
	}
	var value string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "value" {
			value = o.StringValue()
		}
	}
	respond(s, i, a.setGamebox(value))
}

func (a *App) handlePrefixGamebox(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to change gamebox.", m.Reference())
		return
	}
	value := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/gamebox"):])
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setGamebox(value), m.Reference())
}

func (a *App) handleForgetCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to clear the memory.")
		return
	}
	scope := "channel"
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "scope" {
			scope = o.StringValue()
		}
	}
	if scope == "all" {
		a.eng.ClearAll()
		respond(s, i, "all AI memory cleared.")
		return
	}
	if channel, err := u64(i.ChannelID); err == nil {
		a.eng.Clear(channel)
	}
	respond(s, i, "AI memory cleared for this channel.")
}

func (a *App) handlePrefixForget(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to clear the memory.", m.Reference())
		return
	}
	arg := strings.ToLower(strings.TrimSpace(strings.TrimSpace(m.Content)[len("/forget"):]))
	if arg == "all" {
		a.eng.ClearAll()
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "all AI memory cleared.", m.Reference())
		return
	}
	if channel, err := u64(m.ChannelID); err == nil {
		a.eng.Clear(channel)
	}
	_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI memory cleared for this channel.", m.Reference())
}

func (a *App) setEvent(text string) string {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return fmt.Sprintf("mini events are %v.", a.snapshot().EventsEnabled)
	case "on", "true", "1", "yes":
		a.mu.Lock()
		a.cfg.EventsEnabled = true
		a.mu.Unlock()
		a.persist()
		return "mini events enabled."
	case "off", "false", "0", "no":
		a.mu.Lock()
		a.cfg.EventsEnabled = false
		a.mu.Unlock()
		a.persist()
		return "mini events disabled."
	default:
		return "usage: /event on|off"
	}
}

func (a *App) handleEventCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var value string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "value" {
			value = o.StringValue()
		}
	}
	respond(s, i, a.setEvent(value))
}

func (a *App) handlePrefixEvent(s *discordgo.Session, m *discordgo.MessageCreate) {
	value := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/event"):])
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setEvent(value), m.Reference())
}

func (a *App) soundWorker() {
	for snd := range a.soundQ {
		a.voiceMu.Lock()
		vc := a.voice
		a.voiceMu.Unlock()
		if vc == nil || vc.Status != discordgo.VoiceConnectionStatusReady {
			continue
		}
		for _, frame := range snd.Frames {
			select {
			case vc.OpusSend <- frame:
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (a *App) playSound(name string) {
	snd := audio.Get(name)
	if len(snd.Frames) == 0 {
		return
	}
	select {
	case a.soundQ <- snd:
	default:
	}
}

var parenRe = regexp.MustCompile(`\([^)]*\)`)

func speechText(s string) string {
	s = parenRe.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 400 {
		s = string(r[:400])
	}
	return s
}

func (a *App) speak(text string) {
	cfg := a.snapshot()
	if !cfg.TTSEnabled {
		return
	}
	spoken := speechText(text)
	if spoken == "" {
		return
	}
	a.voiceMu.Lock()
	connected := a.voice != nil && a.voice.Status == discordgo.VoiceConnectionStatusReady
	a.voiceMu.Unlock()
	if !connected {
		return
	}
	go func() {
		pcm, rate, err := audio.TTSPCM(spoken, cfg.TTSBinary, cfg.TTSVoice)
		if err != nil {
			log.Printf("tts: %v", err)
			return
		}
		snd := audio.FromPCM(pcm, rate)
		select {
		case a.soundQ <- snd:
		default:
		}
	}()
}

func (a *App) setTTS(text string) string {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
		return fmt.Sprintf("tts is %v.", a.snapshot().TTSEnabled)
	case "on", "true", "1", "yes":
		a.mu.Lock()
		a.cfg.TTSEnabled = true
		a.mu.Unlock()
		a.persist()
		return "tts enabled."
	case "off", "false", "0", "no":
		a.mu.Lock()
		a.cfg.TTSEnabled = false
		a.mu.Unlock()
		a.persist()
		return "tts disabled."
	default:
		return "usage: /tts on|off"
	}
}

func (a *App) handleTTSCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to change tts.")
		return
	}
	var value string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "value" {
			value = o.StringValue()
		}
	}
	respond(s, i, a.setTTS(value))
}

func (a *App) handlePrefixTTS(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to change tts.", m.Reference())
		return
	}
	value := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/tts"):])
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setTTS(value), m.Reference())
}

const terminalSystem = `You are Ralsei, typing into a bash terminal inside your Artix Linux VM. Turn the user's request into exactly one bash command. Reply with the command only. No explanations, no markdown, no code fences, no backticks. Choose the closest safe command if the request is ambiguous.`

func cleanCommand(s string) string {
	s = strings.ReplaceAll(s, "```bash", "")
	s = strings.ReplaceAll(s, "```sh", "")
	s = strings.ReplaceAll(s, "```", "")
	s = strings.ReplaceAll(s, "`", "")
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimPrefix(ln, "$ ")
		if ln == "" {
			continue
		}
		r := []rune(ln)
		if len(r) > 400 {
			ln = string(r[:400])
		}
		return ln
	}
	return ""
}

func (a *App) terminalOn(channelID string) bool {
	a.termMu.Lock()
	defer a.termMu.Unlock()
	return a.termMode[channelID]
}

func (a *App) presentTerminal(channelID, command, output string, exit int) {
	if a.term == nil {
		return
	}
	a.voiceMu.Lock()
	vcCh := a.voiceCh
	a.voiceMu.Unlock()
	if vcCh != "" {
		channelID = vcCh
	}
	if exit != 0 {
		output += fmt.Sprintf("\n[exit %d]", exit)
	}
	if strings.TrimSpace(output) == "" {
		output = " "
	}
	img, err := a.term.Render("artix VM - ralsei@artix", command, output)
	if err != nil {
		log.Printf("terminal render: %v", err)
		return
	}
	a.termMu.Lock()
	msgID := a.termMsg[channelID]
	a.termMu.Unlock()
	if msgID != "" {
		empty := ""
		att := []*discordgo.MessageAttachment{}
		_, err := a.dg.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:     channelID,
			ID:          msgID,
			Content:     &empty,
			Attachments: &att,
			Files:       []*discordgo.File{{Name: "terminal.png", Reader: bytes.NewReader(img)}},
		})
		if err == nil {
			return
		}
		log.Printf("terminal edit: %v", err)
	}
	msg, err := a.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Files: []*discordgo.File{{Name: "terminal.png", Reader: bytes.NewReader(img)}},
	})
	if err != nil {
		log.Printf("terminal send: %v", err)
		return
	}
	a.termMu.Lock()
	a.termMsg[channelID] = msg.ID
	a.termMu.Unlock()
}

func (a *App) runVMCommand(command string) (string, int) {
	out, code, err := a.vmc.Exec(command, 60*time.Second)
	if err != nil {
		return err.Error(), -1
	}
	return out, code
}

func (a *App) runTerminalPrompt(channelID string, userID uint64, text string) {
	cmd, err := a.eng.Complete(terminalSystem, text, 0.2)
	if err != nil {
		log.Printf("terminal ai: %v", err)
		return
	}
	cmd = cleanCommand(cmd)
	if cmd == "" {
		return
	}
	log.Printf("terminal command from %d: %s", userID, cmd)
	a.presentTerminal(channelID, cmd, "(running...)", 0)
	out, code := a.runVMCommand(cmd)
	a.presentTerminal(channelID, cmd, out, code)
	a.speak(out)
}

func (a *App) setTerminalMode(channelID string, on bool) string {
	if on && a.term == nil {
		return "terminal renderer is not available."
	}
	a.termMu.Lock()
	a.termMode[channelID] = on
	a.termMu.Unlock()
	if on {
		a.presentTerminal(channelID, "", "Terminal mode enabled.\nMessages in this channel become commands and run inside the artix VM.", 0)
		a.speak("Terminal mode is ready.")
		return "terminal mode enabled."
	}
	return "terminal mode disabled."
}

func (a *App) handleTerminalModeCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to change terminal mode.")
		return
	}
	value := "on"
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "value" {
			value = o.StringValue()
		}
	}
	on := value != "off"
	respond(s, i, a.setTerminalMode(i.ChannelID, on))
}

func (a *App) handlePrefixTerminalMode(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to change terminal mode.", m.Reference())
		return
	}
	arg := strings.ToLower(strings.TrimSpace(strings.TrimSpace(m.Content)[len("/terminalmode"):]))
	on := arg != "off" && arg != "false" && arg != "0"
	_, _ = s.ChannelMessageSendReply(m.ChannelID, a.setTerminalMode(m.ChannelID, on), m.Reference())
}

func (a *App) handleTermCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || !a.isElevated(uid) {
		respond(s, i, "you are not allowed to run terminal commands.")
		return
	}
	cmd := ""
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "command" {
			cmd = o.StringValue()
		}
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		respond(s, i, "usage: /term command:<command>")
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("term defer: %v", err)
		return
	}
	go func() {
		out, code := a.runVMCommand(cmd)
		if code != 0 {
			out += fmt.Sprintf("\n[exit %d]", code)
		}
		if a.term == nil {
			msg := "terminal renderer unavailable."
			_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
			return
		}
		img, err := a.term.Render("artix VM - ralsei@artix", cmd, out)
		if err != nil {
			msg := "render failed."
			_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
			return
		}
		att := []*discordgo.MessageAttachment{}
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Attachments: &att,
			Files:       []*discordgo.File{{Name: "terminal.png", Reader: bytes.NewReader(img)}},
		})
	}()
}

func (a *App) handlePrefixTerm(s *discordgo.Session, m *discordgo.MessageCreate) {
	uid, err := u64(m.Author.ID)
	if err != nil || !a.isElevated(uid) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "you are not allowed to run terminal commands.", m.Reference())
		return
	}
	cmd := strings.TrimSpace(strings.TrimSpace(m.Content)[len("/term"):])
	if cmd == "" {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "usage: /term <command>", m.Reference())
		return
	}
	go func() {
		out, code := a.runVMCommand(cmd)
		a.presentTerminal(m.ChannelID, cmd, out, code)
	}()
}

func (a *App) joinVoice(guildID, channelID string) error {
	a.voiceMu.Lock()
	if a.voice != nil {
		if a.voiceCh == channelID {
			a.voiceMu.Unlock()
			return nil
		}
		a.voice.Disconnect(context.Background())
		a.voice = nil
		a.voiceCh = ""
	}
	a.voiceMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	vc, err := a.dg.ChannelVoiceJoin(ctx, guildID, channelID, false, true)
	if err != nil {
		return err
	}
	if err := vc.WaitForDAVEReady(ctx); err != nil {
		log.Printf("dave ready: %v", err)
	}

	a.voiceMu.Lock()
	a.voice = vc
	a.voiceCh = channelID
	a.voiceMu.Unlock()
	log.Printf("voice joined %s", channelID)
	return nil
}

func (a *App) leaveVoice() {
	a.voiceMu.Lock()
	defer a.voiceMu.Unlock()
	if a.voice != nil {
		a.voice.Disconnect(context.Background())
		a.voice = nil
		a.voiceCh = ""
	}
}

func (a *App) autoJoinVoice(i *discordgo.InteractionCreate) {
	if i.GuildID == "" || i.Member == nil || i.Member.User == nil {
		return
	}
	vs, err := a.dg.State.VoiceState(i.GuildID, i.Member.User.ID)
	if err != nil || vs.ChannelID == "" {
		return
	}
	if err := a.joinVoice(i.GuildID, vs.ChannelID); err != nil {
		log.Printf("voice join: %v", err)
	}
}

func (a *App) handleCome(s *discordgo.Session, m *discordgo.MessageCreate) {
	vs, err := s.State.VoiceState(m.GuildID, m.Author.ID)
	if err != nil || vs.ChannelID == "" {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "Um... I don't see you in a voice channel.", m.Reference())
		return
	}
	_, _ = s.ChannelMessageSendReply(m.ChannelID, "Okay! I'm coming.", m.Reference())
	go func() {
		if err := a.joinVoice(m.GuildID, vs.ChannelID); err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, "I could not join voice: "+err.Error())
			return
		}
		a.speak("I'm here!")
	}()
}

func (a *App) handleVoiceLeave(s *discordgo.Session, m *discordgo.MessageCreate) {
	a.leaveVoice()
	_, _ = s.ChannelMessageSendReply(m.ChannelID, "Okay, I will go now. See you soon!", m.Reference())
}

func (a *App) dmUser(s *discordgo.Session, userID, text string) {
	ch, err := s.UserChannelCreate(userID)
	if err != nil {
		log.Printf("dm open: %v", err)
		return
	}
	if _, err := s.ChannelMessageSend(ch.ID, text); err != nil {
		log.Printf("dm send: %v", err)
	}
}

func (a *App) handleVcCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" {
		respondPrivate(s, i, "voice only works in servers.")
		return
	}
	action := "join"
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "action" {
			action = o.StringValue()
		}
	}
	if action == "leave" {
		a.leaveVoice()
		respondPrivate(s, i, "left the voice channel.")
		return
	}
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	vs, err := s.State.VoiceState(i.GuildID, caller)
	if err != nil || vs.ChannelID == "" {
		respondPrivate(s, i, "join a voice channel first, then run /vc join.")
		return
	}
	if action == "terminalmode" {
		uid, err := u64(caller)
		if err != nil || !a.isElevated(uid) {
			respondPrivate(s, i, "you are not allowed to share the terminal.")
			return
		}
		a.termMu.Lock()
		a.termMode[i.ChannelID] = true
		a.termMu.Unlock()
		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
		}); err != nil {
			log.Printf("vc defer: %v", err)
			return
		}
		go func() {
			if err := a.joinVoice(i.GuildID, vs.ChannelID); err != nil {
				msg := "could not join voice: " + err.Error()
				_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
				return
			}
			a.presentTerminal(vs.ChannelID, "", "Terminal mode is on.\nMessages in this text channel become commands in the artix VM. I will share the screen here.", 0)
			a.speak("Terminal mode is ready. I will share my screen here.")
			msg := "terminal mode is on, sharing the terminal in the voice channel chat."
			_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
		}()
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		log.Printf("vc defer: %v", err)
		return
	}
	go func() {
		if err := a.joinVoice(i.GuildID, vs.ChannelID); err != nil {
			msg := "could not join voice: " + err.Error()
			_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
			return
		}
		msg := "joined <#" + vs.ChannelID + ">."
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
	}()
}

func (a *App) handlePrefixVc(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.GuildID == "" {
		return
	}
	arg := strings.ToLower(strings.TrimSpace(strings.TrimSpace(m.Content)[len("/vc"):]))
	if arg == "leave" || arg == "stop" {
		a.leaveVoice()
		a.dmUser(s, m.Author.ID, "left the voice channel.")
		return
	}
	vs, err := s.State.VoiceState(m.GuildID, m.Author.ID)
	if err != nil || vs.ChannelID == "" {
		a.dmUser(s, m.Author.ID, "join a voice channel first.")
		return
	}
	a.dmUser(s, m.Author.ID, "joining <#"+vs.ChannelID+">...")
	go func() {
		if err := a.joinVoice(m.GuildID, vs.ChannelID); err != nil {
			a.dmUser(s, m.Author.ID, "could not join voice: "+err.Error())
			return
		}
		a.dmUser(s, m.Author.ID, "joined <#"+vs.ChannelID+">.")
	}()
}

func loadTerm(path string) *term.Renderer {
	r, err := term.Load(path)
	if err != nil {
		log.Printf("terminal renderer unavailable: %v", err)
		return nil
	}
	return r
}

func (a *App) watchConfig(path, token string) {
	lastMod := time.Time{}
	lastSize := int64(-1)
	if fi, err := os.Stat(path); err == nil {
		lastMod = fi.ModTime()
		lastSize = fi.Size()
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if fi.ModTime().Equal(lastMod) && fi.Size() == lastSize {
			continue
		}
		lastMod = fi.ModTime()
		lastSize = fi.Size()
		cfg, err := config.Load(path)
		if err != nil {
			log.Printf("config reload: %v", err)
			continue
		}
		if cfg.DiscordToken != token {
			log.Printf("config reload: discord_token changed, restart required to apply")
		}
		a.mu.Lock()
		a.cfg = cfg
		a.mu.Unlock()
		a.applyAIConfig()
		log.Printf("config reloaded from %s", path)
	}
}

func (a *App) sendHelp(s *discordgo.Session, m *discordgo.MessageCreate) {
	text := "mention me, write my name, reply to me or use /ask <question> — I answer with local AI.\n" +
		"/ask <question> — ask me\n" +
		"/ai status|model <name> — owner/admin controls\n" +
		"/personality <text> — owner/admin controls, reset restores default\n" +
		"/think true|false — owner/admin controls\n" +
		"/chance <0-100> — owner/admin controls, random reply percent\n" +
		"/gamebox on|off — owner/admin controls, Deltarune image replies\n" +
		"/forget [all] — owner/admin controls, clear AI memory\n" +
		"/event on|off — anyone, mini events\n" +
		"/act — anyone, Deltarune battle: FIGHT (timing), ACT (mercy), MERCY (spare), FLEE (deletes message)\n" +
		"/vc join|leave — join your voice channel for battle sounds\n" +
		"say \"ralsei come here\" to make him join your voice channel, \"ralsei leave\" to make him go\n" +
		"/tts on|off — owner/admin controls, speak replies in voice\n" +
		"/terminalmode on|off — owner/admin controls, AI terminal in the artix VM\n" +
		"/term <command> — owner/admin controls, run a command in the artix VM\n" +
		"config: ~/.config/ralsei/config.jsonc — edits apply live"
	_, _ = s.ChannelMessageSendReply(m.ChannelID, text, m.Reference())
}

func (a *App) handleAskCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var text string
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == "text" {
			text = o.StringValue()
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		respond(s, i, "usage: /ask text:<your question>")
		return
	}
	cfg := a.snapshot()
	if !enabled(cfg) {
		respond(s, i, "AI is off right now.")
		return
	}
	caller := ""
	if i.Member != nil && i.Member.User != nil {
		caller = i.Member.User.ID
	} else if i.User != nil {
		caller = i.User.ID
	}
	uid, err := u64(caller)
	if err != nil || a.isBlocked(uid) {
		respond(s, i, "you are not allowed to use the AI.")
		return
	}
	historyKey, err := u64(i.ChannelID)
	if err != nil {
		respond(s, i, "bad channel id.")
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		log.Printf("defer: %v", err)
		return
	}
	display := ""
	if i.Member != nil && i.Member.User != nil {
		display = i.Member.User.GlobalName
		if display == "" {
			display = i.Member.User.Username
		}
	} else if i.User != nil {
		display = i.User.GlobalName
		if display == "" {
			display = i.User.Username
		}
	}
	reply, err := a.eng.Ask(historyKey, ai.SpeakerTag(display), text)
	if err != nil {
		log.Printf("ai: %v", err)
		msg := "AI error, try again later."
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg})
		return
	}
	a.speak(reply)
	if cfg.Gamebox && a.gb != nil {
		if img, err := a.gb.Render(reply); err == nil {
			if err := a.editInteractionImage(s, i, img); err == nil {
				return
			} else {
				log.Printf("gamebox edit: %v", err)
			}
		}
	}
	chunks := ai.ChunkReply(reply)
	first := chunks[0]
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &first}); err != nil {
		log.Printf("edit: %v", err)
	}
	for _, c := range chunks[1:] {
		if _, err := s.ChannelMessageSend(i.ChannelID, c); err != nil {
			log.Printf("send: %v", err)
			return
		}
	}
}

func (a *App) applyAIConfig() {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	a.eng.Update(ai.Config{
		Host:        cfg.OllamaHost,
		Model:       cfg.AIModel,
		System:      cfg.AIPrompt,
		Temperature: cfg.AITemperature,
		Think:       cfg.AIThink,
	})
}

func (a *App) persist() {
	a.mu.Lock()
	cfgCopy := *a.cfg
	path := a.cfgPath
	a.mu.Unlock()
	if err := config.Save(&cfgCopy, path); err != nil {
		log.Printf("persist config: %v", err)
	}
}

func validModelName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	if strings.Contains(s, " ") || strings.ContainsAny(s, "/\\\"'`;,") {
		return false
	}
	return true
}

func respondPrivate(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
	})
	if err != nil {
		log.Printf("respond: %v", err)
	}
}

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	if err != nil {
		log.Printf("respond: %v", err)
	}
}
