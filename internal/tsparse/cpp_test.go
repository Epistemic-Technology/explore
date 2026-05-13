package tsparse

import (
	"context"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestParseCPP(t *testing.T) {
	src := []byte(`#include <string>
#include "user.h"

namespace app {

enum class Status { Active, Disabled };

struct Point {
    int x;
    int y;
};

class User {
public:
    User(std::string id, std::string name);
    std::string greet() const;
    std::string id_;
};

User::User(std::string id, std::string name) : id_(id) {}

std::string User::greet() const {
    return "hi";
}

int top_level_fn(int a) {
    return a + 1;
}

}  // namespace app
`)
	pf, err := Parse(context.Background(), "user.cc", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangCPP {
		t.Fatalf("Lang = %q, want cpp", pf.Lang)
	}

	type sig struct {
		kind model.SymbolKind
		recv string
	}
	got := map[string]sig{}
	for _, s := range pf.Symbols {
		key := s.Name
		if s.Receiver != "" {
			key = s.Receiver + "." + s.Name
		}
		got[key] = sig{kind: s.Kind, recv: s.Receiver}
	}

	for _, w := range []string{"app", "User", "Point", "Status"} {
		if got[w].kind != model.SymType {
			t.Errorf("%q kind = %v, want type (full got=%v)", w, got[w].kind, got)
		}
	}
	if got["top_level_fn"].kind != model.SymFunc {
		t.Errorf("top_level_fn = %+v, want func", got["top_level_fn"])
	}
	// In-class declarations:
	for _, w := range []string{"User.greet", "User.User"} {
		if got[w].kind != model.SymMethod {
			t.Errorf("%q kind = %v, want method (got=%v)", w, got[w].kind, got)
		}
	}
	// Out-of-line definitions should attach to receiver via qualified_identifier:
	// these appear *in addition to* the in-class declarations, so we expect at
	// least one method-kind entry whose key starts with "User.".
	if got["User.greet"].kind != model.SymMethod {
		t.Errorf("expected out-of-line User.greet to be method")
	}

	imports := map[string]bool{}
	for _, im := range pf.Imports {
		imports[im] = true
	}
	if !imports["string"] {
		t.Errorf("expected <string> include; got %v", pf.Imports)
	}
	if !imports["user.h"] {
		t.Errorf("expected \"user.h\" include; got %v", pf.Imports)
	}
}
