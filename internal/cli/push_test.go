package cli

import (
	"reflect"
	"testing"
)

func TestPushDestinationParsing(t *testing.T) {
	t.Run("cdp uri becomes http endpoint", func(t *testing.T) {
		got, err := cdpEndpointFromDestination("cdp://127.0.0.1:19222")
		if err != nil {
			t.Fatalf("cdpEndpointFromDestination: %v", err)
		}
		if got != "http://127.0.0.1:19222" {
			t.Fatalf("endpoint = %q", got)
		}
	})

	t.Run("http endpoint passes through without trailing slash", func(t *testing.T) {
		got, err := cdpEndpointFromDestination("http://127.0.0.1:19222/")
		if err != nil {
			t.Fatalf("cdpEndpointFromDestination: %v", err)
		}
		if got != "http://127.0.0.1:19222" {
			t.Fatalf("endpoint = %q", got)
		}
	})

	t.Run("unsupported destination is rejected", func(t *testing.T) {
		if _, err := cdpEndpointFromDestination("sidecar://default"); err == nil {
			t.Fatal("expected error for unsupported destination")
		}
	})
}

func TestPushSSHViaParsing(t *testing.T) {
	t.Run("host alias", func(t *testing.T) {
		target, port, err := parseSSHVia("ssh://aircover1.ws")
		if err != nil {
			t.Fatalf("parseSSHVia: %v", err)
		}
		if target != "aircover1.ws" || port != "" {
			t.Fatalf("target=%q port=%q", target, port)
		}
	})

	t.Run("user and port", func(t *testing.T) {
		target, port, err := parseSSHVia("ssh://thinh_nguyen@example.test:2222")
		if err != nil {
			t.Fatalf("parseSSHVia: %v", err)
		}
		if target != "thinh_nguyen@example.test" || port != "2222" {
			t.Fatalf("target=%q port=%q", target, port)
		}
	})

	t.Run("password rejected", func(t *testing.T) {
		if _, _, err := parseSSHVia("ssh://u:p@example.test"); err == nil {
			t.Fatal("expected password rejection")
		}
	})
}

func TestSSHTunnelArgs(t *testing.T) {
	got := sshTunnelArgs("aircover1.ws", "", "127.0.0.1:51000:127.0.0.1:19222")
	want := []string{"-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-N", "-L", "127.0.0.1:51000:127.0.0.1:19222", "aircover1.ws"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshTunnelArgs without port = %#v, want %#v", got, want)
	}

	got = sshTunnelArgs("thinh_nguyen@example.test", "2222", "127.0.0.1:51000:127.0.0.1:19222")
	want = []string{"-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-N", "-L", "127.0.0.1:51000:127.0.0.1:19222", "-p", "2222", "thinh_nguyen@example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshTunnelArgs with port = %#v, want %#v", got, want)
	}
}
