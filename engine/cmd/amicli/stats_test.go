package main

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"ipcc/engine/ami"
)

// 샘플 녹화(정상 콜 1건 + 포기 콜 1건)의 숫자는 docs/05-ami.md 6절에 적은 값과 같아야 한다.
func TestStatsOnSampleRecording(t *testing.T) {
	f, err := os.Open("../../testdata/sim-normal-and-abandon.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := newRecordingStats()
	if err := st.add(ami.NewReader(f)); err != nil {
		t.Fatal(err)
	}

	if st.messages != 135 || st.events != 133 || st.responses != 2 {
		t.Fatalf("messages=%d events=%d responses=%d, want 135/133/2", st.messages, st.events, st.responses)
	}
	if st.byEvent["Newexten"].count != 25 || st.byEvent["DeviceStateChange"].count != 24 {
		t.Fatalf("Newexten=%d DeviceStateChange=%d, want 25/24",
			st.byEvent["Newexten"].count, st.byEvent["DeviceStateChange"].count)
	}
	normal := st.calls["1792123138.0"]
	if len(st.calls) != 2 || normal == nil || len(normal.channels) != 4 {
		t.Fatalf("calls=%d, normal call=%+v, want 2 calls and 4 channels in the normal one", len(st.calls), normal)
	}
}

func TestCompressSnapshots(t *testing.T) {
	fields := append([]string{"Privilege"}, snapshotFields...)
	for _, f := range snapshotFields {
		fields = append(fields, "Dest"+f)
	}
	fields = append(fields, "Queue", "DestChannel") // 중복은 무시된다
	got := compressSnapshots(fields)
	want := []string{"Privilege", "{채널}", "{Dest 채널}", "Queue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	// 일부만 있으면 줄이지 않는다
	partial := []string{"Channel", "Uniqueid"}
	if got := compressSnapshots(partial); !reflect.DeepEqual(got, partial) {
		t.Fatalf("partial snapshot must stay as is: %v", got)
	}
}

func TestPrintCommandOutput(t *testing.T) {
	m := ami.Message{Fields: []ami.Field{
		{Key: "Response", Value: "Success"},
		{Key: "Output", Value: "support has 0 calls"},
		{Key: "Output", Value: "   No Members"},
	}}
	var b bytes.Buffer
	printCommandOutput(&b, m)
	if got := b.String(); got != "support has 0 calls\n   No Members\n" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(b.String(), "Response") {
		t.Fatal("only Output lines should be printed")
	}
}
