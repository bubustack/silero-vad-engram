package engram

import (
	"context"
	"encoding/json"
	"testing"

	sdkengram "github.com/bubustack/bubu-sdk-go/engram"
)

func testSpeechPacketJSON(room string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"type": "speech.audio.v1",
		"room": map[string]any{
			"name": room,
		},
		"participant": map[string]any{
			"identity": "speaker",
		},
		"audio": map[string]any{
			"encoding":   "pcm_s16le",
			"sampleRate": 16000,
			"channels":   1,
			"data":       "AQI=",
		},
	})
	return payload
}

func TestSendTurnPacketModeMirrorsPayloadToBinary(t *testing.T) {
	engine := &SileroVAD{}
	out := make(chan sdkengram.StreamMessage, 1)
	err := engine.sendTurn(
		context.Background(),
		nil,
		out,
		roomInfo{Name: "demo-room", SID: "RM1"},
		participantInfo{Identity: "speaker-1", SID: "PA1"},
		turnChunk{PCM: []byte{1, 2, 3, 4}, SampleRate: 16000, Channels: 1},
		7,
	)
	if err != nil {
		t.Fatalf("sendTurn returned error: %v", err)
	}

	select {
	case msg := <-out:
		if msg.Binary == nil {
			t.Fatal("expected binary payload")
		}
		if string(msg.Payload) != string(msg.Binary.Payload) {
			t.Fatalf("expected mirrored payload, payload=%q binary=%q", string(msg.Payload), string(msg.Binary.Payload))
		}
		var packet turnPacket
		if err := json.Unmarshal(msg.Payload, &packet); err != nil {
			t.Fatalf("failed to decode turn packet: %v", err)
		}
		if packet.Sequence != 7 {
			t.Fatalf("unexpected packet sequence %d", packet.Sequence)
		}
	default:
		t.Fatal("expected emitted turn packet")
	}
}

func TestPacketFromStreamMessagePrefersPayloadOverBinary(t *testing.T) {
	msg := sdkengram.NewInboundMessage(sdkengram.StreamMessage{
		Payload: testSpeechPacketJSON("payload-room"),
		Binary: &sdkengram.BinaryFrame{
			Payload:  testSpeechPacketJSON("binary-room"),
			MimeType: "application/json",
		},
	})

	packet, ok, err := packetFromStreamMessage(msg)
	if err != nil {
		t.Fatalf("packetFromStreamMessage returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected structured packet to be parsed")
	}
	if packet.Room.Name != "payload-room" {
		t.Fatalf("expected payload room to win, got %q", packet.Room.Name)
	}
}

func TestPacketFromStreamMessagePrefersInputsOverPayloadAndBinary(t *testing.T) {
	msg := sdkengram.NewInboundMessage(sdkengram.StreamMessage{
		Inputs:  testSpeechPacketJSON("inputs-room"),
		Payload: testSpeechPacketJSON("payload-room"),
		Binary: &sdkengram.BinaryFrame{
			Payload:  testSpeechPacketJSON("binary-room"),
			MimeType: "application/json",
		},
	})

	packet, ok, err := packetFromStreamMessage(msg)
	if err != nil {
		t.Fatalf("packetFromStreamMessage returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected structured packet to be parsed")
	}
	if packet.Room.Name != "inputs-room" {
		t.Fatalf("expected inputs room to win, got %q", packet.Room.Name)
	}
}

func TestOffloadContentTypeMatchesEncoding(t *testing.T) {
	if got := offloadContentType("wav"); got != "audio/wav" {
		t.Fatalf("expected wav content type, got %q", got)
	}
	if got := offloadContentType("pcm"); got != "audio/L16" {
		t.Fatalf("expected pcm content type, got %q", got)
	}
}
