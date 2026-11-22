package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlonaBot/AlonaBot/sdk"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"github.com/AlonaBot/AlonaBot/sdk/errcode"
	msg "github.com/AlonaBot/AlonaBot/sdk/message"
	"github.com/AlonaBot/alonabot-adapter-telegram/action"
	"github.com/AlonaBot/alonabot-adapter-telegram/telegramtest"
)

func startCompletion(t *testing.T, server *telegramtest.Server, core adapter.Core) *instance {
	t.Helper()
	running, err := New().Start(t.Context(), startRequest(t, server), core)
	if err != nil {
		t.Fatal(err)
	}
	registerStop(t, running)
	return running.(*instance)
}

// TestMediaSources 固定外部 Bot API 的来源映射、相册边界、URL 按钮和完整预检契约。
func TestMediaSources(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	core := &sendTestCore{recordingCore: newRecordingCore(), media: map[string][]byte{"gif": []byte("GIF89a-media"), "document": []byte("document")}}
	running := startCompletion(t, server, core)
	source := func(id string) msg.MediaSource { return msg.MediaSource{PlatformFile: id} }
	inline := func(data string) msg.MediaSource {
		return msg.MediaSource{InlineBase64: base64.StdEncoding.EncodeToString([]byte(data))}
	}
	chain := msg.Chain{
		msg.Image(source("photo-a")), msg.Image(source("photo-b")),
		msg.Image(msg.MediaSource{MediaRef: "gif", MIME: "image/gif"}),
		msg.VoiceSegment{Source: inline("voice"), Caption: "caption", Duration: 2 * time.Second},
		msg.File(inline("document"), "file.txt"), msg.Image(inline("GIF89a-inline")),
		msg.Image(msg.MediaSource{HTTPSURL: "https://example.test/image.gif", MIME: "image/gif"}),
		msg.Image(source("photo-c")), msg.Text("last"),
		msg.Buttons([]msg.Button{msg.URLButton("Open", "https://example.test"), {Label: "Callback", CoreToken: "core-token"}}),
	}
	result, safe := running.Send(t.Context(), "telegram:9", chain)
	if safe != nil || result.Status != msg.SendAccepted {
		t.Fatalf("send: %+v %v", result, safe)
	}
	requests := server.Requests()
	want := []string{"sendMediaGroup", "sendAnimation", "sendVoice", "sendDocument", "sendAnimation", "sendMediaGroup", "sendMessage"}
	if len(requests) != len(want) {
		t.Fatalf("requests=%+v", requests)
	}
	for n, name := range want {
		if requests[n].Method != name {
			t.Fatalf("method %d=%s want %s", n, requests[n].Method, name)
		}
	}
	if !bytes.Equal(requests[1].Files["media_2"], core.media["gif"]) || string(requests[2].Files["media_3"]) != "voice" || requests[2].Params["duration"] != "2" {
		t.Fatalf("uploads=%+v", requests)
	}
	for _, upload := range []struct {
		request int
		field   string
		want    telegramtest.FileMetadata
	}{
		{1, "media_2", telegramtest.FileMetadata{Filename: "media_2.gif", ContentType: "image/gif"}},
		{2, "media_3", telegramtest.FileMetadata{Filename: "media_3", ContentType: "application/octet-stream"}},
		{3, "media_4", telegramtest.FileMetadata{Filename: "file.txt", ContentType: "application/octet-stream"}},
		{4, "media_5", telegramtest.FileMetadata{Filename: "media_5.gif", ContentType: "image/gif"}},
	} {
		if got := requests[upload.request].FileMetadata[upload.field]; got != upload.want {
			t.Fatalf("%s metadata=%+v want=%+v", upload.field, got, upload.want)
		}
	}
	media := requests[5].Params["media"].([]any)
	if media[0].(map[string]any)["media"] != "https://example.test/image.gif" {
		t.Fatal("https URL was changed")
	}
	buttons := requests[6].Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	if buttons[0].(map[string]any)["url"] != "https://example.test" || buttons[0].(map[string]any)["callback_data"] != nil || buttons[1].(map[string]any)["callback_data"] != "core-token" {
		t.Fatalf("buttons=%+v", buttons)
	}
	before := len(requests)
	_, safe = running.Send(t.Context(), "telegram:9", msg.Chain{msg.Text("before"), msg.Image(msg.MediaSource{InlineBase64: "invalid!"})})
	if safe == nil || len(server.Requests()) != before {
		t.Fatal("invalid later source caused a partial send")
	}
	filename := `报告"2026".txt`
	_, safe = running.Send(t.Context(), "telegram:9", msg.Chain{msg.File(msg.MediaSource{MediaRef: "document", MIME: "text/plain"}, filename)})
	if safe != nil {
		t.Fatal(safe)
	}
	if got := server.Requests()[before].FileMetadata["media_0"]; got.Filename != filename || got.ContentType != "text/plain" {
		t.Fatalf("named file metadata=%+v", got)
	}
}

type interruptedMediaCore struct {
	*recordingCore
	entered chan struct{}
	failure error
}

func (c *interruptedMediaCore) ReadMedia(ctx context.Context, _ string, offset uint64, _ uint32) ([]byte, uint64, bool, error) {
	if c.entered != nil {
		close(c.entered)
		<-ctx.Done()
		return nil, offset, false, ctx.Err()
	}
	if offset > 0 {
		return nil, offset, false, c.failure
	}
	return bytes.Repeat([]byte("x"), mediaReadChunk), mediaReadChunk, false, nil
}

// TestStreamingFailure 保留读取原因和取消，后续物理操作停在未尝试状态。
func TestStreamingFailure(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			server := telegramtest.NewServer(t, "local-secret-token")
			core := &interruptedMediaCore{recordingCore: newRecordingCore(), failure: errcode.PermissionDenied.New("media lease read failed")}
			if cancelRead {
				core.entered = make(chan struct{})
			}
			running := startCompletion(t, server, core)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelRead {
				go func() { <-core.entered; cancel() }()
			}
			result, safe := running.Send(ctx, "telegram:9", msg.Chain{msg.Image(msg.MediaSource{MediaRef: "token"}), msg.Text("after")})
			if safe == nil || result.Status == msg.SendAccepted || len(result.PartResults) != 2 || result.PartResults[1].Status != msg.PartNotAttempted {
				t.Fatalf("result=%+v error=%v", result, safe)
			}
			if !cancelRead && (!strings.Contains(safe.Message, "media lease read failed") || safe.Code != errcode.PermissionDenied) {
				t.Fatalf("read error lost: %v", safe)
			}
			if cancelRead && !strings.Contains(safe.Message, "context canceled") {
				t.Fatalf("cancel error lost: %v", safe)
			}
		})
	}
}

// TestDeleteBatches 覆盖 100/101 边界、同聊顺序和明确失败/未知结果后的停发。
func TestDeleteBatches(t *testing.T) {
	for _, scenario := range []struct {
		count   int
		failure string
	}{{100, ""}, {101, ""}, {201, "rejected"}, {201, "unknown"}} {
		t.Run(fmt.Sprintf("%d-%s", scenario.count, scenario.failure), func(t *testing.T) {
			server := telegramtest.NewServer(t, "local-secret-token")
			running := startCompletion(t, server, newRecordingCore())
			if scenario.failure == "rejected" {
				server.FailMethod("deleteMessages", 2, 403, "delete denied")
			}
			if scenario.failure == "unknown" {
				server.Drop("deleteMessages", 2)
			}
			ids := make([]string, scenario.count)
			for n := range ids {
				ids[n] = strconv.Itoa(n + 1)
			}
			result, safe := running.Delete(t.Context(), "telegram:-9#77", ids)
			if (safe != nil) != (scenario.failure != "") || len(result.Items) != len(ids) {
				t.Fatalf("delete=%+v %v", result, safe)
			}
			requests := server.Requests()
			wantRequests := (len(ids) + 99) / 100
			if scenario.failure != "" {
				wantRequests = 2
			}
			if len(requests) != wantRequests {
				t.Fatalf("requests=%d want=%d", len(requests), wantRequests)
			}
			for n, r := range requests {
				batch := r.Params["message_ids"].([]any)
				if len(batch) > 100 || r.Params["message_thread_id"] != nil || r.Params["chat_id"] != float64(-9) || batch[0] != float64(n*100+1) {
					t.Fatalf("batch=%+v", r)
				}
			}
			for n, item := range result.Items {
				want := msg.PartAccepted
				if scenario.failure != "" && n >= 100 {
					want = msg.PartRejected
					if scenario.failure == "unknown" {
						want = msg.PartUnknown
					}
					if n >= 200 {
						want = msg.PartNotAttempted
					}
				}
				if item.PlatformMessageID != ids[n] || item.Status != want {
					t.Fatalf("item=%+v want=%v", item, want)
				}
			}
			before := len(requests)
			_, safe = running.Delete(t.Context(), "telegram:-9", []string{"1", "bad"})
			if safe == nil || len(server.Requests()) != before {
				t.Fatal("invalid delete was attempted")
			}
		})
	}
}

type instanceActionCaller struct {
	instance *instance
	ctx      context.Context
}

func (c instanceActionCaller) CallAction(to, name string, version uint32, params any, _ ...sdk.SendOption) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	result, safe := c.instance.Action(c.ctx, to, name, version, raw)
	if safe != nil {
		return result, safe
	}
	return result, nil
}

// TestPlatformActions 经带类型公开函数验证全部 14 个 Bot API 映射与结果原文。
func TestPlatformActions(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	server.SetChatAdministrators(-9, 200, json.RawMessage(`{"status":"administrator","user":{"id":7,"is_bot":false,"first_name":"Admin"}}`))
	running := startCompletion(t, server, newRecordingCore())
	caller := instanceActionCaller{instance: running, ctx: t.Context()}
	to := "telegram:-9#77"
	ref := msg.Ref(to, "42", "test-instance")
	cases := []struct {
		name string
		call func() error
	}{
		{"banChatMember", func() error {
			_, err := action.BanChatMember(caller, to, action.BanChatMemberParams{UserID: "7"})
			return err
		}},
		{"unbanChatMember", func() error {
			_, err := action.UnbanChatMember(caller, to, action.UnbanChatMemberParams{UserID: "7"})
			return err
		}},
		{"restrictChatMember", func() error {
			_, err := action.RestrictChatMember(caller, to, action.RestrictChatMemberParams{UserID: "7"})
			return err
		}},
		{"pinChatMessage", func() error {
			_, err := action.PinChatMessage(caller, ref, action.PinChatMessageParams{})
			return err
		}},
		{"unpinChatMessage", func() error {
			_, err := action.UnpinChatMessage(caller, ref, action.UnpinChatMessageParams{})
			return err
		}},
		{"setMessageReaction", func() error {
			_, err := action.SetMessageReaction(caller, ref, action.SetMessageReactionParams{})
			return err
		}},
		{"getChat", func() error {
			chat, err := action.GetChat(caller, to)
			if chat.ID != "-9" {
				t.Errorf("chat=%+v", chat)
			}
			return err
		}},
		{"getChatMember", func() error {
			member, err := action.GetChatMember(caller, to, action.GetChatMemberParams{UserID: "7"})
			if member.User.ID != "7" {
				t.Errorf("member=%+v", member)
			}
			return err
		}},
		{"getChatMemberCount", func() error {
			count, err := action.GetChatMemberCount(caller, to)
			if count != 3 {
				t.Errorf("count=%d", count)
			}
			return err
		}},
		{"getChatAdministrators", func() error {
			members, err := action.GetChatAdministrators(caller, to)
			if len(members) != 1 || members[0].User.ID != "7" {
				t.Errorf("members=%+v", members)
			}
			return err
		}},
		{"setChatTitle", func() error {
			_, err := action.SetChatTitle(caller, to, action.SetChatTitleParams{Title: "Title"})
			return err
		}},
		{"leaveChat", func() error { _, err := action.LeaveChat(caller, to); return err }},
		{"sendChatAction", func() error {
			_, err := action.SendChatAction(caller, to, action.SendChatActionParams{Action: "typing"})
			return err
		}},
		{"sendMessageDraft", func() error {
			_, err := action.SendMessageDraft(caller, "telegram:9#77", action.SendMessageDraftParams{DraftID: "123", Text: "partial", CanStop: true})
			return err
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err != nil {
				t.Fatal(err)
			}
			if server.Calls(test.name) != 1 {
				t.Fatalf("%s was not called", test.name)
			}
		})
	}
	requests := server.Requests()
	for _, r := range requests {
		if r.Params["message_ref"] != nil {
			t.Fatal("message handle leaked to platform")
		}
		if r.Method == "pinChatMessage" || r.Method == "unpinChatMessage" || r.Method == "setMessageReaction" {
			if r.Params["message_id"] != float64(42) {
				t.Fatalf("message id=%+v", r)
			}
		}
		if r.Method == "sendChatAction" || r.Method == "sendMessageDraft" {
			if r.Params["message_thread_id"] != float64(77) {
				t.Fatalf("thread missing: %+v", r)
			}
		} else if r.Params["message_thread_id"] != nil {
			t.Fatalf("unexpected thread: %+v", r)
		}
	}
	full := json.RawMessage(`{"id":4503599627370495,"type":"private","new_platform_field":{"value":1234567890123456789}}`)
	server.SetResult("getChat", full)
	raw, err := action.Call(caller, "telegram:9", "telegram.get_chat", action.Empty{})
	if err != nil || !bytes.Equal(raw, full) {
		t.Fatalf("raw result=%s %v", raw, err)
	}
	chat, err := action.GetChat(caller, "telegram:9")
	if err != nil || chat.ID != "4503599627370495" {
		t.Fatalf("typed id=%+v %v", chat, err)
	}
	server.FailMethod("getChat", server.Calls("getChat")+1, 400, "action rejected with partial result")
	raw, err = action.Call(caller, "telegram:9", "telegram.get_chat", action.Empty{})
	if err == nil || !bytes.Equal(raw, full) {
		t.Fatalf("partial raw result=%s %v", raw, err)
	}
	server.FailMethod("getChat", server.Calls("getChat")+1, 403, "typed action rejected with partial result")
	chat, err = action.GetChat(caller, "telegram:9")
	if err == nil || chat.ID != "4503599627370495" {
		t.Fatalf("typed partial result=%+v %v", chat, err)
	}
	before := len(server.Requests())
	for _, test := range []struct {
		to, name string
		params   any
		code     errcode.Code
	}{
		{to, "telegram.send_message_draft", action.SendMessageDraftParams{DraftID: "1", Text: "draft"}, errcode.Unsupported},
		{"telegram:9", "telegram.send_message_draft", action.SendMessageDraftParams{DraftID: "0", Text: "draft"}, errcode.InvalidArgument},
		{to, "telegram.pin_chat_message", action.PinChatMessageParams{MessageID: "bad"}, errcode.InvalidArgument},
		{to, "telegram.pin_chat_message", action.PinChatMessageParams{MessageID: "0"}, errcode.InvalidArgument},
	} {
		raw, _ := json.Marshal(test.params)
		_, safe := running.Action(t.Context(), test.to, test.name, 1, raw)
		if safe == nil || safe.Code != test.code {
			t.Fatalf("preflight=%v", safe)
		}
	}
	if len(server.Requests()) != before {
		t.Fatal("invalid action was attempted")
	}
}

// TestMemberActionInvalidatesCache 验证成员管理成功会作废接收循环的管理员权限缓存。
func TestMemberActionInvalidatesCache(t *testing.T) {
	server := telegramtest.NewServer(t, "local-secret-token")
	running := startCompletion(t, server, newRecordingCore())
	for _, name := range []string{"telegram.ban_chat_member", "telegram.unban_chat_member", "telegram.restrict_chat_member"} {
		running.administratorsMu.Lock()
		running.administrators[-9] = administratorCache{fetchedAt: time.Now()}
		running.administratorsMu.Unlock()
		_, safe := running.Action(t.Context(), "telegram:-9", name, 1, json.RawMessage(`{"user_id":"7","permissions":{}}`))
		if safe != nil {
			t.Fatal(safe)
		}
		running.administratorsMu.Lock()
		_, exists := running.administrators[-9]
		running.administratorsMu.Unlock()
		if exists {
			t.Fatalf("%s left cached permissions", name)
		}
	}
}

// TestIncomingMappings 固定 voice/audio/video_note/sticker 的平台映射与 chat_title 来源。
func TestIncomingMappings(t *testing.T) {
	i := &instance{id: "test", account: adapter.Account{ID: "1"}}
	for _, test := range []struct {
		field string
		want  any
	}{
		{`"voice":{"file_id":"v","duration":3}`, msg.VoiceSegment{}},
		{`"audio":{"file_id":"a","mime_type":"audio/mpeg"}`, msg.FileSegment{}},
		{`"animation":{"file_id":"a"}`, msg.VideoSegment{}},
		{`"video_note":{"file_id":"n"}`, msg.VideoSegment{}},
		{`"sticker":{"file_id":"s","is_animated":false,"is_video":false}`, msg.ImageSegment{}},
		{`"sticker":{"file_id":"s","is_animated":true}`, nil},
		{`"sticker":{"file_id":"s","is_video":true}`, nil},
	} {
		raw := json.RawMessage(`{"update_id":1,"message":{"message_id":1,"chat":{"id":9,"type":"private","first_name":"First","last_name":"Last"},` + test.field + `}}`)
		events, err := i.normalize(raw)
		if err != nil || len(events) != 1 {
			t.Fatalf("normalize=%v %v", events, err)
		}
		e := events[0]
		if e.ChatTitle != "First Last" {
			t.Fatalf("title=%q", e.ChatTitle)
		}
		chain := e.Message.Chain
		if test.want == nil {
			if len(chain) != 0 {
				t.Fatalf("unmapped sticker=%+v", chain)
			}
			continue
		}
		if len(chain) != 1 || reflect.TypeOf(chain[0]) != reflect.TypeOf(test.want) {
			t.Fatalf("chain=%+v", chain)
		}
	}
}

// TestServiceEvents 保持服务过滤先于内容映射、成员去重、迁移单侧及事件 JSON 字段契约。
func TestServiceEvents(t *testing.T) {
	i := &instance{id: "test", account: adapter.Account{ID: "1"}}
	for _, test := range []struct{ field, payload, event, expected string }{
		{"forum_topic_created", `{"name":"Topic","icon_color":123}`, "telegram.forum_topic_created", `{"name":"Topic","icon_color":123}`},
		{"forum_topic_edited", `{"icon_custom_emoji_id":""}`, "telegram.forum_topic_edited", `{"icon_custom_emoji_id":""}`},
		{"forum_topic_closed", `{}`, "telegram.forum_topic_closed", `{}`},
		{"forum_topic_reopened", `{}`, "telegram.forum_topic_reopened", `{}`},
		{"pinned_message", `{"message_id":42}`, "telegram.message_pinned", `{"message_id":"42"}`},
		{"new_chat_title", `"New title"`, "telegram.chat_title_changed", `{"new_chat_title":"New title"}`},
		{"migrate_from_chat_id", `-8`, "telegram.chat_migrated", `{"migrate_from_chat_id":"-8"}`},
		{"migrate_to_chat_id", `-100`, "", ""}, {"new_chat_members", `[{"id":7}]`, "", ""}, {"left_chat_member", `{"id":7}`, "", ""},
		{"location", `{"latitude":1,"longitude":2}`, "message.created", ""}, {"poll", `{"id":"p"}`, "message.created", ""},
	} {
		raw := json.RawMessage(fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"chat":{"id":-9,"type":"supergroup","title":"Group"},"message_thread_id":77,"is_topic_message":true,"text":"skip malformed entities","entities":[{"type":"bold","offset":999,"length":1}],%q:%s}}`, test.field, test.payload))
		if test.event == "message.created" {
			raw = json.RawMessage(fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"chat":{"id":-9,"type":"supergroup","title":"Group"},"message_thread_id":77,"is_topic_message":true,%q:%s}}`, test.field, test.payload))
		}
		events, err := i.normalize(raw)
		if err != nil {
			t.Fatal(err)
		}
		if test.event == "" {
			if len(events) != 0 {
				t.Fatalf("service leaked: %s", test.field)
			}
			continue
		}
		if len(events) != 1 || events[0].Type != test.event || events[0].ChatTitle != "Group" || events[0].Session != "telegram:-9#77" {
			t.Fatalf("event=%+v", events)
		}
		if test.expected != "" {
			p := events[0].Payload.(*msg.AdapterEventPayload)
			if p.Version != 1 || p.PayloadJSON != test.expected || events[0].Message != nil {
				t.Fatalf("payload=%+v", p)
			}
		} else if len(events[0].Message.Chain) != 0 {
			t.Fatalf("ordinary unmapped content=%+v", events[0].Message.Chain)
		}
	}
	// 官方列表中的其余服务字段都必须跳过，内容映射前的错误实体也不影响游标。
	mapped := map[string]bool{"forum_topic_created": true, "forum_topic_edited": true, "forum_topic_closed": true, "forum_topic_reopened": true, "pinned_message": true, "new_chat_title": true, "migrate_from_chat_id": true}
	for _, field := range strings.Fields(serviceFields) {
		if mapped[field] {
			continue
		}
		raw := json.RawMessage(fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"chat":{"id":-9,"type":"group"},%q:{}}}`, field))
		events, err := i.normalize(raw)
		if err != nil || len(events) != 0 {
			t.Fatalf("service %s=%+v %v", field, events, err)
		}
	}
}

func TestDraftHelperCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := action.Typing(ctx, "telegram:9"); !errors.Is(err, context.Canceled) {
		t.Fatalf("typing=%v", err)
	}
	if _, err := action.StreamDraft(ctx, "telegram:9", "1", make(chan action.DraftUpdate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("draft=%v", err)
	}
}
