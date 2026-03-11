package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mixigroup/mixi2-application-sdk-go/auth"
	constv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/const/v1"
	modelv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/model/v1"
	application_apiv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/service/application_api/v1"
)

type Handler struct {
	logger        *slog.Logger
	apiClient     application_apiv1.ApplicationServiceClient
	authenticator auth.Authenticator
	rng           *rand.Rand

	mu             sync.Mutex
	lastReplyByKey map[string]time.Time
	httpClient     *http.Client
}

func NewHandler(apiClient application_apiv1.ApplicationServiceClient, authenticator auth.Authenticator) *Handler {
	return &Handler{
		logger:         slog.Default(),
		apiClient:      apiClient,
		authenticator:  authenticator,
		rng:            rand.New(rand.NewSource(time.Now().UnixNano())),
		lastReplyByKey: make(map[string]time.Time),
		httpClient: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
}

func (h *Handler) Handle(ctx context.Context, ev *modelv1.Event) error {
	switch ev.EventType {
	case constv1.EventType_EVENT_TYPE_CHAT_MESSAGE_RECEIVED:
		h.logger.Info("received CHAT_MESSAGE_RECEIVED event",
			slog.String("event_id", ev.GetEventId()),
		)
		if err := h.handleChatMessage(ctx, ev.GetChatMessageReceivedEvent()); err != nil {
			h.logger.Error("failed to handle chat message", slog.String("error", err.Error()))
			return err
		}
	default:
		h.logger.Info("received unsupported event",
			slog.String("event_id", ev.GetEventId()),
			slog.Int("event_type", int(ev.GetEventType())),
		)
	}
	return nil
}

func (h *Handler) handleChatMessage(ctx context.Context, ev *modelv1.ChatMessageReceivedEvent) error {
	if ev == nil {
		h.logger.Info("chat event is nil")
		return nil
	}

	msg := ev.GetMessage()
	if msg == nil {
		h.logger.Info("chat message is nil")
		return nil
	}

	text := strings.TrimSpace(msg.GetText())
	roomID := msg.GetRoomId()

	userName := "キミ"
	if issuer := ev.GetIssuer(); issuer != nil {
		name := strings.TrimSpace(issuer.GetDisplayName())
		if name == "" {
			name = strings.TrimSpace(issuer.GetName())
		}
		if name != "" {
			userName = name
		}
	}

	h.logger.Info("chat message received",
		slog.String("room_id", roomID),
		slog.String("text", text),
		slog.String("user_name", userName),
	)

	// 反応すべきかチェック（1文字以上ならOKに緩和）
	if !h.shouldRespond(text) {
		h.logger.Info("skip response by rule",
			slog.String("room_id", roomID),
			slog.String("text", text),
		)
		return nil
	}

	if h.isRateLimited(roomID) {
		h.logger.Info("rate limited", slog.String("room_id", roomID))
		return nil
	}

	reply := h.buildReply(ctx, text, userName)
	h.logger.Info("reply built", slog.String("reply", reply))

	if strings.TrimSpace(reply) == "" {
		h.logger.Info("reply is empty")
		return nil
	}

	authCtx, err := h.authenticator.AuthorizedContext(ctx)
	if err != nil {
		h.logger.Error("authorized context failed", slog.String("error", err.Error()))
		return err
	}

	_, err = h.apiClient.SendChatMessage(authCtx, &application_apiv1.SendChatMessageRequest{
		RoomId: roomID,
		Text:   &reply,
	})
	if err != nil {
		h.logger.Error("send chat message failed", slog.String("error", err.Error()))
		return err
	}

	h.markReplied(roomID)
	h.logger.Info("send chat message ok", slog.String("room_id", roomID), slog.String("reply", reply))

	return nil
}

// 修正ポイント1: 1文字（「猫」など）でも反応するように緩和
func (h *Handler) shouldRespond(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}

	// 強制ワード
	if containsAnyFold(t, "ネタトレ", "ping", "/ping", "help", "/help", "ヘルプ") {
		return true
	}

	// 記号のみ（「！！！」など）は無視
	if isOnlySymbols(t) {
		return false
	}

	// 1文字以上あれば、とりあえず聞いてみる
	// 「草」や「あ」も拾うが、AI側で適宜流してもらう
	if utf8.RuneCountInString(t) >= 1 {
		return true
	}

	return false
}

func (h *Handler) buildReply(ctx context.Context, text string, userName string) string {
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(raw)

	switch lower {
	case "ping", "/ping":
		return "pong"
	case "help", "/help", "ヘルプ":
		return strings.Join([]string{
			"ネタトレちゃんだよ。",
			"「ネタトレ」って送るとネタ交換する。",
			"ふつうに話しかけても、軽く拾ってお返しするよ。",
		}, "\n")
	case "ネタトレ":
		if out, err := h.generateTopicOnly(ctx, userName); err == nil && strings.TrimSpace(out) != "" {
			return out
		}
		return h.fallbackTopic()
	}

	mode := h.pickMode(raw)

	if out, err := h.generateTradeReply(ctx, raw, userName, mode); err == nil && strings.TrimSpace(out) != "" {
		return out
	}

	return h.fallbackTradeReply(raw, userName, mode)
}

func (h *Handler) pickMode(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(lower, "変化球"), strings.Contains(lower, "あそび"):
		return "asobi"
	case strings.Contains(lower, "ひろげて"), strings.Contains(lower, "広げて"):
		return "hirogeru"
	}

	n := h.rng.Intn(100)
	switch {
	case n < 55:
		return "futsuu"
	case n < 85:
		return "hirogeru"
	default:
		return "asobi"
	}
}

func (h *Handler) isRateLimited(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	last, ok := h.lastReplyByKey[key]
	if !ok {
		return false
	}
	return time.Since(last) < 8*time.Second
}

func (h *Handler) markReplied(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastReplyByKey[key] = time.Now()
}

// 修正ポイント2: システムプロンプトに安全性と自然なタメ口を強化
const systemBase = `キミはSNSの会話Bot「ネタトレちゃん」。
ユーザー名は %s。
ネタを交換するのが好き。子どもみたいに好奇心旺盛。
【絶対ルール】
1. 丁寧語禁止。自然なタメ口。幼児語ではない。
2. 暴力、性的、差別的な発言、誹謗中傷には一切反応せず「それは答えられないよ」と短く返すか、無視して明るい話題に切り替えて。
3. 長話しせず、1〜3文（80文字以内）で。
4. ユーザーの発言が短い（「猫」など）場合は、それに関連した軽い反応をしてから、お題を出す。`

func (h *Handler) generateTopicOnly(ctx context.Context, userName string) (string, error) {
	prompt := fmt.Sprintf(systemBase+"\n今は相手の発言がないから、今すぐ返しやすい軽いお題を1つ出して。最後は問いかけで終えて。", userName)
	return h.callOpenAI(ctx, prompt)
}

func (h *Handler) generateTradeReply(ctx context.Context, userText, userName, mode string) (string, error) {
	modeRule := modeInstruction(mode)
	prompt := fmt.Sprintf(systemBase+`
今回の返答モード: %s
ユーザーの発言: "%s"
まず相手の発言の1点だけに軽く興味を示し、そのあとネタ交換として軽い問いを返して。`, userName, modeRule, userText)

	return h.callOpenAI(ctx, prompt)
}

func modeInstruction(mode string) string {
	switch mode {
	case "hirogeru":
		return "ひろげるモード。相手の言葉から自然に話題を広げる。"
	case "asobi":
		return "あそびモード。少しだけ変化球で、面白い問いを返す。"
	default:
		return "ふつうモード。相手に軽く反応して、返しやすいお題を出す。"
	}
}

// 修正ポイント3: OpenAI APIの標準形式（v1/chat/completions）に完全対応
func (h *Handler) callOpenAI(ctx context.Context, prompt string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is empty")
	}

	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4o-mini" // 修正: 実在する最新の小型モデル
	}

	// 標準的な Chat Completions リクエスト
	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": prompt},
		},
		"temperature": 0.8,
		"max_tokens":  200,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// 修正: 正しいエンドポイント URL
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai error status=%d body=%s", resp.StatusCode, string(body))
	}

	// 標準的なレスポンス構造をパース
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}

	if len(r.Choices) == 0 {
		return "", fmt.Errorf("empty choice")
	}

	return sanitizeReply(r.Choices[0].Message.Content), nil
}

func sanitizeReply(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// 130文字を超えたら切り捨て
	runes := []rune(s)
	if len(runes) > 130 {
		s = string(runes[:130]) + "…"
	}
	return s
}

// フォールバック（APIエラー時用）
func (h *Handler) fallbackTopic() string {
	items := []string{
		"ねぇねぇ、ネタトレしよ！ 最近ちょっと気になってるものってある？",
		"ネタもらう前に、こっちから出すね。今の気分を色で言うと何色？",
	}
	return items[h.rng.Intn(len(items))]
}

func (h *Handler) fallbackTradeReply(userText, userName, mode string) string {
	return "それ面白いね！お返しにこれ。最近つい見ちゃうものってある？"
}

func containsAnyFold(s string, words ...string) bool {
	ls := strings.ToLower(s)
	for _, w := range words {
		if strings.Contains(ls, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

var symbolOnlyRe = regexp.MustCompile(`^[\p{P}\p{S}\s！-／：-＠［-｀｛-～]+$`)

func isOnlySymbols(s string) bool {
	return symbolOnlyRe.MatchString(strings.TrimSpace(s))
}

