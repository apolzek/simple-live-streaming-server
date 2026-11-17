package main

import (
	"strings"
	"testing"
)

func TestParseLadder(t *testing.T) {
	l, err := ParseLadder(DefaultLadder)
	if err != nil || len(l) != 3 || l[0].Name != "480p" || l[0].Height != 480 || l[0].VideoKbps != 1400 {
		t.Fatalf("ladder padrão: %v %v", l, err)
	}
	for _, bad := range []string{"480p:480:1400", "x:a:1:1", "x:0:1:1"} {
		if _, err := ParseLadder(bad); err == nil {
			t.Errorf("esperava erro para %q", bad)
		}
	}
}

func TestBuildFFmpegArgsCopyWithAudio(t *testing.T) {
	l, _ := ParseLadder("480p:480:1400:128,360p:360:700:96")
	args := strings.Join(BuildFFmpegArgs("rtsp://m/live/x", "/hls/x", l, "H264", true, 3000), " ")
	for _, want := range []string{
		"[0:v]split=3[v0][v1][vthumb]",
		"-c:v:0 copy -b:v:0 3000k",
		"-c:v:1 libx264 -b:v:1 1400k",
		"-c:v:2 libx264 -b:v:2 700k",
		"v:0,a:0,name:source v:1,a:1,name:480p v:2,a:2,name:360p",
		"-hls_segment_type fmp4",
		"/hls/x/thumb.jpg",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("faltou %q em:\n%s", want, args)
		}
	}
	if n := strings.Count(args, "-map 0:a:0"); n != 3 {
		t.Errorf("esperava 3 maps de áudio, veio %d", n)
	}
}

func TestBuildFFmpegArgsTranscodeNoAudio(t *testing.T) {
	l, _ := ParseLadder("360p:360:700:96")
	args := strings.Join(BuildFFmpegArgs("in", "/o", l, "VP8", false, 0), " ")
	for _, want := range []string{"split=3[vsrc][v0][vthumb]", "-map [vsrc]", "-c:v:0 libx264", "v:0,name:source v:1,name:360p"} {
		if !strings.Contains(args, want) {
			t.Errorf("faltou %q em:\n%s", want, args)
		}
	}
	if strings.Contains(args, "0:a") || strings.Contains(args, "-c:a") {
		t.Errorf("não deveria mapear áudio:\n%s", args)
	}
}

func TestAuthDecide(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	s.Ensure("demo", "Demo", "segredo")
	a := &API{store: s}
	cases := []struct {
		action, path, query, pass, ip string
		want                          bool
	}{
		{"publish", "live/demo", "key=segredo", "", "1.2.3.4", true},
		{"publish", "live/demo", "", "segredo", "1.2.3.4", true},
		{"publish", "live/demo", "key=errado", "", "1.2.3.4", false},
		{"publish", "outro/demo", "key=segredo", "", "1.2.3.4", false},
		{"publish", "live/naoexiste", "key=segredo", "", "1.2.3.4", false},
		{"read", "live/demo", "", "", "1.2.3.4", true},
		{"api", "", "", "", "172.18.0.5", true},
		{"api", "", "", "", "8.8.8.8", false},
	}
	for _, c := range cases {
		if got, why := a.decide(c.action, c.path, c.query, "", c.pass, "", c.ip); got != c.want {
			t.Errorf("%+v: got %v (%s)", c, got, why)
		}
	}
}

func TestStoreKeyRecoverAndRotate(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	s.Ensure("canal", "x", "chave1")
	if k, err := s.Key("canal"); err != nil || k != "chave1" {
		t.Fatalf("Key: %q %v", k, err)
	}
	nk, err := s.RotateKey("canal")
	if err != nil || nk == "chave1" || !s.CheckKey("canal", nk) || s.CheckKey("canal", "chave1") {
		t.Fatalf("RotateKey: %q %v", nk, err)
	}
	if _, err := s.Key("nao_existe"); err != ErrNotFound {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}

func TestStoreDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	s.Ensure("canal", "x", "k")
	if err := s.Delete("canal"); err != nil || s.CheckKey("canal", "k") {
		t.Fatalf("Delete falhou: %v", err)
	}
	if err := s.Delete("canal"); err != ErrNotFound {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}
