package vcs

import (
	"strings"
	"testing"
)

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in      string
		host    string
		owner   string
		repo    string
		project string
	}{
		{"git@github.com:chuntley/go-ralph-go.git", "github.com", "chuntley", "go-ralph-go", "chuntley/go-ralph-go"},
		{"https://github.com/foo/bar.git", "github.com", "foo", "bar", "foo/bar"},
		{"https://github.com/foo/bar", "github.com", "foo", "bar", "foo/bar"},
		{"ssh://git@gitlab.com/group/sub/repo.git", "gitlab.com", "group", "repo", "group/sub/repo"},
		{"git@gitlab.example.com:group/repo.git", "gitlab.example.com", "group", "repo", "group/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRemote(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Host != tc.host || got.Owner != tc.owner || got.Repo != tc.repo || got.Project != tc.project {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestGuessProvider(t *testing.T) {
	cases := map[string]string{
		"github.com":         "github",
		"gitlab.com":         "gitlab",
		"gitlab.example.com": "gitlab",
		"bitbucket.org":      "",
	}
	for host, want := range cases {
		if got := GuessProvider(host); got != want {
			t.Errorf("GuessProvider(%q)=%q, want %q", host, got, want)
		}
	}
}

func TestParseRemoteErrors(t *testing.T) {
	for _, in := range []string{"", "not-a-url", "git@host-no-colon"} {
		if _, err := ParseRemote(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

// TestParseRemoteErrorsRedactURLCredentials guards against credential leakage
// via ParseRemote error messages. Callers (doctor, factory.BuildProvider)
// surface these errors to stdout / stderr; an embedded PAT in a malformed
// origin URL must not appear verbatim in the error string.
func TestParseRemoteErrorsRedactURLCredentials(t *testing.T) {
	// Token built by concatenation so the source file never contains a
	// continuous "ghp_<20+>" substring (GitHub push-protection scans repo
	// content and would reject pushes of this file otherwise).
	token := "ghp_" + strings.Repeat("A", 36)
	cases := []string{
		// url.Parse failure path: invalid character in host triggers url.Parse's
		// own error which echoes the URL.
		"https://x-access-token:" + token + "@bad host/o/r.git",
		// "unrecognised" path: scheme not in the recognised set.
		"ftp://x-access-token:" + token + "@host/o/r.git",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ParseRemote(in)
			if err == nil {
				t.Fatalf("expected ParseRemote(%q) to error", in)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("token leaked into ParseRemote error:\n  in:  %q\n  err: %q", in, err.Error())
			}
		})
	}
}

func TestRedactURLCreds(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://x-access-token:" + "ghp_" + "abc@github.com/o/r.git",
			"https://***@github.com/o/r.git",
		},
		{
			"prefix http://alice:hunter2@example.com/x suffix",
			"prefix http://***@example.com/x suffix",
		},
		{
			"no creds here: https://github.com/o/r",
			"no creds here: https://github.com/o/r",
		},
	}
	for _, tc := range cases {
		if got := redactURLCreds(tc.in); got != tc.want {
			t.Errorf("redactURLCreds(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}
