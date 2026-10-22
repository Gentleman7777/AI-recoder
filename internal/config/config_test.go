package config

import "testing"

func TestWhisperLanguage(t *testing.T) {
	for _, language := range []string{"", "ru", "auto", "en"} {
		t.Run("language_"+language, func(t *testing.T) {
			t.Setenv("WHISPER_LANGUAGE", language)
			want := language
			if want == "" {
				want = "auto"
			}
			if got := Load().WhisperLanguage; got != want {
				t.Fatalf("WhisperLanguage = %q, want %q", got, want)
			}
		})
	}
}
