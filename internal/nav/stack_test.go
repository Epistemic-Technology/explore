package nav

import (
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestStackBackForward(t *testing.T) {
	s := New()
	a := model.NodeID{Kind: model.KindFile, Path: "a.go"}
	b := model.NodeID{Kind: model.KindFile, Path: "b.go"}
	c := model.NodeID{Kind: model.KindFile, Path: "c.go"}
	s.Push(a)
	s.Push(b)
	s.Push(c)
	got, _ := s.Current()
	if got != c {
		t.Fatalf("want c, got %v", got)
	}
	got, _ = s.Back()
	if got != b {
		t.Fatalf("want b, got %v", got)
	}
	got, _ = s.Back()
	if got != a {
		t.Fatalf("want a, got %v", got)
	}
	if _, ok := s.Back(); ok {
		t.Fatal("expected no further back")
	}
	got, _ = s.Forward()
	if got != b {
		t.Fatalf("forward want b, got %v", got)
	}
	// Push from middle drops forward history.
	d := model.NodeID{Kind: model.KindFile, Path: "d.go"}
	s.Push(d)
	if _, ok := s.Forward(); ok {
		t.Fatal("forward should be empty after push")
	}
}

func TestStackDedupesConsecutive(t *testing.T) {
	s := New()
	a := model.NodeID{Kind: model.KindFile, Path: "a.go"}
	s.Push(a)
	s.Push(a)
	if len(s.Breadcrumbs()) != 1 {
		t.Fatalf("expected single crumb, got %d", len(s.Breadcrumbs()))
	}
}
