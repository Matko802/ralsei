package main

import (
	"bytes"
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Matko802/ralsei/internal/ai"
	"github.com/Matko802/ralsei/internal/config"
	"github.com/Matko802/ralsei/internal/events"
	"github.com/Matko802/ralsei/internal/gamebox"
)

type App struct {
	mu        sync.Mutex
	eventMu   sync.Mutex
	lastEvent map[string]time.Time
	cfg       *config.Config
	cfgPath   string
	eng       *ai.Engine
	gb        *gamebox.Renderer
	dg        *discordgo.Session
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

	app := &App{cfg: cfg, cfgPath: path, lastEvent: make(map[string]time.Time)}
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
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageTyping |
		discordgo.IntentsMessageContent)

	app.dg = dg
	if err := dg.Open(); err != nil {
		log.Fatalf("gateway: %v", err)
	}
	go app.watchConfig(path, cfg.DiscordToken)

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
		{Label: "Compliment", Style: "success", Text: "You tell Ralsei he is doing great. He looks away, ears pink. That... means a lot, thank you."},
		{Label: "Hug", Style: "primary", Text: "You hug Ralsei. He goes very still, then hugs back. Oh! Okay. This is nice."},
		{Label: "Flirt", Style: "danger", Text: "You flirt with Ralsei. He turns bright red and hides behind his scarf. I, um. I do not know what to say to that!"},
		{Label: "Check", Style: "secondary", Text: "RALSEI - ATK 3 DEF 3. A gentle prince who believes in you. His scarf is very soft."},
	}
}

func (a *App) actComponents() []discordgo.MessageComponent {
	actions := actActions()
	row := make([]discordgo.MessageComponent, 0, len(actions))
	for i, act := range actions {
		row = append(row, discordgo.Button{
			Label:    act.Label,
			Style:    buttonStyle(act.Style),
			CustomID: fmt.Sprintf("act:%d", i),
		})
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: row}}
}

func (a *App) sendAct(s *discordgo.Session, channelID string, ref *discordgo.MessageReference) {
	msg := &discordgo.MessageSend{
		Components: a.actComponents(),
		Reference:  ref,
	}
	cfg := a.snapshot()
	if cfg.Gamebox && a.gb != nil {
		if img, err := a.gb.Render("ACT\nRalsei waits for your move."); err == nil {
			msg.Files = []*discordgo.File{{Name: "ralsei.png", Reader: bytes.NewReader(img)}}
		}
	}
	if len(msg.Files) == 0 {
		msg.Content = "**ACT**\nRalsei waits for your move."
	}
	if _, err := s.ChannelMessageSendComplex(channelID, msg); err != nil {
		log.Printf("act send: %v", err)
	}
}

func (a *App) handleActCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	a.sendAct(s, m.ChannelID, m.Reference())
}

func (a *App) handleActInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	a.respondGamebox(s, i, "ACT\nRalsei waits for your move.", a.actComponents())
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
		a.respondGamebox(s, i, actions[idx].Text, a.actComponents())
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
		"/act — anyone, ACT on Ralsei\n" +
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
