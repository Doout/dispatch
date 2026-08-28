package privateaccess

import "testing"

func TestParseConnectorBootstrap(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"
	if len(token) != 43 {
		t.Fatalf("test token length = %d", len(token))
	}
	command := "curl --fail --silent --show-error --proto '=https' --tlsv1.3 'https://lane.example.com/.well-known/laneway/bootstrap/" + token + "' | sudo bash -s -- '" + token + "'"
	bootstrap, err := ParseConnectorBootstrap(command, "https://lane.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.URL.Host != "lane.example.com" || bootstrap.Key != token {
		t.Fatalf("unexpected bootstrap: %#v", bootstrap)
	}
}

func TestParseConnectorBootstrapRejectsAnotherAuthority(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"
	command := "curl --fail --silent --show-error --proto '=https' --tlsv1.3 'https://other.example.com/.well-known/laneway/bootstrap/" + token + "' | sudo bash -s -- '" + token + "'"
	if _, err := ParseConnectorBootstrap(command, "https://lane.example.com"); err == nil {
		t.Fatal("expected authority mismatch")
	}
}

func TestParseConnectorBootstrapRejectsShellInput(t *testing.T) {
	if _, err := ParseConnectorBootstrap("curl https://lane.example.com/install.sh | sh", "https://lane.example.com"); err == nil {
		t.Fatal("expected unsafe command to be rejected")
	}
}
