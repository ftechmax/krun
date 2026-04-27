package helper

import (
	"reflect"
	"testing"
)

func TestHelperServiceArgsIncludesOwner(t *testing.T) {
	got := helperServiceArgs("/home/testuser/.kube/config", " testuser ")
	want := []string{"--kubeconfig", "/home/testuser/.kube/config", "--owner", "testuser"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args: want %v, got %v", want, got)
	}
}
