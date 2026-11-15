package config

import "testing"

func TestCheaperSTTModelFromEnv(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CheaperSTTModel != "whisper-large-v3-turbo" {
		t.Fatalf("CheaperSTTModel по умолчанию = %q", c.CheaperSTTModel)
	}

	t.Setenv("CHEAPER_STT_MODEL", "whisper-test")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CheaperSTTModel != "whisper-test" {
		t.Fatalf("CheaperSTTModel из CHEAPER_STT_MODEL = %q", c.CheaperSTTModel)
	}
}
