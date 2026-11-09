package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/Matko802/ralsei/internal/ai"
	"github.com/Matko802/ralsei/internal/config"
)

type App struct {
	mu      sync.Mutex
	cfg     *config.Config
	cfgPath string
	eng     *ai.Engine
	dg      *discordgo.Session
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

	app := &App{cfg: cfg, cfgPath: path}
	app.eng = ai.New(ai.Config{
		Host:        cfg.OllamaHost,
		Model:       cfg.AIModel,
		System:      cfg.AIPrompt,
		Temperature: cfg.AITemperature,
		Think:       cfg.AIThink,
	})

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
		discordgo.IntentsMessageContent)

	app.dg = dg
	if err := dg.Open(); err != nil {
		log.Fatalf("gateway: %v", err)
	}

	log.Println("ralsei is running, press ctrl+c to exit")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	dg.Close()
}

func (a *App) ready(s *discordgo.Session, e *discordgo.Ready) {
	meName := a.botName()
	log.Printf("logged in as %s (%s)", e.User.String(), meName)

	cmds := []*discordgo.ApplicationCommand{
		{
			Name:        "ai",
			Description: "control the AI",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "action",
					Description: "what to do with the AI",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "status", Value: "status"},
						{Name: "on", Value: "on"},
						{Name: "off", Value: "off"},
						{Name: "clear", Value: "clear"},
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
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "text",
					Description: "your question",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
	}
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", cmds); err != nil {
		log.Printf("command register: %v", err)
	}
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

	cfg := a.snapshot()
	id := s.State.User.ID
	name := a.botName()
	content := m.Content
	lower := strings.ToLower(content)
	trimmed := strings.TrimSpace(lower)

	isAsk := trimmed == "/ask" || strings.HasPrefix(trimmed, "/ask ")
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
	case repliedToBot || mentioned:
		prompt = stripName(stripIDTag(content, id), name)
		if strings.TrimSpace(prompt) == "" {
			prompt = "hello"
		}
	default:
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
		if isAsk || mentioned || repliedToBot {
			_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI is off — the owner runs /ai on to enable me.", m.Reference())
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

func (a *App) askAndReply(s *discordgo.Session, channelID string, ref *discordgo.MessageReference, historyKey uint64, speaker, prompt string) {
	stopTyping := a.startTyping(channelID)
	defer close(stopTyping)

	reply, err := a.eng.Ask(historyKey, speaker, prompt)
	if err != nil {
		log.Printf("ai: %v", err)
		_, _ = s.ChannelMessageSendReply(channelID, "AI error, try again later.", ref)
		return
	}
	chunks := ai.ChunkReply(reply)
	for i, c := range chunks {
		if i == 0 {
			_, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
				Content:   c,
				Reference: ref,
			})
		} else {
			_, err = s.ChannelMessageSend(channelID, c)
		}
		if err != nil {
			log.Printf("send: %v", err)
			return
		}
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
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := i.ApplicationCommandData()
	if data.Name == "ask" {
		a.handleAskCommand(s, i)
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
		state := "off"
		if cfg.AIEnabled {
			state = "on"
		}
		st := a.eng.Status()
		ollama := "not reachable"
		installed := ""
		if st.Running {
			ollama = "running (local)"
			found := false
			for _, m := range st.Models {
				if m == cfg.AIModel || strings.HasPrefix(m, cfg.AIModel+":") {
					found = true
					break
				}
			}
			if found {
				installed = ", installed"
			} else {
				installed = ", not installed"
			}
		}
		respond(s, i, fmt.Sprintf("AI is %s. Ollama is %s. model: %s%s. think: %v.",
			state, ollama, cfg.AIModel, installed, cfg.AIThink))
	case "on":
		a.mu.Lock()
		cfg.AIEnabled = true
		a.mu.Unlock()
		a.persist()
		respond(s, i, "AI enabled.")
	case "off":
		a.mu.Lock()
		cfg.AIEnabled = false
		a.mu.Unlock()
		a.persist()
		respond(s, i, "AI disabled.")
	case "clear":
		a.eng.ClearAll()
		respond(s, i, "AI history cleared.")
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
		a.persist()
		respond(s, i, "AI model set to "+model+".")
	default:
		respond(s, i, "unknown action.")
	}
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
		respond(s, i, "AI is off — the owner runs /ai on to enable me.")
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

func respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg},
	})
	if err != nil {
		log.Printf("respond: %v", err)
	}
}
