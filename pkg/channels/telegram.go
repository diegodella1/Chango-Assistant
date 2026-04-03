package channels

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/voice"
)

// Pre-compiled regex patterns (avoid re-compiling on every message)
var (
	reCodeBlock    = regexp.MustCompile("```[\\w]*\\n?[\\s\\S]*?```")
	reInlineCode   = regexp.MustCompile("`[^`]+`")
	reBold         = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reUnderBold    = regexp.MustCompile(`__(.+?)__`)
	reItalic       = regexp.MustCompile(`_([^_]+)_`)
	reStrike       = regexp.MustCompile(`~~(.+?)~~`)
	reLink         = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reHeading      = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reBlockquote   = regexp.MustCompile(`(?m)^>\s*(.*)$`)
	reListItem     = regexp.MustCompile(`(?m)^[-*]\s+`)
	reCodeBlockCap = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
)

type TelegramChannel struct {
	*BaseChannel
	bot          *telego.Bot
	config       config.TelegramConfig
	appConfig    *config.Config
	configPath   string // path to config.json for persistence
	chatIDs      map[string]int64
	transcriber  *voice.GroqTranscriber
	placeholders sync.Map // chatID -> messageID
	stopThinking sync.Map // chatID -> thinkingCancel
	voiceInput   sync.Map // chatID -> bool (true if last input was voice/audio)
	adminUserID  string   // admin user ID for /join, /leave commands
}

var defaultModels = []string{
	// Codex models (ChatGPT Plus via OAuth)
	"gpt-5.2-codex",
	"gpt-5.3-codex",
	"gpt-5-codex",
	"gpt-5.1-codex-max",
	"gpt-5",
}

// providerModels maps provider names to their available models for the keyboard menu.
var providerModels = map[string][]string{
	"openai":     {"gpt-5", "gpt-5-codex", "gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.1-codex-max"},
	"openrouter": {"anthropic/claude-sonnet-4", "openai/gpt-5", "google/gemini-2.5-flash", "deepseek/deepseek-chat-v3-0324", "meta-llama/llama-4-maverick"},
	"groq":       {"llama-3.3-70b-versatile", "llama-3.1-8b-instant", "mixtral-8x7b-32768"},
	"anthropic":  {"claude-sonnet-4-20250514", "claude-opus-4-20250514"},
	"deepseek":   {"deepseek-chat", "deepseek-reasoner"},
	"gemini":     {"gemini-2.5-flash", "gemini-2.5-pro"},
	"llamacpp":   {"gemma-4-E2B-it", "local"},
}

type thinkingCancel struct {
	fn context.CancelFunc
}

func (c *thinkingCancel) Cancel() {
	if c != nil && c.fn != nil {
		c.fn()
	}
}

func NewTelegramChannel(cfg config.TelegramConfig, bus *bus.MessageBus, appConfig *config.Config) (*TelegramChannel, error) {
	var opts []telego.BotOption

	if cfg.Proxy != "" {
		proxyURL, parseErr := url.Parse(cfg.Proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", cfg.Proxy, parseErr)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	}

	bot, err := telego.NewBot(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	base := NewBaseChannel("telegram", cfg, bus, cfg.AllowFrom)

	return &TelegramChannel{
		BaseChannel:  base,
		bot:          bot,
		config:       cfg,
		appConfig:    appConfig,
		chatIDs:      make(map[string]int64),
		transcriber:  nil,
		placeholders: sync.Map{},
		stopThinking: sync.Map{},
	}, nil
}

func (c *TelegramChannel) SetTranscriber(transcriber *voice.GroqTranscriber) {
	c.transcriber = transcriber
}

func (c *TelegramChannel) SetConfigPath(path string) {
	c.configPath = path
}

func (c *TelegramChannel) SetAdminUserID(id string) {
	c.adminUserID = id
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (polling mode)...")

	// Register bot commands so Telegram shows them in the "/" menu
	_ = c.bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: []telego.BotCommand{
			{Command: "help", Description: "Qué puedo hacer"},
			{Command: "status", Description: "Estado del sistema"},
			{Command: "model", Description: "Cambiar modelo LLM"},
			{Command: "provider", Description: "Cambiar proveedor"},
		},
	})

	if err := c.startPolling(ctx); err != nil {
		return err
	}

	return nil
}

func (c *TelegramChannel) startPolling(ctx context.Context) error {
	updates, err := c.bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout:        30,
		AllowedUpdates: []string{"message", "callback_query"},
	})
	if err != nil {
		return fmt.Errorf("failed to start long polling: %w", err)
	}

	c.setRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]interface{}{
		"username": c.bot.Username(),
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					logger.WarnC("telegram", "Updates channel closed, attempting reconnect...")
					c.setRunning(false)
					c.reconnectPolling(ctx)
					return
				}
				if update.CallbackQuery != nil {
					c.handleCallbackQuery(ctx, update)
				} else if update.Message != nil {
					c.handleMessage(ctx, update)
				}
			}
		}
	}()

	return nil
}

func (c *TelegramChannel) reconnectPolling(ctx context.Context) {
	backoff := 2 * time.Second
	maxBackoff := 5 * time.Minute
	conflictRetries := 0
	const maxConflictRetries = 3

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		logger.InfoCF("telegram", "Reconnecting polling", map[string]interface{}{
			"backoff": backoff.String(),
		})

		if err := c.startPolling(ctx); err != nil {
			errStr := err.Error()
			isConflict := strings.Contains(errStr, "409") || strings.Contains(errStr, "Conflict")

			if isConflict {
				conflictRetries++
				logger.WarnCF("telegram", "Another bot instance is polling this token — stopping this instance", map[string]interface{}{
					"attempt": conflictRetries,
					"max":     maxConflictRetries,
				})
				if conflictRetries >= maxConflictRetries {
					logger.ErrorCF("telegram", "Max 409 conflict retries reached, stopping polling permanently", map[string]interface{}{
						"retries": conflictRetries,
					})
					return
				}
				backoff = 30 * time.Second
				continue
			}

			// Reset conflict counter on non-conflict errors
			conflictRetries = 0

			logger.ErrorCF("telegram", "Reconnect failed", map[string]interface{}{
				"error": errStr,
			})
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		return
	}
}

func (c *TelegramChannel) Stop(ctx context.Context) error {
	logger.InfoC("telegram", "Stopping Telegram bot...")
	c.setRunning(false)
	return nil
}

// ttsMaxChars is the max plain-text length for a response to be sent as voice.
const ttsMaxChars = 300

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("telegram bot not running")
	}

	chatID, err := parseChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID: %w", err)
	}

	// Stop thinking animation
	if stop, ok := c.stopThinking.Load(msg.ChatID); ok {
		if cf, ok := stop.(*thinkingCancel); ok && cf != nil {
			cf.Cancel()
		}
		c.stopThinking.Delete(msg.ChatID)
	}

	// Delete placeholder before sending voice or text (atomic to avoid race)
	if pID, ok := c.placeholders.LoadAndDelete(msg.ChatID); ok {
		_ = c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
			ChatID:    tu.ID(chatID),
			MessageID: pID.(int),
		})
	}

	// Send media as photos or documents based on file type
	if len(msg.Media) > 0 {
		for _, mediaURL := range msg.Media {
			caption := ""
			if msg.Content != "" {
				caption = markdownToTelegramHTML(msg.Content)
			}

			if isImageURL(mediaURL) {
				sent, photoErr := c.sendTelegramPhoto(ctx, chatID, mediaURL, caption)
				if photoErr != nil {
					logger.ErrorCF("telegram", "Failed to send photo, falling back to text", map[string]interface{}{
						"error": photoErr.Error(),
						"url":   mediaURL,
					})
					break // fall through to text send below
				}
				if sent {
					return nil
				}
				continue
			}

			sent, docErr := c.sendTelegramDocument(ctx, chatID, mediaURL, caption)
			if docErr != nil {
				logger.ErrorCF("telegram", "Failed to send document, falling back to text", map[string]interface{}{
					"error": docErr.Error(),
					"url":   mediaURL,
				})
				break // fall through to text send below
			}
			if sent {
				return nil
			}
		}
	}

	// Only reply with voice if the user sent a voice/audio message
	if _, wasVoice := c.voiceInput.LoadAndDelete(msg.ChatID); wasVoice {
		plainText := stripMarkdown(msg.Content)
		if len(plainText) > 0 && len(plainText) <= ttsMaxChars && !containsCode(msg.Content) {
			voiceErr := c.sendVoice(ctx, chatID, plainText)
			if voiceErr == nil {
				return nil
			}
			logger.ErrorCF("telegram", "TTS failed, falling back to text", map[string]interface{}{
				"error": voiceErr.Error(),
			})
		}
	}

	// Send as text, splitting if necessary (Telegram limit: 4096 chars)
	htmlContent := markdownToTelegramHTML(msg.Content)
	chunks := splitMessage(htmlContent, 4096)

	for _, chunk := range chunks {
		tgMsg := tu.Message(tu.ID(chatID), chunk)
		tgMsg.ParseMode = telego.ModeHTML

		if _, err = c.bot.SendMessage(ctx, tgMsg); err != nil {
			logger.ErrorCF("telegram", "HTML parse failed, falling back to plain text", map[string]interface{}{
				"error": err.Error(),
			})
			tgMsg.ParseMode = ""
			if _, err = c.bot.SendMessage(ctx, tgMsg); err != nil {
				return err
			}
		}
	}

	return nil
}

// sendVoice converts text to speech and sends as a Telegram voice message.
func (c *TelegramChannel) sendVoice(ctx context.Context, chatID int64, text string) error {
	tmpMP3 := filepath.Join(os.TempDir(), fmt.Sprintf("chango_tts_%d.mp3", time.Now().UnixNano()))
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("chango_tts_%d.ogg", time.Now().UnixNano()))
	defer os.Remove(tmpMP3)
	defer os.Remove(tmpFile)

	// Use edge-tts CLI directly (installed via pip in container)
	ttsCmd := exec.CommandContext(ctx, "edge-tts", "--voice", "es-AR-TomasNeural", "--text", text, "--write-media", tmpMP3)
	if output, err := ttsCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("edge-tts failed: %w: %s", err, string(output))
	}

	// Convert MP3 to OGG for Telegram voice
	ffCmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", tmpMP3, "-c:a", "libopus", "-b:a", "64k", tmpFile)
	if output, err := ffCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg failed: %w: %s", err, string(output))
	}

	voiceFile, err := os.Open(tmpFile)
	if err != nil {
		return fmt.Errorf("opening voice file: %w", err)
	}
	defer voiceFile.Close()

	voiceParams := &telego.SendVoiceParams{
		ChatID: tu.ID(chatID),
		Voice:  telego.InputFile{File: voiceFile},
	}
	_, err = c.bot.SendVoice(ctx, voiceParams)
	return err
}

// stripMarkdown removes markdown formatting to get plain text length.
func stripMarkdown(text string) string {
	text = reCodeBlock.ReplaceAllString(text, "")
	text = reInlineCode.ReplaceAllString(text, "")
	text = reBold.ReplaceAllString(text, "$1")
	text = reUnderBold.ReplaceAllString(text, "$1")
	text = reItalic.ReplaceAllString(text, "$1")
	text = reStrike.ReplaceAllString(text, "$1")
	text = reLink.ReplaceAllString(text, "$1")
	text = reHeading.ReplaceAllString(text, "")
	text = reListItem.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	return text
}

// containsCode checks if the message has code blocks or inline code.
func containsCode(text string) bool {
	return strings.Contains(text, "```") || reInlineCode.MatchString(text)
}

func (c *TelegramChannel) handleMessage(ctx context.Context, update telego.Update) {
	message := update.Message
	if message == nil {
		return
	}

	user := message.From
	if user == nil {
		return
	}

	userID := fmt.Sprintf("%d", user.ID)
	senderID := userID
	if user.Username != "" {
		senderID = fmt.Sprintf("%s|%s", userID, user.Username)
	}

	chatID := message.Chat.ID
	chatIDStr := fmt.Sprintf("%d", chatID)
	isGroup := message.Chat.Type != "private"
	isAdmin := c.adminUserID != "" && userID == c.adminUserID

	// Handle /join command — admin adds this group to the allowlist
	cmdText := strings.TrimSpace(message.Text)
	// Telegram sends "/join@BotUsername" in groups — normalize
	if idx := strings.Index(cmdText, "@"); idx > 0 {
		cmdText = cmdText[:idx]
	}
	if isGroup && isAdmin && cmdText == "/join" {
		c.AddToAllowList(chatIDStr)
		// Persist to config
		if c.configPath != "" && c.appConfig != nil {
			// Avoid duplicates in persisted config
			alreadyInConfig := false
			for _, id := range c.appConfig.Channels.Telegram.AllowFrom {
				if id == chatIDStr {
					alreadyInConfig = true
					break
				}
			}
			if !alreadyInConfig {
				c.appConfig.Channels.Telegram.AllowFrom = append(c.appConfig.Channels.Telegram.AllowFrom, chatIDStr)
			}
			if err := config.SaveConfig(c.configPath, c.appConfig); err != nil {
				logger.ErrorCF("telegram", "Failed to persist config after /join", map[string]interface{}{"error": err.Error()})
			}
		}
		c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), "✓ Me uní a este grupo. Mencioname o respondé a mis mensajes para hablar conmigo."))
		logger.InfoCF("telegram", "Joined group via /join", map[string]interface{}{"chat_id": chatIDStr})
		return
	}

	// Handle /leave command — admin removes this group from the allowlist
	if isGroup && isAdmin && cmdText == "/leave" {
		c.RemoveFromAllowList(chatIDStr)
		// Persist to config
		if c.configPath != "" && c.appConfig != nil {
			newAllow := make(config.FlexibleStringSlice, 0)
			for _, id := range c.appConfig.Channels.Telegram.AllowFrom {
				if id != chatIDStr {
					newAllow = append(newAllow, id)
				}
			}
			c.appConfig.Channels.Telegram.AllowFrom = newAllow
			if err := config.SaveConfig(c.configPath, c.appConfig); err != nil {
				logger.ErrorCF("telegram", "Failed to persist config after /leave", map[string]interface{}{"error": err.Error()})
			}
		}
		c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), "Listo, me retiro de este grupo. Chau!"))
		logger.InfoCF("telegram", "Left group via /leave", map[string]interface{}{"chat_id": chatIDStr})
		return
	}

	if isGroup {
		// In groups, check if the group chat ID is in the allowlist
		if !c.IsAllowedChat(chatIDStr) {
			return
		}
		// Only respond when mentioned or replied to
		botUsername := c.bot.Username()
		mentioned := botUsername != "" && strings.Contains(message.Text, "@"+botUsername)
		repliedToBot := message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && message.ReplyToMessage.From.IsBot
		if !mentioned && !repliedToBot {
			return
		}
		// Strip the mention from the text so the LLM gets clean input
		if mentioned && botUsername != "" {
			message.Text = strings.ReplaceAll(message.Text, "@"+botUsername, "")
			message.Text = strings.TrimSpace(message.Text)
		}
	} else {
		// Private chat: check user allowlist as before
		if !c.IsAllowed(userID) && !c.IsAllowed(senderID) {
			logger.DebugCF("telegram", "Message rejected by allowlist", map[string]interface{}{
				"user_id":  userID,
				"username": user.Username,
			})
			return
		}
	}

	c.chatIDs[senderID] = chatID

	// Intercept bare /help command → show capabilities
	if text := strings.TrimSpace(message.Text); text == "/help" || text == "/start" {
		c.sendHelp(ctx, chatID)
		return
	}

	// Intercept bare /provider command → show inline keyboard
	if text := strings.TrimSpace(message.Text); text == "/provider" {
		c.sendProviderMenu(ctx, chatID)
		return
	}

	// Intercept bare /model command → show inline keyboard
	if text := strings.TrimSpace(message.Text); text == "/model" {
		c.sendModelMenu(ctx, chatID)
		return
	}

	// Welcome removed: Chango personality comes from AGENTS.md system prompt.
	// Hardcoded greeting reset on every restart and felt robotic.

	content := ""
	mediaPaths := []string{}
	localFiles := []string{} // 跟踪需要清理的本地文件

	// Include quoted/replied message text for context
	if reply := message.ReplyToMessage; reply != nil {
		quotedText := reply.Text
		if quotedText == "" {
			quotedText = reply.Caption
		}
		if quotedText != "" {
			fromName := ""
			if reply.From != nil {
				fromName = reply.From.FirstName
				if reply.From.Username != "" {
					fromName = "@" + reply.From.Username
				}
			}
			if fromName != "" {
				content = fmt.Sprintf("[En respuesta a %s: \"%s\"]\n", fromName, quotedText)
			} else {
				content = fmt.Sprintf("[En respuesta a: \"%s\"]\n", quotedText)
			}
		}
	}

	// 确保临时文件在函数返回时被清理
	defer func() {
		for _, file := range localFiles {
			if err := os.Remove(file); err != nil {
				logger.DebugCF("telegram", "Failed to cleanup temp file", map[string]interface{}{
					"file":  file,
					"error": err.Error(),
				})
			}
		}
	}()

	if message.Text != "" {
		content += message.Text
	}

	if message.Caption != "" {
		if content != "" {
			content += "\n"
		}
		content += message.Caption
	}

	if message.Photo != nil && len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		photoPath := c.downloadPhoto(ctx, photo.FileID)
		if photoPath != "" {
			localFiles = append(localFiles, photoPath)
			// Read and base64-encode the image so it survives temp file cleanup
			if imgData, err := os.ReadFile(photoPath); err == nil {
				dataURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imgData)
				mediaPaths = append(mediaPaths, dataURI)
			}
			if content != "" {
				content += "\n"
			}
			content += "[image: photo]"
			logger.InfoCF("telegram", "Image attached from Telegram photo", map[string]interface{}{
				"chat_id":     fmt.Sprintf("%d", chatID),
				"media_count": len(mediaPaths),
				"source":      "photo",
				"has_caption": message.Caption != "",
			})
		}
	}

	// Track whether this input is voice/audio for TTS response
	isVoiceInput := message.Voice != nil || message.Audio != nil
	voiceKey := fmt.Sprintf("%d", chatID)
	if isVoiceInput {
		c.voiceInput.Store(voiceKey, true)
	} else {
		c.voiceInput.Delete(voiceKey)
	}

	if message.Voice != nil {
		voicePath := c.downloadFile(ctx, message.Voice.FileID, ".ogg")
		if voicePath != "" {
			localFiles = append(localFiles, voicePath)
			mediaPaths = append(mediaPaths, voicePath)

			transcribedText := ""
			if c.transcriber != nil && c.transcriber.IsAvailable() {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()

				result, err := c.transcriber.Transcribe(ctx, voicePath)
				if err != nil {
					logger.ErrorCF("telegram", "Voice transcription failed", map[string]interface{}{
						"error": err.Error(),
						"path":  voicePath,
					})
					transcribedText = "[No pude transcribir el audio — intentá de nuevo o escribí el mensaje]"
				} else {
					transcribedText = fmt.Sprintf("[voice transcription: %s]", result.Text)
					logger.InfoCF("telegram", "Voice transcribed successfully", map[string]interface{}{
						"text": result.Text,
					})
				}
			} else {
				transcribedText = fmt.Sprintf("[voice]")
			}

			if content != "" {
				content += "\n"
			}
			content += transcribedText
		}
	}

	if message.Audio != nil {
		audioPath := c.downloadFile(ctx, message.Audio.FileID, ".mp3")
		if audioPath != "" {
			localFiles = append(localFiles, audioPath)
			mediaPaths = append(mediaPaths, audioPath)
			if content != "" {
				content += "\n"
			}
			content += fmt.Sprintf("[audio]")
		}
	}

	if message.Document != nil {
		docPath := c.downloadFile(ctx, message.Document.FileID, "")
		if docPath != "" {
			localFiles = append(localFiles, docPath)
			mimeType := ""
			if message.Document.MimeType != "" {
				mimeType = message.Document.MimeType
			}
			fileName := ""
			if message.Document.FileName != "" {
				fileName = message.Document.FileName
			}

			// Telegram images sent as "document" should still reach the model as images.
			if isImageDocument(mimeType, fileName) {
				dataURI, err := fileToDataURI(docPath, mimeType)
				if err != nil {
					logger.ErrorCF("telegram", "Failed to encode image document as data URI", map[string]interface{}{
						"path":  docPath,
						"error": err.Error(),
					})
				} else {
					mediaPaths = append(mediaPaths, dataURI)
					if content != "" {
						content += "\n"
					}
					if fileName != "" {
						content += fmt.Sprintf("[image: %s]", fileName)
					} else {
						content += "[image: document]"
					}
					logger.InfoCF("telegram", "Image attached from Telegram document", map[string]interface{}{
						"chat_id":     fmt.Sprintf("%d", chatID),
						"file_name":   fileName,
						"mime_type":   mimeType,
						"media_count": len(mediaPaths),
						"source":      "document",
					})
				}
			} else {
				if content != "" {
					content += "\n"
				}
				if extracted := c.extractDocumentText(docPath, mimeType, fileName); extracted != "" {
					content += extracted
				} else {
					mediaPaths = append(mediaPaths, docPath)
					content += fmt.Sprintf("[file: %s]", fileName)
				}
			}
		}
	}

	if content == "" {
		content = "[empty message]"
	}

	logger.DebugCF("telegram", "Received message", map[string]interface{}{
		"sender_id": senderID,
		"chat_id":   fmt.Sprintf("%d", chatID),
		"preview":   utils.Truncate(content, 50),
	})
	if len(mediaPaths) > 0 {
		logger.InfoCF("telegram", "Telegram media prepared for provider", map[string]interface{}{
			"chat_id":     fmt.Sprintf("%d", chatID),
			"media_count": len(mediaPaths),
		})
	}

	// Thinking indicator
	err := c.bot.SendChatAction(ctx, tu.ChatAction(tu.ID(chatID), telego.ChatActionTyping))
	if err != nil {
		logger.ErrorCF("telegram", "Failed to send chat action", map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Stop any previous thinking animation
	if prevStop, ok := c.stopThinking.Load(chatIDStr); ok {
		if cf, ok := prevStop.(*thinkingCancel); ok && cf != nil {
			cf.Cancel()
		}
	}

	// Create cancel function for thinking state
	_, thinkCancel := context.WithTimeout(ctx, 5*time.Minute)
	c.stopThinking.Store(chatIDStr, &thinkingCancel{fn: thinkCancel})

	pMsg, err := c.bot.SendMessage(ctx, tu.Message(tu.ID(chatID), "Thinking... 💭"))
	if err == nil {
		pID := pMsg.MessageID
		c.placeholders.Store(chatIDStr, pID)
	}

	metadata := map[string]string{
		"message_id": fmt.Sprintf("%d", message.MessageID),
		"user_id":    fmt.Sprintf("%d", user.ID),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}

	c.HandleMessage(senderID, fmt.Sprintf("%d", chatID), content, mediaPaths, metadata)
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get photo file", map[string]interface{}{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}

	url := c.bot.FileDownloadURL(file.FilePath)
	logger.DebugCF("telegram", "File URL", map[string]interface{}{"url": url})

	// Use FilePath as filename for better identification
	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "telegram",
	})
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get file", map[string]interface{}{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ext)
}

func (c *TelegramChannel) extractDocumentText(docPath, mimeType, fileName string) string {
	lowerName := strings.ToLower(fileName)
	switch {
	case mimeType == "application/pdf" || strings.HasSuffix(lowerName, ".pdf"):
		pdfText := c.extractPDFText(docPath)
		if pdfText == "" {
			return fmt.Sprintf("[PDF: %s - no se pudo extraer texto]", fileName)
		}
		return fmt.Sprintf("[PDF: %s]\n%s", fileName, pdfText)
	case mimeType == "text/csv" || strings.HasSuffix(lowerName, ".csv"):
		if csvText := extractCSVText(docPath); csvText != "" {
			return fmt.Sprintf("[CSV: %s]\n%s", fileName, csvText)
		}
		return fmt.Sprintf("[CSV: %s - no se pudo extraer texto]", fileName)
	case mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || strings.HasSuffix(lowerName, ".docx"):
		if docxText := extractDOCXText(docPath); docxText != "" {
			return fmt.Sprintf("[DOCX: %s]\n%s", fileName, docxText)
		}
		return fmt.Sprintf("[DOCX: %s - no se pudo extraer texto]", fileName)
	case mimeType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		mimeType == "application/vnd.ms-excel" ||
		strings.HasSuffix(lowerName, ".xlsx") ||
		strings.HasSuffix(lowerName, ".xls"):
		if sheetText := extractSpreadsheetText(docPath); sheetText != "" {
			return fmt.Sprintf("[Spreadsheet: %s]\n%s", fileName, sheetText)
		}
		return fmt.Sprintf("[Spreadsheet: %s - no se pudo extraer texto]", fileName)
	default:
		return ""
	}
}

// extractPDFText uses pdftotext to extract text from a PDF file.
// Returns extracted text (truncated to 15000 chars to avoid context overflow).
func (c *TelegramChannel) extractPDFText(pdfPath string) string {
	cmd := exec.Command("pdftotext", "-layout", pdfPath, "-")
	output, err := cmd.Output()
	if err != nil {
		logger.ErrorCF("telegram", "Failed to extract PDF text", map[string]interface{}{
			"path":  pdfPath,
			"error": err.Error(),
		})
		return ""
	}

	text := strings.TrimSpace(string(output))
	if len(text) > 15000 {
		text = text[:15000] + "\n\n[... texto truncado, PDF muy largo ...]"
	}

	logger.InfoCF("telegram", "PDF text extracted", map[string]interface{}{
		"path":  pdfPath,
		"chars": len(text),
	})
	return text
}

func extractCSVText(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		return ""
	}

	var lines []string
	maxRows := minInt(len(rows), 20)
	for i := 0; i < maxRows; i++ {
		lines = append(lines, strings.Join(rows[i], " | "))
	}
	text := strings.Join(lines, "\n")
	return truncateDocText(text, 15000)
}

func extractDOCXText(path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()

	var documentXML []byte
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		documentXML, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return ""
		}
		break
	}
	if len(documentXML) == 0 {
		return ""
	}

	decoder := xml.NewDecoder(bytes.NewReader(documentXML))
	var lines []string
	var paragraph strings.Builder
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if se.Name.Local == "tab" {
				paragraph.WriteString("\t")
			}
		case xml.EndElement:
			if se.Name.Local == "p" {
				text := strings.TrimSpace(paragraph.String())
				if text != "" {
					lines = append(lines, text)
				}
				paragraph.Reset()
			}
		case xml.CharData:
			paragraph.WriteString(string(se))
		}
	}

	return truncateDocText(strings.Join(lines, "\n"), 15000)
}

func extractSpreadsheetText(path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()

	sharedStrings := map[int]string{}
	var sheetFiles []string
	for _, f := range zr.File {
		switch {
		case f.Name == "xl/sharedStrings.xml":
			sharedStrings = parseXLSXSharedStrings(f)
		case strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml"):
			sheetFiles = append(sheetFiles, f.Name)
		}
	}
	if len(sheetFiles) == 0 {
		return ""
	}

	var lines []string
	for _, name := range sheetFiles {
		for _, f := range zr.File {
			if f.Name != name {
				continue
			}
			sheetLines := parseXLSXSheet(f, sharedStrings)
			if len(sheetLines) > 0 {
				lines = append(lines, fmt.Sprintf("## %s", filepath.Base(name)))
				lines = append(lines, sheetLines...)
			}
			break
		}
	}

	return truncateDocText(strings.Join(lines, "\n"), 15000)
}

func parseXLSXSharedStrings(file *zip.File) map[int]string {
	result := map[int]string{}
	rc, err := file.Open()
	if err != nil {
		return result
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	idx := -1
	inText := false
	var current strings.Builder
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if se.Name.Local == "si" {
				idx++
				current.Reset()
			}
			if se.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			if se.Name.Local == "t" {
				inText = false
			}
			if se.Name.Local == "si" && idx >= 0 {
				result[idx] = current.String()
			}
		case xml.CharData:
			if inText {
				current.WriteString(string(se))
			}
		}
	}
	return result
}

func parseXLSXSheet(file *zip.File, sharedStrings map[int]string) []string {
	rc, err := file.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	var lines []string
	var row []string
	cellType := ""
	inValue := false
	maxRows := 20

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "row":
				row = nil
			case "c":
				cellType = ""
				for _, attr := range se.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			case "v", "t":
				inValue = true
			}
		case xml.EndElement:
			switch se.Name.Local {
			case "v", "t":
				inValue = false
			case "row":
				if len(row) > 0 {
					lines = append(lines, strings.Join(row, " | "))
					if len(lines) >= maxRows {
						return lines
					}
				}
			}
		case xml.CharData:
			if !inValue {
				continue
			}
			val := string(se)
			if cellType == "s" {
				if idx, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					val = sharedStrings[idx]
				}
			}
			val = strings.TrimSpace(val)
			if val != "" {
				row = append(row, val)
			}
		}
	}
	return lines
}

func truncateDocText(text string, maxLen int) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "\n\n[... texto truncado ...]"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (c *TelegramChannel) sendHelp(ctx context.Context, chatID int64) {
	helpText := "¡Hola! Soy <b>Chango</b> 🐒 — agente autónomo de Diego.\n\n" +
		"<b>Comandos:</b>\n" +
		"/help — este mensaje\n" +
		"/status — estado del sistema\n" +
		"/model — cambiar modelo LLM\n" +
		"/provider — cambiar proveedor\n\n" +
		"<b>Capacidades:</b>\n" +
		"• Enviar/leer emails (Gmail)\n" +
		"• Calendario y agenda\n" +
		"• Buscar en la web\n" +
		"• Generar imágenes\n" +
		"• Recordatorios y tareas\n" +
		"• Memoria persistente\n" +
		"• Controlar luces WiFi\n" +
		"• Investigar temas (web + YouTube)\n" +
		"• Traducir textos\n" +
		"• Y más...\n\n" +
		"Podés hablarme en texto o audio 🎤"
	msg := tu.Message(tu.ID(chatID), helpText)
	msg.ParseMode = telego.ModeHTML
	_, _ = c.bot.SendMessage(ctx, msg)
}

func (c *TelegramChannel) sendModelMenu(ctx context.Context, chatID int64) {
	models := c.appConfig.Agents.Defaults.AvailableModels
	if len(models) == 0 {
		// Use provider-specific models if available
		currentProvider := strings.ToLower(c.appConfig.Agents.Defaults.Provider)
		if pm, ok := providerModels[currentProvider]; ok {
			models = pm
		} else {
			models = defaultModels
		}
	}
	currentModel := c.appConfig.Agents.Defaults.Model

	var buttons []telego.InlineKeyboardButton
	for _, m := range models {
		label := m
		if m == currentModel {
			label = "\u2705 " + m
		}
		buttons = append(buttons, tu.InlineKeyboardButton(label).WithCallbackData("model:"+m))
	}

	keyboard := tu.InlineKeyboardGrid(tu.InlineKeyboardCols(2, buttons...))
	msg := tu.Message(tu.ID(chatID), "Elegí un modelo:")
	msg.ReplyMarkup = keyboard

	if _, err := c.bot.SendMessage(ctx, msg); err != nil {
		logger.ErrorCF("telegram", "Failed to send model menu", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

func (c *TelegramChannel) sendProviderMenu(ctx context.Context, chatID int64) {
	available := providers.AvailableProviders(c.appConfig)
	if len(available) == 0 {
		msg := tu.Message(tu.ID(chatID), "No hay providers configurados con credenciales.")
		_, _ = c.bot.SendMessage(ctx, msg)
		return
	}

	currentProvider := strings.ToLower(c.appConfig.Agents.Defaults.Provider)

	var buttons []telego.InlineKeyboardButton
	for _, p := range available {
		label := p
		if p == currentProvider {
			label = "\u2705 " + p
		}
		buttons = append(buttons, tu.InlineKeyboardButton(label).WithCallbackData("provider:"+p))
	}

	keyboard := tu.InlineKeyboardGrid(tu.InlineKeyboardCols(2, buttons...))
	msg := tu.Message(tu.ID(chatID), fmt.Sprintf("Provider actual: %s\nElegí un provider:", currentProvider))
	msg.ReplyMarkup = keyboard

	if _, err := c.bot.SendMessage(ctx, msg); err != nil {
		logger.ErrorCF("telegram", "Failed to send provider menu", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

func (c *TelegramChannel) handleCallbackQuery(ctx context.Context, update telego.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}

	var command, displayText, answerText string
	switch {
	case strings.HasPrefix(query.Data, "model:"):
		modelName := strings.TrimPrefix(query.Data, "model:")
		command = "/model " + modelName
		displayText = "\u2705 Modelo: " + modelName
		answerText = "Cambiando a " + modelName + "..."
	case strings.HasPrefix(query.Data, "provider:"):
		providerName := strings.TrimPrefix(query.Data, "provider:")
		command = "/provider " + providerName
		displayText = "\u2705 Provider: " + providerName
		answerText = "Cambiando a " + providerName + "..."
	default:
		return
	}

	// Answer the callback to dismiss the spinner
	_ = c.bot.AnswerCallbackQuery(ctx, tu.CallbackQuery(query.ID).WithText(answerText))

	// Edit original message to show selection, remove buttons
	if query.Message != nil {
		editParams := &telego.EditMessageTextParams{
			ChatID:    tu.ID(query.Message.GetChat().ID),
			MessageID: query.Message.GetMessageID(),
			Text:      displayText,
		}
		_, _ = c.bot.EditMessageText(ctx, editParams)
	}

	// Publish to bus so AgentLoop handles the actual change
	userID := fmt.Sprintf("%d", query.From.ID)
	senderID := userID
	if query.From.Username != "" {
		senderID = fmt.Sprintf("%s|%s", userID, query.From.Username)
	}
	chatIDStr := ""
	if query.Message != nil {
		chatIDStr = fmt.Sprintf("%d", query.Message.GetChat().ID)
	}

	c.HandleMessage(senderID, chatIDStr, command, nil, map[string]string{
		"user_id":  userID,
		"username": query.From.Username,
	})
}

// splitMessage splits a message into chunks that fit within Telegram's character limit.
// It tries to split at paragraph boundaries (\n\n), then at line breaks (\n),
// and as a last resort at the exact limit.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to split at paragraph boundary
		chunk := text[:maxLen]
		splitAt := strings.LastIndex(chunk, "\n\n")
		if splitAt < maxLen/2 {
			// Try line break
			splitAt = strings.LastIndex(chunk, "\n")
		}
		if splitAt < maxLen/4 {
			// Hard split
			splitAt = maxLen
		}

		chunks = append(chunks, strings.TrimSpace(text[:splitAt]))
		text = strings.TrimSpace(text[splitAt:])
	}

	return chunks
}

func (c *TelegramChannel) sendTelegramPhoto(ctx context.Context, chatID int64, mediaRef, caption string) (bool, error) {
	photoParams := &telego.SendPhotoParams{
		ChatID: tu.ID(chatID),
	}
	if caption != "" {
		photoParams.Caption = caption
		photoParams.ParseMode = telego.ModeHTML
	}

	inputFile, cleanup, useUpload, err := telegramMediaInput(mediaRef, ".png")
	if err != nil {
		return false, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	if useUpload {
		photoParams.Photo = inputFile
	} else {
		photoParams.Photo = tu.FileFromURL(mediaRef)
	}

	_, err = c.bot.SendPhoto(ctx, photoParams)
	return err == nil, err
}

func (c *TelegramChannel) sendTelegramDocument(ctx context.Context, chatID int64, mediaRef, caption string) (bool, error) {
	docParams := &telego.SendDocumentParams{
		ChatID: tu.ID(chatID),
	}
	if caption != "" {
		docParams.Caption = caption
		docParams.ParseMode = telego.ModeHTML
	}

	inputFile, cleanup, useUpload, err := telegramMediaInput(mediaRef, ".bin")
	if err != nil {
		return false, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	if useUpload {
		docParams.Document = inputFile
	} else {
		docParams.Document = tu.FileFromURL(mediaRef)
	}

	_, err = c.bot.SendDocument(ctx, docParams)
	return err == nil, err
}

// isImageURL returns true if the URL points to a known image format or is a data URI image.
func isImageURL(u string) bool {
	if strings.HasPrefix(u, "data:image/") {
		return true
	}
	lower := strings.ToLower(u)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(lower, ext) || strings.Contains(lower, ext+"?") {
			return true
		}
	}
	return false
}

func telegramMediaInput(mediaRef, fallbackExt string) (telego.InputFile, func(), bool, error) {
	if strings.HasPrefix(mediaRef, "data:") {
		return telegramInputFileFromDataURI(mediaRef, fallbackExt)
	}

	if looksLikeLocalPath(mediaRef) {
		file, err := os.Open(mediaRef)
		if err != nil {
			return telego.InputFile{}, nil, false, fmt.Errorf("opening media file %q: %w", mediaRef, err)
		}
		cleanup := func() {
			_ = file.Close()
		}
		return telego.InputFile{File: file}, cleanup, true, nil
	}

	return telego.InputFile{}, nil, false, nil
}

func telegramInputFileFromDataURI(dataURI, fallbackExt string) (telego.InputFile, func(), bool, error) {
	header, payload, found := strings.Cut(dataURI, ",")
	if !found {
		return telego.InputFile{}, nil, false, fmt.Errorf("invalid data URI")
	}
	if !strings.HasSuffix(header, ";base64") {
		return telego.InputFile{}, nil, false, fmt.Errorf("unsupported data URI encoding")
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return telego.InputFile{}, nil, false, fmt.Errorf("decoding data URI: %w", err)
	}

	ext := extensionFromDataURIHeader(header)
	if ext == "" {
		ext = fallbackExt
	}

	tmpFile, err := os.CreateTemp("", "picoclaw_tg_media_*"+ext)
	if err != nil {
		return telego.InputFile{}, nil, false, fmt.Errorf("creating temp media file: %w", err)
	}

	if _, err := io.Copy(tmpFile, bytes.NewReader(data)); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return telego.InputFile{}, nil, false, fmt.Errorf("writing temp media file: %w", err)
	}

	if _, err := tmpFile.Seek(0, 0); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return telego.InputFile{}, nil, false, fmt.Errorf("rewinding temp media file: %w", err)
	}

	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}
	return telego.InputFile{File: tmpFile}, cleanup, true, nil
}

func extensionFromDataURIHeader(header string) string {
	switch {
	case strings.HasPrefix(header, "data:image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(header, "data:image/png"):
		return ".png"
	case strings.HasPrefix(header, "data:image/gif"):
		return ".gif"
	case strings.HasPrefix(header, "data:image/webp"):
		return ".webp"
	case strings.HasPrefix(header, "data:application/pdf"):
		return ".pdf"
	default:
		return ""
	}
}

func isImageDocument(mimeType, fileName string) bool {
	if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return true
	}
	lowerName := strings.ToLower(fileName)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

func fileToDataURI(path, mimeType string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if mimeType == "" {
		mimeType = mimeTypeFromFilename(path)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func mimeTypeFromFilename(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

func looksLikeLocalPath(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
		return false
	}
	if filepath.IsAbs(ref) {
		return true
	}
	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		return true
	}
	_, err := os.Stat(ref)
	return err == nil
}

func parseChatID(chatIDStr string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(chatIDStr, "%d", &id)
	return id, err
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	text = reHeading.ReplaceAllString(text, "$1")

	text = reBlockquote.ReplaceAllString(text, "$1")

	text = escapeHTML(text)

	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	text = reBold.ReplaceAllString(text, "<b>$1</b>")

	text = reUnderBold.ReplaceAllString(text, "<b>$1</b>")

	text = reItalic.ReplaceAllStringFunc(text, func(s string) string {
		match := reItalic.FindStringSubmatch(s)
		if len(match) < 2 {
			return s
		}
		return "<i>" + match[1] + "</i>"
	})

	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	text = reListItem.ReplaceAllString(text, "• ")

	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), fmt.Sprintf("<code>%s</code>", escaped))
	}

	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00CB%d\x00", i), fmt.Sprintf("<pre><code>%s</code></pre>", escaped))
	}

	return text
}

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	matches := reCodeBlockCap.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reCodeBlockCap.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	reCapture := regexp.MustCompile("`([^`]+)`")
	matches := reCapture.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reInlineCode.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}
