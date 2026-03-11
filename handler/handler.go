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
	h.logger.Info("authorized context ok")

	_, err = h.apiClient.SendChatMessage(authCtx, &application_apiv1.SendChatMessageRequest{
		RoomId: roomID,
		Text:   &reply,
	})
	if err != nil {
		h.logger.Error("send chat message failed", slog.String("error", err.Error()))
		return err
	}

	h.markReplied(roomID)

	h.logger.Info("send chat message ok",
		slog.String("room_id", roomID),
		slog.String("reply", reply),
	)

	return nil
}

func (h *Handler) shouldRespond(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}

	// 強制起動ワード
	if containsAnyFold(t, "ネタトレ", "ping", "/ping", "help", "/help", "ヘルプ") {
		return true
	}

	// 短すぎる文は無視
	if utf8.RuneCountInString(t) <= 1 {
		return false
	}

	// 記号だけっぽいものは無視
	if isOnlySymbols(t) {
		return false
	}

	// 公開側を意識して、メンションっぽいものだけ反応しやすくする
	// DMではそもそもメンション不要だが、普通文もある程度は返したいので、
	// 「意味のある長さ」の文なら反応可にする
	if utf8.RuneCountInString(t) >= 4 {
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
			"ping で pong も返す。",
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

	// 明示コマンド
	switch {
	case strings.Contains(lower, "変化球"), strings.Contains(lower, "あそび"), strings.Contains(lower, "変なお題"):
		return "asobi"
	case strings.Contains(lower, "ひろげて"), strings.Contains(lower, "広げて"):
		return "hirogeru"
	}

	// ランダム分岐
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

func (h *Handler) generateTopicOnly(ctx context.Context, userName string) (string, error) {
	prompt := fmt.Sprintf(strings.Join([]string{
		"キミはSNSの会話Bot「ネタトレちゃん」。",
		"ユーザー名は %s。",
		"ネタを交換するのが好き。",
		"子どもみたいに何でも気になる。",
		"丁寧語は禁止。自然なタメ口。",
		"幼児語にはしない。",
		"軽く、明るく、押しつけない。",
		"長話ししない。1〜3文。",
		"同じ口癖を毎回使わない。",
		"ときどき次の雰囲気を混ぜていい。",
		"「ねぇねぇ、ネタトレしよ！」",
		"「ネタもらった。じゃあお返しにこれ！」",
		"「いいこと聞いたから、お返しにこれ聞いていい？」",
		"今回は相手の発言はまだ無い。",
		"返すのは今すぐ返しやすい軽いお題だけ。",
		"深刻すぎない。説教しない。説明しすぎない。",
		"60〜100文字くらい。",
		"最後は問いかけで終える。",
		"完成文だけ返す。",
	}, "\n"), userName)

	return h.callOpenAI(ctx, prompt)
}

func (h *Handler) generateTradeReply(ctx context.Context, userText, userName, mode string) (string, error) {
	modeRule := modeInstruction(mode)

	prompt := fmt.Sprintf(strings.Join([]string{
		"キミはSNSの会話Bot「ネタトレちゃん」。",
		"ユーザー名は %s。",
		"ネタを交換するのが好き。",
		"子どもみたいに何でも気になる。",
		"丁寧語は禁止。自然なタメ口。",
		"幼児語にはしない。",
		"明るいけど、うるさすぎない。",
		"友達っぽい距離感。",
		"ときどきユーザー名を呼んでもいいが、毎回は呼ばない。",
		"まず相手の発言の中の1点だけに軽く興味を示す。",
		"そのあとネタ交換として返しやすい問いを返す。",
		"1〜3文。",
		"60〜110文字くらい。",
		"説教しない。結論を出さない。深刻にしすぎない。",
		"同じ口癖を毎回使わない。",
		"使っていい雰囲気の言い回し:",
		"「ねぇねぇ、ネタトレしよ！」",
		"「ネタもらった。じゃあお返しにこれ！」",
		"「いいこと聞いたから、お返しにこれ聞いていい？」",
		"",
		"今回の返答モード:",
		modeRule,
		"",
		"ユーザーの発言:",
		"%s",
		"",
		"出力はそのままSNSで読める完成文だけ。",
	}, "\n"), userName, userText)

	return h.callOpenAI(ctx, prompt)
}

func modeInstruction(mode string) string {
	switch mode {
	case "hirogeru":
		return "ひろげるモード。相手の言葉を少し拾って、そこから自然に話題を広げる。"
	case "asobi":
		return "あそびモード。少しだけ変化球で、軽くて面白い問いを返す。変すぎず、返しやすさは残す。"
	default:
		return "ふつうモード。相手の言葉に軽く反応して、返しやすいお題を返す。"
	}
}

func (h *Handler) callOpenAI(ctx context.Context, prompt string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is empty")
	}

	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-5.4"
	}

	reqBody := openAIResponsesRequest{
		Model: model,
		Input: []openAIInputItem{
			{
				Role:    "developer",
				Type:    "message",
				Content: prompt,
			},
		},
		Temperature: 0.9,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(b))
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

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai status=%d body=%s", resp.StatusCode, string(body))
	}

	var r openAIResponsesResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}

	text := strings.TrimSpace(r.ExtractText())
	if text == "" {
		return "", fmt.Errorf("empty openai output")
	}

	return sanitizeReply(text), nil
}

func sanitizeReply(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	s = strings.Join(out, "\n")

	runes := []rune(s)
	if len(runes) > 130 {
		s = string(runes[:130]) + "…"
	}
	return s
}

func (h *Handler) fallbackTopic() string {
	items := []string{
		"ねぇねぇ、ネタトレしよ！ 最近ちょっと気になってるものってある？",
		"ネタもらう前に、こっちから出すね。今の気分を色で言うと何色？",
		"いいこと思いついた。最近、前より好きになったものってある？",
		"今日のネタこれ。音で思い出すものってある？",
		"ちょっと聞きたい。落ち着く時間ってどんな時間？",
	}
	return items[h.rng.Intn(len(items))]
}

func (h *Handler) fallbackTradeReply(userText, userName, mode string) string {
	switch mode {
	case "hirogeru":
		items := []string{
			"そこちょっと気になる。じゃあ広げるね。最近、前より好きになったものってある？",
			"その話、まだ続きありそう。お返しにこれ。キミが最近よく考えちゃうことって何？",
			"そこからちょっと広がった。今の気分に合う場所ってどこ？",
		}
		return items[h.rng.Intn(len(items))]
	case "asobi":
		items := []string{
			"ネタもらった。じゃあ変化球。今日だけ何かの達人になれるなら何がいい？",
			"それ面白いね。お返しにこれ。もし今の気分が動物なら何っぽい？",
			"いいこと聞いた。じゃあ遊びネタ。部屋にひとつだけ謎アイテム置くなら何がいい？",
		}
		return items[h.rng.Intn(len(items))]
	default:
		items := []string{
			"それちょっと気になる。ネタもらった。じゃあお返しにこれ！ 最近つい見ちゃうものってある？",
			"そこ面白いね。いいこと聞いたから、お返しにこれ聞いていい？ 今の気分に合う場所ってどこ？",
			"その話の続き、ちょっと気になる。ねぇねぇ、ネタトレしよ！ 最近ハマってるものってある？",
		}
		return items[h.rng.Intn(len(items))]
	}
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

type openAIResponsesRequest struct {
	Model       string            `json:"model"`
	Input       []openAIInputItem `json:"input"`
	Temperature float64           `json:"temperature,omitempty"`
}

type openAIInputItem struct {
	Role    string `json:"role"`
	Type    string `json:"type,omitempty"`
	Content string `json:"content"`
}

type openAIResponsesResponse struct {
	Output []openAIOutputItem `json:"output"`
}

type openAIOutputItem struct {
	Type    string                `json:"type"`
	Role    string                `json:"role,omitempty"`
	Content []openAIOutputContent `json:"content,omitempty"`
}

type openAIOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (r openAIResponsesResponse) ExtractText() string {
	var parts []string
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
