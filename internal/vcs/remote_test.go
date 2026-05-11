package vcs

import "testing"

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
