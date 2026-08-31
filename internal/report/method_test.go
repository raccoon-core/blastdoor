package report

import "testing"

func TestParseWishKeepsStatedOrder(t *testing.T) {
	w, err := ParseWish("int=auto,stg=auto,prd=manual")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	want := []string{"int", "stg", "prd"}
	got := w.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if m, ok := w.Method("prd"); !ok || m != Manual {
		t.Errorf("Method(prd) = %q, %v, want manual, true", m, ok)
	}
	if !w.Stated() {
		t.Error("Stated() = false, want true")
	}
}

func TestParseWishEmptyIsNotStated(t *testing.T) {
	w, err := ParseWish("")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	if w.Stated() {
		t.Error("Stated() = true for an empty wish, want false")
	}
}

func TestParseWishRejects(t *testing.T) {
	tests := []struct {
		name, input string
	}{
		{"no equals", "int"},
		{"no environment", "=auto"},
		{"unknown method", "int=sometimes"},
		{"none cannot be wished for", "int=none"},
		{"duplicate environment", "int=auto,int=manual"},
		{"name is not a dotenv key", "my-env=auto"},
		{"name starts with a digit", "1int=auto"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseWish(tc.input); err == nil {
				t.Errorf("ParseWish(%q) = nil error, want one", tc.input)
			}
		})
	}
}

func TestParseWishToleratesSpacingAndTrailingComma(t *testing.T) {
	w, err := ParseWish(" int = auto , prd=manual, ")
	if err != nil {
		t.Fatalf("ParseWish: %v", err)
	}
	if m, _ := w.Method("int"); m != Auto {
		t.Errorf("Method(int) = %q, want auto", m)
	}
	if len(w.Names()) != 2 {
		t.Errorf("Names() = %v, want 2 entries", w.Names())
	}
}
