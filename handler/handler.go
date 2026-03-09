package handler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mixigroup/mixi2-application-sdk-go/auth"
	constv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/const/v1"
	modelv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/model/v1"
	application_apiv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/service/application_api/v1"
)

// Handler implements event.EventHandler interface.
type Handler struct {
	logger        *slog.Logger
	apiClient     application_apiv1.ApplicationServiceClient
	authenticator auth.Authenticator
	rng           *rand.Rand
}

// NewHandler creates a new Handler.
func NewHandler(apiClient application_apiv1.ApplicationServiceClient, authenticator auth.Authenticator) *Handler {
	return &Handler{
		logger:        slog.Default(),
		apiClient:     apiClient,
		authenticator: authenticator,
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Handle processes events from mixi2.
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

// handleChatMessage handles chat message received events.
func (h *Handler) handleChatMessage(ctx context.Context, ev *modelv1.ChatMessageReceivedEvent) error {
	if ev == nil {
		return nil
	}

	msg := ev.GetMessage()
	if msg == nil {
		return nil
	}

	text := strings.TrimSpace(msg.GetText())
	if text == "" {
		return nil
	}

	reply := h.buildReply(text)
	if strings.TrimSpace(reply) == "" {
		return nil
	}

	authCtx, err := h.authenticator.AuthorizedContext(ctx)
	if err != nil {
		return err
	}

	_, err = h.apiClient.SendChatMessage(authCtx, &application_apiv1.SendChatMessageRequest{
		RoomId: msg.GetRoomId(),
		Text:   &reply,
	})
	if err != nil {
		return err
	}

	h.logger.Info("sent chat reply",
		slog.String("room_id", msg.GetRoomId()),
		slog.String("received_text", text),
		slog.String("reply_text", reply),
	)

	return nil
}

func (h *Handler) buildReply(text string) string {
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(raw)

	switch {
	case lower == "help" || lower == "/help" || lower == "ヘルプ":
		return h.helpMessage()

	case lower == "ping" || lower == "/ping":
		return "pong"

	case lower == "おはよう":
		return greetingByTime("おはようございます")
	case lower == "こんにちは":
		return greetingByTime("こんにちは")
	case lower == "こんばんは":
		return greetingByTime("こんばんは")
	case lower == "おやすみ":
		return "おやすみなさい。今日はここまででも十分です。"

	case lower == "おみくじ" || lower == "/omikuji":
		return h.omikuji()

	case lower == "サイコロ" || lower == "/dice":
		return fmt.Sprintf("🎲 %d が出ました。", h.rng.Intn(6)+1)

	case strings.HasPrefix(lower, "/dice "):
		return h.handleDiceCommand(lower)

	case strings.HasPrefix(lower, "choice "):
		return h.handleChoice(raw[len("choice "):])
	case strings.HasPrefix(lower, "/choice "):
		return h.handleChoice(raw[len("/choice "):])

	case strings.HasPrefix(lower, "reverse "):
		return h.handleReverse(raw[len("reverse "):])
	case strings.HasPrefix(lower, "/reverse "):
		return h.handleReverse(raw[len("/reverse "):])
	}

	switch {
	case containsAny(lower, "ありがとう", "助かった", "たすかった"):
		return randomPick(h.rng,
			"どういたしまして。",
			"お役に立ててよかったです。",
			"またいつでもどうぞ。",
		)

	case containsAny(lower, "疲れた", "つかれた"):
		return randomPick(h.rng,
			"お疲れさまです。少し休むのも大事です。",
			"今日は十分がんばっておられます。",
			"無理を続けず、ひと息入れてください。",
		)

	case containsAny(lower, "眠い", "ねむい"):
		return randomPick(h.rng,
			"少し目を閉じるだけでも違います。",
			"眠いときは判断が鈍りやすいので、短い休憩がおすすめです。",
			"温かい飲み物を飲んで整えるのも良いです。",
		)

	case containsAny(lower, "不安", "ふあん", "怖い", "こわい"):
		return randomPick(h.rng,
			"不安があるときは、次の一歩だけ決めると少し楽になります。",
			"全部を一度に解決しなくて大丈夫です。",
			"まずは一つだけ、今できることを選びましょう。",
		)

	case containsAny(lower, "元気？", "げんき？", "元気", "げんき"):
		return randomPick(h.rng,
			"元気です。今日も動いています。",
			"はい、大丈夫です。何を試しますか？",
			"稼働中です。雑談でもコマンドでもどうぞ。",
		)

	case containsAny(lower, "すごい", "えらい", "いいね"):
		return randomPick(h.rng,
			"ありがとうございます。",
			"そう言っていただけるとうれしいです。",
			"励みになります。",
		)
	}

	return h.defaultReply(raw)
}

func (h *Handler) helpMessage() string {
	return strings.Join([]string{
		"使える機能です。",
		"",
		"help / ヘルプ",
		"ping",
		"おみくじ",
		"サイコロ",
		"/dice 20",
		"choice A, B, C",
		"reverse 文字列",
		"",
		"あいさつや雑談にも軽く反応します。",
	}, "\n")
}

func (h *Handler) omikuji() string {
	type item struct {
		Name string
		Text string
	}

	items := []item{
		{"大吉", "勢いがあります。丁寧に進めるほど良い流れになります。"},
		{"中吉", "安定しています。焦らず進めるのが吉です。"},
		{"小吉", "小さな前進が力になります。"},
		{"吉", "今日は整える行動が向いています。"},
		{"末吉", "前半は準備、後半に流れが出やすい日です。"},
	}

	x := items[h.rng.Intn(len(items))]
	return fmt.Sprintf("おみくじ結果：%s\n%s", x.Name, x.Text)
}

func (h *Handler) handleDiceCommand(text string) string {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return "使い方: /dice 6"
	}

	var max int
	_, err := fmt.Sscanf(parts[1], "%d", &max)
	if err != nil || max <= 0 {
		return "数字を正しく指定してください。例: /dice 20"
	}
	if max > 1000 {
		return "大きすぎます。1000以下にしてください。"
	}

	return fmt.Sprintf("🎲 1〜%d の結果: %d", max, h.rng.Intn(max)+1)
}

func (h *Handler) handleChoice(src string) string {
	candidates := splitCandidates(src)
	if len(candidates) == 0 {
		return "候補がありません。例: choice カレー, ラーメン, うどん"
	}

	chosen := candidates[h.rng.Intn(len(candidates))]
	return fmt.Sprintf("選択結果: %s", chosen)
}

func (h *Handler) handleReverse(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return "反転する文字を入れてください。"
	}
	return reverseString(src)
}

func (h *Handler) defaultReply(text string) string {
	t := strings.TrimSpace(text)

	switch {
	case utf8.RuneCountInString(t) <= 4:
		return fmt.Sprintf("「%s」ですね。もう少し詳しく聞かせてください。", t)
	case strings.HasSuffix(t, "？") || strings.HasSuffix(t, "?"):
		return fmt.Sprintf("「%s」についてですね。もう少し具体的に書いていただければ返しやすいです。", truncate(t, 40))
	default:
		return fmt.Sprintf("受け取りました。「%s」という内容ですね。", truncate(t, 50))
	}
}

func splitCandidates(src string) []string {
	if strings.TrimSpace(src) == "" {
		return nil
	}

	seps := []string{",", "、", "/", "|", "／"}
	tmp := src
	for _, sep := range seps[1:] {
		tmp = strings.ReplaceAll(tmp, sep, ",")
	}

	raw := strings.Split(tmp, ",")
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		x = strings.TrimSpace(x)
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func randomPick(rng *rand.Rand, items ...string) string {
	if len(items) == 0 {
		return ""
	}
	return items[rng.Intn(len(items))]
}

func greetingByTime(base string) string {
	hour := time.Now().Hour()
	switch {
	case hour >= 5 && hour < 11:
		return base + "。朝の流れを整えていきましょう。"
	case hour >= 11 && hour < 17:
		return base + "。ここからでも十分進められます。"
	case hour >= 17 && hour < 23:
		return base + "。今日はここまででも価値があります。"
	default:
		return base + "。遅い時間なので、無理しすぎないでください。"
	}
}
