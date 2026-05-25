package security

import "testing"

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		`..\CON?.txt`:     "CON_.txt",
		` a/b\c : d.pdf `: "c _ d.pdf",
		`NUL`:             "_NUL",
		"":                "unnamed",
	}
	for input, want := range cases {
		got := SanitizeFilename(input)
		if got != want {
			t.Fatalf("SanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsAllowedHost(t *testing.T) {
	allowed := []string{"example.com"}
	if !IsAllowedHost("https://files.example.com/a.zip", allowed) {
		t.Fatal("subdomain should be allowed")
	}
	if IsAllowedHost("https://example.org/a.zip", allowed) {
		t.Fatal("unlisted host should be denied")
	}
	if IsAllowedHost("http://127.0.0.1/a.zip", allowed) {
		t.Fatal("IP host should be denied")
	}
}
