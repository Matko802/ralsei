package main

import (
	"flag"
	"fmt"
	"log"
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

	id := s.State.User.ID
	lower := strings.ToLower(m.Content)
	name := a.botName()
	mentioned := strings.Contains(m.Content, "<@"+id+">") ||
		strings.Contains(m.Content, "<@!"+id+">") ||
		(name != "" && strings.Contains(lower, name))
	if !mentioned {
		return
	}

	prompt := stripName(stripIDTag(m.Content, id), name)
	if strings.TrimSpace(prompt) == "" {
		prompt = "hello"
	}

	cfg := a.snapshot()
	if !enabled(cfg) {
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI is off — the owner runs /ai on to enable me.", m.Reference())
		return
	}

	stopTyping := a.startTyping(m.ChannelID)
	defer close(stopTyping)

	display := m.Author.GlobalName
	if display == "" {
		display = m.Author.Username
	}
	reply, err := a.eng.Ask(uid, ai.SpeakerTag(display), prompt)
	if err != nil {
		log.Printf("ai: %v", err)
		_, _ = s.ChannelMessageSendReply(m.ChannelID, "AI error, try again later.", m.Reference())
		return
	}
	chunks := ai.ChunkReply(reply)
	for i, c := range chunks {
		if i == 0 {
			_, err = s.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{
				Content:   c,
				Reference: m.Reference(),
			})
		} else {
			_, err = s.ChannelMessageSend(m.ChannelID, c)
		}
		if err != nil {
			log.Printf("send: %v", err)
			return
		}
	}
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
		respond(s, i, fmt.Sprintf("AI is %s, model %s, think %v, host %s.",
			state, cfg.AIModel, cfg.AIThink, cfg.OllamaHost))
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
