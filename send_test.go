package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/AlonaBot/AlonaBot/adapters/telegram/telegramtest"
	pb "github.com/AlonaBot/AlonaBot/contracts/pb/alonabotv1"
	"github.com/AlonaBot/AlonaBot/sdk/adapter"
	"google.golang.org/protobuf/proto"
)

type sendTestCore struct {
	*recordingCore
	media map[string][]byte
	reads int
}

func (c *sendTestCore) ReadMedia(_ context.Context, token string, offset uint64, length uint32) ([]byte, uint64, bool, error) {
	c.reads++
	content, ok := c.media[token]
	if !ok {
		return nil, 0, false, fmt.Errorf("unknown media token %q", token)
	}
	if length > 1<<20 {
		return nil, 0, false, fmt.Errorf("read length exceeds 1 MiB: %d", length)
	}
	end := min(offset+uint64(length), uint64(len(content)))
	return content[offset:end], end, end == uint64(len(content)), nil
}

// TestSendPlan 验证真实实例的物理发送顺序、媒体上传、部分回执和预检零副作用。
func TestSendPlan(t *testing.T) {
	text := func(value string) *pb.Segment {
		return &pb.Segment{Data: &pb.Segment_Text{Text: &pb.TextSegment{Text: value}}}
	}
	image := func(token string) *pb.Segment {
		return &pb.Segment{Data: &pb.Segment_Image{Image: &pb.ImageSegment{Source: &pb.MediaSource{Source: &pb.MediaSource_MediaRef{MediaRef: token}}}}}
	}
	chain := func() *pb.MessageChain {
		return &pb.MessageChain{Segments: []*pb.Segment{
			{Data: &pb.Segment_Reference{Reference: &pb.ReferenceSegment{OriginalPlatformMessageId: "42"}}},
			text("before"), image("photo-a"), image("photo-b"), text("after"),
			{Data: &pb.Segment_File{File: &pb.FileSegment{Source: &pb.MediaSource{Source: &pb.MediaSource_MediaRef{MediaRef: "document"}}, Filename: proto.String("example.bin")}}},
			{Data: &pb.Segment_Buttons{Buttons: &pb.ButtonsSegment{Rows: []*pb.ButtonRow{{Buttons: []*pb.Button{{Label: "Open", CoreToken: proto.String("core-token")}}}}}}},
		}}
	}
	for _, scenario := range []string{"accepted", "second rejected", "first unknown", "long text", "forward", "missing button token", "markdown entities"} {
		t.Run(scenario, func(t *testing.T) {
			server := telegramtest.NewServer(t, "send-test-secret")
			core := &sendTestCore{recordingCore: newRecordingCore(), media: map[string][]byte{"photo-a": bytes.Repeat([]byte{0, 1, 255}, 400000), "photo-b": []byte("second photo"), "document": []byte("document bytes")}}
			config, err := json.Marshal(map[string]string{"token": "send-test-secret", "api_base_url": server.URL()})
			if err != nil {
				t.Fatal(err)
			}
			running, err := New().Start(context.Background(), adapter.StartRequest{InstanceID: "send-instance", ConfigJSON: config}, core)
			if err != nil {
				t.Fatal(err)
			}
			registerStop(t, running)
			message := chain()
			expectedCode := pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
			switch scenario {
			case "second rejected":
				server.FailMethod("sendMediaGroup", 1, 400, "bad media")
			case "first unknown":
				server.Drop("sendMessage", 1)
			case "long text":
				message.Segments = append(message.Segments, text(strings.Repeat("a", 4097)))
			case "forward":
				message.Segments = append(message.Segments, &pb.Segment{Data: &pb.Segment_Forward{Forward: &pb.ForwardSegment{}}})
				expectedCode = pb.ErrorCode_ERROR_CODE_UNSUPPORTED
			case "missing button token":
				message.Segments[6].GetButtons().Rows[0].Buttons[0].CoreToken = nil
			case "markdown entities":
				segment := text("bad")
				segment.GetText().Format = pb.TextFormat_TEXT_FORMAT_MARKDOWN.Enum()
				segment.GetText().Entities = []*pb.TextEntity{{Type: pb.EntityType_ENTITY_TYPE_BOLD, LengthUtf16: 3}}
				message.Segments = append(message.Segments, segment)
			}
			target := &pb.Target{AdapterId: AdapterID, AdapterInstanceId: "send-instance", AccountId: "1", ChatId: "-123"}
			result, safe := running.Send(context.Background(), target, message)
			requests := server.Requests()
			if result == nil || !proto.Equal(result.Target, target) {
				t.Fatalf("result target: %v", result)
			}
			if scenario != "accepted" && scenario != "second rejected" && scenario != "first unknown" {
				if safe == nil || safe.Code != expectedCode || result.Status != pb.SendStatus_SEND_STATUS_REJECTED || len(result.PartResults) != 0 || len(requests) != 0 || core.reads != 0 {
					t.Fatalf("preflight: %v %v requests=%d reads=%d", result, safe, len(requests), core.reads)
				}
				return
			}
			if len(result.PartResults) != 4 {
				t.Fatalf("parts: %v", result)
			}
			indexes := [][]uint32{{0, 1}, {2, 3}, {4}, {5, 6}}
			for index, part := range result.PartResults {
				if part.PartIndex != uint32(index) || !reflect.DeepEqual(part.SegmentIndexes, indexes[index]) {
					t.Fatalf("part indexes: %v", part)
				}
			}
			switch scenario {
			case "accepted":
				var names []string
				for _, capability := range result.CapabilitiesUsed {
					if capability.Version != 1 || !capability.Supported {
						t.Fatalf("used capability: %v", capability)
					}
					names = append(names, capability.Name)
				}
				slices.Sort(names)
				if !reflect.DeepEqual(names, []string{"message.album", "message.buttons", "message.reference", "message.send"}) {
					t.Fatalf("used capabilities: %v", names)
				}
				if !reflect.DeepEqual(New().Descriptor().Capabilities, running.Capabilities()) {
					t.Fatal("descriptor and instance capabilities differ")
				}
				if safe != nil || result.Status != pb.SendStatus_SEND_STATUS_ACCEPTED || !reflect.DeepEqual(result.PlatformMessageIds, []string{"1", "2", "3", "4", "5"}) || result.GetPrimaryPlatformMessageId() != "1" {
					t.Fatalf("accepted: %v %v", result, safe)
				}
				methods := []string{"sendMessage", "sendMediaGroup", "sendMessage", "sendDocument"}
				if len(requests) != len(methods) {
					t.Fatalf("requests: %v", requests)
				}
				for index, request := range requests {
					if request.Method != methods[index] || result.PartResults[index].Status != pb.PartStatus_PART_STATUS_ACCEPTED {
						t.Fatalf("operation %d: %v", index, request)
					}
				}
				if requests[0].Params["reply_parameters"].(map[string]any)["message_id"] != float64(42) {
					t.Fatal("reference missing")
				}
				for _, request := range requests[1:] {
					if request.Params["reply_parameters"] != nil {
						t.Fatal("reference repeated")
					}
				}
				if len(requests[1].Params["media"].([]any)) != 2 || !bytes.Equal(requests[1].Files["media_2"], core.media["photo-a"]) || !bytes.Equal(requests[1].Files["media_3"], core.media["photo-b"]) || !bytes.Equal(requests[3].Files["media_5"], core.media["document"]) || core.reads != 4 {
					t.Fatal("multipart bytes or chunking mismatch")
				}
				keyboard := requests[3].Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
				if keyboard[0].([]any)[0].(map[string]any)["callback_data"] != "core-token" {
					t.Fatal("button token missing")
				}
				for index, want := range [][]string{{"1"}, {"2", "3"}, {"4"}, {"5"}} {
					if !reflect.DeepEqual(result.PartResults[index].PlatformMessageIds, want) {
						t.Fatalf("part IDs: %v", result.PartResults[index])
					}
				}
			case "second rejected":
				if safe == nil || safe.Code != pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT || result.Status != pb.SendStatus_SEND_STATUS_PARTIAL || len(requests) != 2 || !reflect.DeepEqual(result.PlatformMessageIds, []string{"1"}) {
					t.Fatalf("partial: %v %v", result, safe)
				}
				for index, want := range []pb.PartStatus{pb.PartStatus_PART_STATUS_ACCEPTED, pb.PartStatus_PART_STATUS_REJECTED, pb.PartStatus_PART_STATUS_NOT_ATTEMPTED, pb.PartStatus_PART_STATUS_NOT_ATTEMPTED} {
					if result.PartResults[index].Status != want {
						t.Fatalf("part status: %v", result.PartResults[index])
					}
				}
				if !proto.Equal(result.PartResults[1].Error, safe) {
					t.Fatal("part error lost")
				}
			case "first unknown":
				if safe == nil || result.Status != pb.SendStatus_SEND_STATUS_UNKNOWN || len(requests) != 1 || core.reads != 0 || len(result.PlatformMessageIds) != 0 || result.PartResults[0].Status != pb.PartStatus_PART_STATUS_UNKNOWN {
					t.Fatalf("unknown: %v %v", result, safe)
				}
				for _, part := range result.PartResults[1:] {
					if part.Status != pb.PartStatus_PART_STATUS_NOT_ATTEMPTED {
						t.Fatalf("unexpected attempt: %v", part)
					}
				}
			}
		})
	}
}
