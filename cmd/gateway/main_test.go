package main

import (
	"io/fs"
	"testing"

	gatewayembed "antigravity-gateway"
)

func TestEmbeddedWebDistFSContainsIndexHTML(t *testing.T) {
	subFS, err := gatewayembed.WebDistFS()
	if err != nil {
		t.Fatalf("WebDistFS failed: %v", err)
	}
	b, err := fs.ReadFile(subFS, "index.html")
	if err != nil {
		t.Fatalf("expected index.html in embedded web/dist FS: %v", err)
	}
	if len(b) == 0 {
		t.Fatalf("expected non-empty index.html")
	}
}
