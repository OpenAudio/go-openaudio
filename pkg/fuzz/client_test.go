package fuzz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDiscoverValidatorEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/console/api/core-validators-endpoints" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"endpoints":["https://node1.example.com","https://node2.example.com"]}`))
	}))
	defer server.Close()

	endpoints, err := NewClient().DiscoverValidatorEndpoints(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://node1.example.com", "https://node2.example.com"}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("endpoints = %v, want %v", endpoints, want)
	}
}
